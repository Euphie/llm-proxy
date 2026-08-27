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

	"github.com/Euphie/llm-proxy/internal/agenttrajectory"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/stats"
)

const (
	maxJSONBodyBytes = 1 << 20
	securityCSP      = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

type Dependencies struct {
	Auth                     *AuthService
	Profiles                 *ProfileService
	Strategies               *StrategyService
	Policies                 *RoutingPolicyService
	Stats                    *stats.DB
	DB                       *sql.DB
	Version                  string
	DataDir                  string
	Logger                   *slog.Logger
	ActivateProfiles         func(context.Context) error
	RuntimeReady             func() bool
	ModelCatalog             ModelCatalogService
	EvaluationCatalog        EvaluationCatalogService
	EvaluationCatalogUpdater EvaluationCatalogUpdater
	AgentTrajectories        AgentTrajectoryStore
	AgentTrajectoryCipher    AgentTrajectoryCipher
}

type API struct {
	auth                     *AuthService
	profiles                 *ProfileService
	strategies               *StrategyService
	policies                 *RoutingPolicyService
	stats                    *stats.DB
	db                       *sql.DB
	version                  string
	dataDir                  string
	logger                   *slog.Logger
	activateProfiles         func(context.Context) error
	runtimeReady             func() bool
	modelCatalog             ModelCatalogService
	evaluationCatalog        EvaluationCatalogService
	evaluationCatalogUpdater EvaluationCatalogUpdater
	agentTrajectories        AgentTrajectoryStore
	agentTrajectoryCipher    AgentTrajectoryCipher
	now                      func() time.Time
	routePaths               *http.ServeMux
	allowedMethods           map[string][]string
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
	activateProfiles := deps.ActivateProfiles
	if activateProfiles == nil {
		activateProfiles = func(context.Context) error { return nil }
	}
	runtimeReady := deps.RuntimeReady
	if runtimeReady == nil {
		runtimeReady = func() bool { return true }
	}
	api := &API{
		auth:                     deps.Auth,
		profiles:                 deps.Profiles,
		strategies:               deps.Strategies,
		policies:                 deps.Policies,
		stats:                    deps.Stats,
		db:                       deps.DB,
		version:                  version,
		dataDir:                  deps.DataDir,
		logger:                   logger,
		activateProfiles:         activateProfiles,
		runtimeReady:             runtimeReady,
		modelCatalog:             deps.ModelCatalog,
		evaluationCatalog:        deps.EvaluationCatalog,
		evaluationCatalogUpdater: deps.EvaluationCatalogUpdater,
		agentTrajectories:        deps.AgentTrajectories,
		agentTrajectoryCipher:    deps.AgentTrajectoryCipher,
		now:                      time.Now,
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
		{method: http.MethodGet, pattern: "/_admin/api/profiles/{id}/routing-policy", handler: a.getRoutingPolicy},
		{method: http.MethodPut, pattern: "/_admin/api/profiles/{id}/routing-policy", handler: a.applyRoutingPolicy},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/routing-policy/generate", handler: a.generateRoutingPolicy},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/routing-policy/rollback", handler: a.rollbackRoutingPolicy},
		{method: http.MethodGet, pattern: "/_admin/api/profiles/{id}/models", handler: a.listProfileModels},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/models", handler: a.mutateProfileModel(runtimeconfig.ModelAdd)},
		{method: http.MethodPut, pattern: "/_admin/api/profiles/{id}/models/update", handler: a.mutateProfileModel(runtimeconfig.ModelUpdate)},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/models/offline", handler: a.offlineProfileModel},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/models/restore", handler: a.mutateProfileModel(runtimeconfig.ModelRestore)},
		{method: http.MethodPost, pattern: "/_admin/api/profiles/{id}/models/retire", handler: a.mutateProfileModel(runtimeconfig.ModelRetire)},
		{method: http.MethodPut, pattern: "/_admin/api/default-profile", handler: a.setDefaultProfile},
		{method: http.MethodGet, pattern: "/_admin/api/stats", handler: a.getStats},
		{method: http.MethodGet, pattern: "/_admin/api/routing-traces", handler: a.getRoutingTraces},
		{method: http.MethodGet, pattern: "/_admin/api/routing-traces/{id}", handler: a.getRoutingTraceDetail},
		{method: http.MethodGet, pattern: "/_admin/api/routing-traces/{id}/session-flow", handler: a.getRoutingSessionFlow},
		{method: http.MethodGet, pattern: "/_admin/api/routing-calls", handler: a.getRoutingCalls},
		{method: http.MethodGet, pattern: "/_admin/api/model-performance", handler: a.getModelPerformance},
		{method: http.MethodGet, pattern: "/_admin/api/agent-trajectories", handler: a.getAgentTrajectories},
		{method: http.MethodGet, pattern: "/_admin/api/agent-trajectories/{id}", handler: a.getAgentTrajectory},
		{method: http.MethodDelete, pattern: "/_admin/api/agent-trajectories/{id}", handler: a.deleteAgentTrajectory},
		{method: http.MethodGet, pattern: "/_admin/api/system", handler: a.getSystem},
		{method: http.MethodGet, pattern: "/_admin/api/model-catalog", handler: a.getModelCatalog},
		{method: http.MethodPost, pattern: "/_admin/api/model-catalog/refresh", handler: a.refreshModelCatalog},
		{method: http.MethodGet, pattern: "/_admin/api/evaluation-catalog", handler: a.getEvaluationCatalog},
		{method: http.MethodPost, pattern: "/_admin/api/evaluation-catalog/update", handler: a.updateEvaluationCatalog},
		{method: http.MethodGet, pattern: "/_admin/api/evaluation-catalog/update-status", handler: a.getEvaluationCatalogUpdateStatus},
	}
}

type AgentTrajectoryStore interface {
	List(context.Context, agenttrajectory.ListFilter) (agenttrajectory.ListResult, error)
	Summary(context.Context, agenttrajectory.ListFilter) (agenttrajectory.Summary, error)
	Get(context.Context, int64) (agenttrajectory.Record, error)
	Delete(context.Context, int64) error
}

type AgentTrajectoryCipher interface {
	Open(agenttrajectory.EncryptedPayload) (agenttrajectory.Plaintext, error)
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
