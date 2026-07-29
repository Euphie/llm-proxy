package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/admin"
	adminui "github.com/Euphie/llm-proxy/internal/admin/ui"
	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/proxy"
	"github.com/Euphie/llm-proxy/internal/stats"
)

type Options struct {
	DataDir string
	Version string
}

type App struct {
	db      *sql.DB
	handler http.Handler
	usage   *stats.DB
	gate    *requestGate

	closeOnce     sync.Once
	closeErr      error
	closeUsage    func() error
	closeDatabase func() error
}

func New(options Options) (*App, error) {
	dataDir := options.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "./data"
	}

	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	usage := stats.New(db)
	fail := func(cause error) (*App, error) {
		usageErr := usage.Close()
		databaseErr := db.Close()
		return nil, errors.Join(cause, usageErr, databaseErr)
	}

	accounts := admin.NewAccountStore(db)
	sessions := admin.NewSessionStore(db, time.Now)
	profiles := profile.NewStore(db)
	registry := gateway.NewRegistry()
	client := &http.Client{Timeout: 10 * time.Minute}
	build := func(record profile.Record) (http.Handler, error) {
		runtime, err := record.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve Profile %q: %w", record.Slug, err)
		}
		return proxy.New(runtime, client, usage), nil
	}
	coordinator := gateway.NewCoordinator(profiles, registry, build)

	account, _, err := accounts.EnsureDefault(context.Background())
	if err != nil {
		return fail(fmt.Errorf("ensure default administrator: %w", err))
	}
	if account.MustChangePassword {
		slog.Warn(
			"default administrator credential admin/admin is enabled; this is high risk; change the password immediately",
		)
	}
	records, _, err := profiles.LoadSnapshot(context.Background())
	if err != nil {
		return fail(fmt.Errorf("load Profile snapshot: %w", err))
	}
	for _, record := range records {
		if _, err := record.Resolve(); err != nil {
			return fail(fmt.Errorf("validate persisted Profile %q: %w", record.Slug, err))
		}
	}
	if !account.MustChangePassword {
		if err := coordinator.Reload(context.Background()); err != nil {
			return fail(fmt.Errorf("publish Profile snapshot: %w", err))
		}
	}

	auth := admin.NewAuthService(
		accounts,
		sessions,
		admin.NewLoginLimiter(time.Now),
	)
	activateProfiles := func(ctx context.Context) error {
		account, err := accounts.Get(ctx)
		if err != nil {
			return fmt.Errorf("load administrator for Profile activation: %w", err)
		}
		if account.MustChangePassword {
			return admin.ErrPasswordChangeRequired
		}
		if coordinator.Ready() {
			return nil
		}
		return coordinator.Reload(ctx)
	}
	adminAPI := admin.NewAPI(admin.Dependencies{
		Auth:             auth,
		Profiles:         admin.NewProfileService(profiles, coordinator),
		Stats:            usage,
		DB:               db,
		Version:          options.Version,
		DataDir:          dataDir,
		ActivateProfiles: activateProfiles,
		RuntimeReady:     coordinator.Ready,
	})
	handler := routeApplication(
		adminAPI,
		adminui.NewHandler(),
		gateway.NewRouter(registry),
	)

	gate := &requestGate{next: handler}
	application := &App{
		db:      db,
		handler: gate,
		usage:   usage,
		gate:    gate,
	}
	application.closeUsage = usage.Close
	application.closeDatabase = db.Close
	return application, nil
}

func routeApplication(adminAPI, adminUI, dataPlane http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath := originalRequestPath(r)
		decodedPath, err := url.PathUnescape(rawPath)
		if err != nil {
			decodedPath = r.URL.Path
		}

		switch {
		case withinPath(decodedPath, "/_admin/api"):
			if nonCanonicalAPIPath(rawPath, decodedPath) {
				serveAPINotFound(adminAPI, w, r)
				return
			}
			adminAPI.ServeHTTP(w, r)
		case withinPath(decodedPath, "/_admin/assets"):
			adminUI.ServeHTTP(w, r)
		case withinPath(decodedPath, "/_admin"):
			adminUI.ServeHTTP(w, r)
		default:
			dataPlane.ServeHTTP(w, r)
		}
	})
}

func originalRequestPath(r *http.Request) string {
	if r.RequestURI == "" {
		return r.URL.EscapedPath()
	}
	requestPath, _, _ := strings.Cut(r.RequestURI, "?")
	return requestPath
}

func withinPath(requestPath, prefix string) bool {
	return requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

func nonCanonicalAPIPath(rawPath, decodedPath string) bool {
	lowerRawPath := strings.ToLower(rawPath)
	return path.Clean(decodedPath) != decodedPath ||
		strings.Contains(lowerRawPath, "%2e") ||
		strings.Contains(lowerRawPath, "%2f")
}

func serveAPINotFound(adminAPI http.Handler, w http.ResponseWriter, r *http.Request) {
	rejected := r.Clone(r.Context())
	rejectedURL := *r.URL
	rejectedURL.Path = "/_admin/api//not-found"
	rejectedURL.RawPath = ""
	rejected.URL = &rejectedURL
	rejected.RequestURI = rejectedURL.Path
	adminAPI.ServeHTTP(w, rejected)
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.gate.Close()
		usageErr := a.closeUsage()
		databaseErr := a.closeDatabase()
		a.closeErr = errors.Join(usageErr, databaseErr)
	})
	return a.closeErr
}

type requestGate struct {
	next http.Handler

	mu       sync.Mutex
	closed   bool
	inFlight sync.WaitGroup
}

func (g *requestGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		http.Error(w, "application is shutting down", http.StatusServiceUnavailable)
		return
	}
	g.inFlight.Add(1)
	g.mu.Unlock()

	defer g.inFlight.Done()
	g.next.ServeHTTP(w, r)
}

func (g *requestGate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.inFlight.Wait()
}
