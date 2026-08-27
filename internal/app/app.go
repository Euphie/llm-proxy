package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/admin"
	adminui "github.com/Euphie/llm-proxy/internal/admin/ui"
	"github.com/Euphie/llm-proxy/internal/agenttrajectory"
	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/evalcatalog"
	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/proxy"
	"github.com/Euphie/llm-proxy/internal/routing"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/stats"
	"github.com/Euphie/llm-proxy/internal/strategy"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

type Options struct {
	DataDir string
	Version string
}

type App struct {
	db         *sql.DB
	handler    http.Handler
	usage      *stats.DB
	evaluation *evaluation.Service
	gate       *requestGate

	closeOnce                       sync.Once
	closeErr                        error
	closeAgentTrajectoryMaintenance func() error
	closeEvaluationCatalog          func() error
	closeEvaluation                 func() error
	closePolicyReconciler           func() error
	closeUsage                      func() error
	closeDatabase                   func() error
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
	evaluationStore := evaluation.NewStore(db, time.Now)
	evaluations, err := evaluation.NewService(
		evaluationStore,
		evaluation.ServiceOptions{},
	)
	if err != nil {
		usageErr := usage.Close()
		databaseErr := db.Close()
		return nil, errors.Join(err, usageErr, databaseErr)
	}
	closeEvaluationCatalog := func() error { return nil }
	closeAgentTrajectoryMaintenance := func() error { return nil }
	closePolicyReconciler := func() error { return nil }
	fail := func(cause error) (*App, error) {
		agentTrajectoryMaintenanceErr := closeAgentTrajectoryMaintenance()
		evaluationCatalogErr := closeEvaluationCatalog()
		evaluationErr := evaluations.Close()
		policyReconcilerErr := closePolicyReconciler()
		usageErr := usage.Close()
		databaseErr := db.Close()
		return nil, errors.Join(
			cause, agentTrajectoryMaintenanceErr, evaluationCatalogErr,
			evaluationErr, policyReconcilerErr, usageErr, databaseErr,
		)
	}
	agentTrajectoryCipher, err := agenttrajectory.OpenCipher(dataDir)
	if err != nil {
		return fail(fmt.Errorf("initialize Agent trajectory encryption: %w", err))
	}
	agentTrajectoryStore := agenttrajectory.NewStore(db, time.Now)
	if _, err := agentTrajectoryStore.MarkInterrupted(context.Background()); err != nil {
		return fail(fmt.Errorf("interrupt stale Agent trajectory evaluations: %w", err))
	}
	agentTrajectories := agenttrajectory.NewCollector(agentTrajectoryStore, agentTrajectoryCipher, time.Now)
	agentTrajectoryMaintenance := agenttrajectory.NewMaintenance(agentTrajectoryStore, time.Now)
	if err := agentTrajectoryMaintenance.Sweep(context.Background()); err != nil {
		return fail(fmt.Errorf("maintain Agent trajectories: %w", err))
	}
	agentTrajectoryMaintenance.Start(0)
	closeAgentTrajectoryMaintenance = func() error {
		agentTrajectoryMaintenance.Close()
		return nil
	}

	routingSessions, err := routing.NewSessionStore(db, time.Now)
	if err != nil {
		return fail(fmt.Errorf("initialize routing Sessions: %w", err))
	}
	accounts := admin.NewAccountStore(db)
	sessions := admin.NewSessionStore(db, time.Now)
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, time.Now)
	runtimeConfigs := runtimeconfig.NewStore(db, time.Now)
	registry := gateway.NewRegistry()
	client := &http.Client{Timeout: 10 * time.Minute}
	var canarySafetyCheck func(
		context.Context, int64, int64, profile.RoutingStrategyConfig,
	) (bool, error)
	buildResolved := func(record profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
		activeRecord := record
		if snapshot.Active.ID != 0 {
			profile.ApplyRoutingStrategyRoles(&activeRecord.Config.AutoRouting, snapshot.Active.Config)
			activeRecord.Config.AutoRouting.Strategy = snapshot.Active.Config
		}
		runtime, err := activeRecord.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve Profile %q: %w", record.Slug, err)
		}
		active := proxy.NewWithAgentTrajectories(
			runtime, client, usage, routingSessions, evaluations, agentTrajectories,
		)
		if snapshot.Canary == nil {
			return active, nil
		}
		canaryRecord := record
		profile.ApplyRoutingStrategyRoles(&canaryRecord.Config.AutoRouting, snapshot.Canary.Config)
		canaryRecord.Config.AutoRouting.Strategy = snapshot.Canary.Config
		canaryRuntime, err := canaryRecord.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve canary strategy for Profile %q: %w", record.Slug, err)
		}
		canary := proxy.NewWithAgentTrajectories(
			canaryRuntime, client, usage, routingSessions, evaluations, agentTrajectories,
		)
		return canaryHandler(
			active, canary, routingSessions, record.ID, snapshot.Canary.ID, snapshot.CanaryBPS,
			func(ctx context.Context) (bool, error) {
				if canarySafetyCheck == nil {
					return true, errors.New("canary safety service unavailable")
				}
				return canarySafetyCheck(ctx, record.ID, snapshot.Canary.ID, snapshot.Canary.Config)
			},
		), nil
	}
	buildStrategy := func(record profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
		return buildResolved(record, snapshot)
	}
	buildAggregate := func(aggregate runtimeconfig.Aggregate) (http.Handler, error) {
		var policy *profile.RoutingPolicyConfig
		if aggregate.Active != nil {
			policy = &aggregate.Active.Policy
		}
		runtime, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, policy)
		if err != nil {
			return nil, fmt.Errorf("resolve Profile %q active Policy: %w", aggregate.Profile.Slug, err)
		}
		runtime.RuntimeRevision = aggregate.State.Revision
		runtime.ActivePolicyVersionID = aggregate.State.ActivePolicyVersionID
		runtime.ModelCatalogRevision = aggregate.State.ModelCatalogRevision
		return proxy.NewWithAgentTrajectories(
			runtime, client, usage, routingSessions, evaluations, agentTrajectories,
		), nil
	}
	runtimeCoordinator := gateway.NewRuntimeCoordinator(registry, buildAggregate)
	build := func(record profile.Record) (http.Handler, error) {
		if record.Config.Version == 2 {
			aggregate, err := runtimeConfigs.Load(context.Background(), record.ID)
			if err != nil {
				return nil, fmt.Errorf("load Profile %q runtime aggregate: %w", record.Slug, err)
			}
			return buildAggregate(aggregate)
		}
		if !record.Config.AutoRouting.Enabled {
			return buildResolved(record, strategy.Snapshot{})
		}
		resolved, snapshot, err := strategies.ResolveRecord(context.Background(), record)
		if err != nil {
			return nil, fmt.Errorf("resolve strategy for Profile %q: %w", record.Slug, err)
		}
		return buildResolved(resolved, snapshot)
	}
	coordinator := gateway.NewCoordinator(profiles, registry, build, buildStrategy)
	modelCatalog, err := modelcatalog.NewService(
		dataDir,
		&http.Client{Timeout: 30 * time.Second},
	)
	if err != nil {
		return fail(fmt.Errorf("initialize model catalog: %w", err))
	}
	evaluationCatalog, err := evalcatalog.NewService(dataDir)
	if err != nil {
		return fail(fmt.Errorf("initialize evaluation catalog: %w", err))
	}
	evaluationCatalogUpdater := evalcatalog.NewUpdater(
		evaluationCatalog,
		modelCatalog,
		evalcatalog.NewRestrictedDownloader(nil, nil),
		[]evalcatalog.SourceAdapter{
			evalcatalog.NewLiveBenchAdapter(),
			evalcatalog.NewBFCLAdapter(),
			evalcatalog.NewVLMEvalKitAdapter(),
			evalcatalog.NewArenaAdapter(),
			evalcatalog.NewSWEBenchAdapter(),
		},
		time.Now,
	)
	closeEvaluationCatalog = evaluationCatalogUpdater.Close
	generationStore := strategy.NewGenerationStore(db, time.Now)
	strategyCompiler := strategycompiler.New(
		modelCatalog,
		evaluationCatalog,
		strategycompiler.NewEvaluationEvidenceProvider(evaluationStore, time.Now),
		time.Now,
	)

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
		if record.Config.Version == 2 {
			aggregate, loadErr := runtimeConfigs.Load(context.Background(), record.ID)
			if loadErr != nil {
				return fail(fmt.Errorf("load Profile %q runtime aggregate: %w", record.Slug, loadErr))
			}
			if _, buildErr := buildAggregate(aggregate); buildErr != nil {
				return fail(buildErr)
			}
			continue
		}
		resolved := record
		if record.Config.AutoRouting.Enabled {
			resolved, _, err = strategies.ResolveRecord(context.Background(), record)
			if err != nil {
				return fail(fmt.Errorf("load routing strategy for Profile %q: %w", record.Slug, err))
			}
		}
		if _, err := resolved.Resolve(); err != nil {
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
	strategyService := admin.NewStrategyService(
		profiles, strategies, coordinator, time.Now, evaluationStore,
	)
	strategyService.EnableGeneration(strategyCompiler, generationStore)
	strategyService.EnableRuntimePolicies(runtimeConfigs)
	canarySafetyCheck = strategyService.CanaryEvidenceUnsafe
	policyService := admin.NewRoutingPolicyService(runtimeConfigs, runtimeCoordinator)
	policyService.EnableGeneration(strategyCompiler)
	if err := applyPendingPolicyMigration(
		context.Background(), dataDir, policyService,
	); err != nil {
		return fail(err)
	}
	policyReconciler := admin.NewRoutingPolicyReconciler(
		db,
		policyService,
		strategyCompiler,
		admin.RoutingPolicyReconcilerOptions{},
	)
	evaluations.SetEvidenceRecordedHook(policyReconciler.MarkDirty)
	usage.SetRoutingTraceRecordedHook(policyReconciler.MarkDirty)
	policyService.EnableAutomaticReconciliation(policyReconciler)
	policyReconciler.Start()
	closePolicyReconciler = func() error {
		evaluations.SetEvidenceRecordedHook(nil)
		usage.SetRoutingTraceRecordedHook(nil)
		policyReconciler.Close()
		return nil
	}
	profileService := admin.NewProfileService(profiles, coordinator, strategies)
	profileService.EnableRuntimeConfiguration(runtimeConfigs, runtimeCoordinator)
	adminAPI := admin.NewAPI(admin.Dependencies{
		Auth:                     auth,
		Profiles:                 profileService,
		Strategies:               strategyService,
		Policies:                 policyService,
		Stats:                    usage,
		DB:                       db,
		Version:                  options.Version,
		DataDir:                  dataDir,
		ActivateProfiles:         activateProfiles,
		RuntimeReady:             coordinator.Ready,
		ModelCatalog:             modelCatalog,
		EvaluationCatalog:        evaluationCatalog,
		EvaluationCatalogUpdater: evaluationCatalogUpdater,
		AgentTrajectories:        agentTrajectoryStore,
		AgentTrajectoryCipher:    agentTrajectoryCipher,
	})
	handler := routeApplication(
		adminAPI,
		adminui.NewHandler(),
		gateway.NewRouter(registry),
	)

	gate := &requestGate{next: handler}
	application := &App{
		db:         db,
		handler:    gate,
		usage:      usage,
		evaluation: evaluations,
		gate:       gate,
	}
	application.closeEvaluation = evaluations.Close
	application.closePolicyReconciler = closePolicyReconciler
	application.closeAgentTrajectoryMaintenance = closeAgentTrajectoryMaintenance
	application.closeEvaluationCatalog = evaluationCatalogUpdater.Close
	application.closeUsage = usage.Close
	application.closeDatabase = db.Close
	return application, nil
}

func applyPendingPolicyMigration(
	ctx context.Context,
	dataDir string,
	policies *admin.RoutingPolicyService,
) error {
	candidate, resultPath, pending, err := database.PendingPolicyMigrationCandidate(dataDir)
	if err != nil || !pending {
		return err
	}
	var legacyConfig profile.Config
	if err := json.Unmarshal(candidate.ProfileConfig, &legacyConfig); err != nil {
		return fmt.Errorf("decode exported Profile for strategy 16: %w", err)
	}
	var strategyConfig profile.RoutingStrategyConfig
	if err := json.Unmarshal(candidate.StrategyConfig, &strategyConfig); err != nil {
		return fmt.Errorf("decode exported strategy 16: %w", err)
	}
	if strategyConfig.Roles == nil {
		strategyConfig.Roles = profile.RoutingStrategyRoles(legacyConfig.AutoRouting)
	}
	target := profile.PolicyFromLegacy(legacyConfig.AutoRouting, strategyConfig)
	overview, err := policies.Overview(ctx, candidate.ProfileID)
	if err != nil {
		return fmt.Errorf("load migrated Profile before strategy 16 import: %w", err)
	}
	status := "applied"
	detail := "Legacy strategy 16 was validated and applied as a new immutable Policy."
	if overview.Active == nil || !reflect.DeepEqual(overview.Active.Policy, target) {
		_, err = policies.Apply(
			ctx, candidate.ProfileID, overview.RuntimeState.Revision, target,
			"Imported from exported legacy strategy 16.",
		)
	}
	if err != nil {
		status = "rejected"
		detail = err.Error()
		slog.Warn("legacy strategy 16 was not applied; migrated active Policy remains live", "error", err)
	} else {
		slog.Info("legacy strategy 16 was applied as the active Routing Policy")
	}
	if err := database.CompletePolicyMigrationCandidate(resultPath, status, detail); err != nil {
		return err
	}
	return nil
}

func canaryHandler(
	active http.Handler,
	canary http.Handler,
	sessions *routing.SessionStore,
	profileID int64,
	strategyID int64,
	canaryBPS int,
	safetyChecks ...func(context.Context) (bool, error),
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := ""
		if values := r.Header.Values(routing.SessionIDHeader); len(values) == 1 {
			sessionID = values[0]
		}
		bucket, identified := sessions.CanaryBucket(r.Header, sessionID, profileID, strategyID)
		if identified && bucket < canaryBPS {
			if len(safetyChecks) > 0 && safetyChecks[0] != nil {
				unsafe, err := safetyChecks[0](r.Context())
				if err != nil || unsafe {
					active.ServeHTTP(w, r)
					return
				}
			}
			canary.ServeHTTP(w, r)
			return
		}
		active.ServeHTTP(w, r)
	})
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
		agentTrajectoryMaintenanceErr := a.closeAgentTrajectoryMaintenance()
		evaluationCatalogErr := a.closeEvaluationCatalog()
		evaluationErr := a.closeEvaluation()
		policyReconcilerErr := a.closePolicyReconciler()
		usageErr := a.closeUsage()
		databaseErr := a.closeDatabase()
		a.closeErr = errors.Join(
			agentTrajectoryMaintenanceErr, evaluationCatalogErr,
			evaluationErr, policyReconcilerErr, usageErr, databaseErr,
		)
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
