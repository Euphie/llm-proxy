package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/aggregate"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/stats"
)

const (
	maxJSONBodyBytes = 1 << 20
	securityCSP      = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

type Dependencies struct {
	Auth             *AuthService
	Profiles         *ProfileService
	Aggregate        *AggregateService
	Stats            *stats.DB
	DB               *sql.DB
	Version          string
	DataDir          string
	Logger           *slog.Logger
	ActivateProfiles func(context.Context) error
	RuntimeReady     func() bool
	Now              func() time.Time
}

type API struct {
	auth             *AuthService
	profiles         *ProfileService
	aggregate        *AggregateService
	stats            *stats.DB
	db               *sql.DB
	version          string
	dataDir          string
	logger           *slog.Logger
	activateProfiles func(context.Context) error
	runtimeReady     func() bool
	now              func() time.Time
	routePaths       *http.ServeMux
	allowedMethods   map[string][]string
}

type apiRoute struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

func NewAPI(deps Dependencies) http.Handler {
	version := resolveSystemVersion(deps.Version)
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	aggregateService := deps.Aggregate
	if aggregateService == nil && deps.DB != nil {
		registry := aggregate.NewRegistry()
		aggregateService = NewAggregateService(aggregate.NewStore(deps.DB), registry)
		_ = aggregateService.Reload(context.Background())
	}
	activateProfiles := deps.ActivateProfiles
	if activateProfiles == nil {
		activateProfiles = func(context.Context) error { return nil }
	}
	runtimeReady := deps.RuntimeReady
	if runtimeReady == nil {
		runtimeReady = func() bool { return true }
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	api := &API{
		auth:             deps.Auth,
		profiles:         deps.Profiles,
		aggregate:        aggregateService,
		stats:            deps.Stats,
		db:               deps.DB,
		version:          version,
		dataDir:          deps.DataDir,
		logger:           logger,
		activateProfiles: activateProfiles,
		runtimeReady:     runtimeReady,
		now:              now,
	}

	mux := http.NewServeMux()
	api.routePaths = http.NewServeMux()
	api.allowedMethods = make(map[string][]string)
	for _, route := range api.routes() {
		mux.HandleFunc(route.method+" "+route.pattern, route.handler)
		if _, registered := api.allowedMethods[route.pattern]; !registered {
			api.routePaths.Handle(route.pattern, http.NotFoundHandler())
		}
		api.allowedMethods[route.pattern] = append(
			api.allowedMethods[route.pattern],
			allowedMethodsForRoute(route.method)...,
		)
	}
	mux.HandleFunc("/_admin/api", api.routeFallback)
	mux.HandleFunc("/_admin/api/", api.routeFallback)

	var handler http.Handler = mux
	handler = api.authenticate(handler)
	handler = rejectNonCanonicalPaths(handler)
	handler = securityHeaders(handler)
	return handler
}

func (a *API) routes() []apiRoute {
	return []apiRoute{
		{method: http.MethodPost, pattern: "/_admin/api/login", handler: a.login},
		{method: http.MethodPost, pattern: "/_admin/api/logout", handler: a.logout},
		{method: http.MethodGet, pattern: "/_admin/api/session", handler: a.getSession},
		{method: http.MethodPost, pattern: "/_admin/api/password", handler: a.changePassword},
		{method: http.MethodGet, pattern: "/_admin/api/profiles", handler: a.listProfiles},
		{method: http.MethodPost, pattern: "/_admin/api/profiles", handler: a.createProfile},
		{method: http.MethodGet, pattern: "/_admin/api/profiles/{id}", handler: a.getProfile},
		{method: http.MethodPut, pattern: "/_admin/api/profiles/{id}", handler: a.updateProfile},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/copy", handler: a.copyProfile},
		{method: http.MethodDelete, pattern: "/_admin/api/profiles/{id}", handler: a.deleteProfile},
		{method: http.MethodPut, pattern: "/_admin/api/default-profile", handler: a.setDefaultProfile},
		{method: http.MethodGet, pattern: "/_admin/api/provider-accounts", handler: a.listProviderAccounts},
		{method: http.MethodPost, pattern: "/_admin/api/provider-accounts", handler: a.createProviderAccount},
		{method: http.MethodPut, pattern: "/_admin/api/provider-accounts/{id}", handler: a.updateProviderAccount},
		{method: http.MethodGet, pattern: "/_admin/api/aggregate-gateways", handler: a.listAggregateGateways},
		{method: http.MethodPost, pattern: "/_admin/api/aggregate-gateways", handler: a.createAggregateGateway},
		{method: http.MethodPut, pattern: "/_admin/api/aggregate-gateways/{id}", handler: a.updateAggregateGateway},
		{method: http.MethodGet, pattern: "/_admin/api/aggregate-gateways/{id}/keys", handler: a.listAggregateGatewayKeys},
		{method: http.MethodPost, pattern: "/_admin/api/aggregate-gateways/{id}/keys", handler: a.createAggregateGatewayKey},
		{method: http.MethodGet, pattern: "/_admin/api/stats", handler: a.getStats},
		{method: http.MethodGet, pattern: "/_admin/api/system", handler: a.getSystem},
	}
}

func allowedMethodsForRoute(method string) []string {
	if method == http.MethodGet {
		return []string{http.MethodGet, http.MethodHead}
	}
	return []string{method}
}

func rejectNonCanonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Clean(r.URL.Path) != r.URL.Path {
			writeError(w, http.StatusNotFound, "not_found", "Resource not found.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/_admin/api/login" {
			next.ServeHTTP(w, r)
			return
		}
		principal, err := a.auth.AuthenticateRequest(r)
		if err != nil {
			a.writeDomainError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", securityCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (a *API) routeFallback(w http.ResponseWriter, r *http.Request) {
	_, pattern := a.routePaths.Handler(r)
	if allowed := a.allowedMethods[pattern]; len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", nil)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "Resource not found.", nil)
}

func principalForRequest(r *http.Request) (Principal, error) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	return principal, nil
}

func (a *API) initializationFor(
	ctx context.Context,
	principal Principal,
) (bool, string, error) {
	if principal.MustChangePassword {
		return false, "password_change_required", nil
	}
	_, defaultProfileID, err := a.profiles.List(ctx)
	if err != nil {
		return false, "", err
	}
	if defaultProfileID == 0 {
		return false, "profile_setup_required", nil
	}
	if !a.runtimeReady() {
		if err := a.activateProfiles(ctx); err != nil {
			return false, "", err
		}
	}
	if !a.runtimeReady() {
		return false, "", fmt.Errorf(
			"%w: activation completed without an exact Profile snapshot",
			gateway.ErrRuntimeSync,
		)
	}
	return true, "ready", nil
}

func (a *API) writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.ErrorContext(
		r.Context(),
		"admin API request failed",
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
	writeError(w, http.StatusInternalServerError, "internal_error", "Internal server error.", nil)
}

func closeCookies(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	expired := time.Unix(1, 0)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Path:     authCookiePath,
		MaxAge:   -1,
		Expires:  expired,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Path:     authCookiePath,
		MaxAge:   -1,
		Expires:  expired,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
