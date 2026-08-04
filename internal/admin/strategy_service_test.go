package admin

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

func TestStrategyServiceAtomicallyReloadsCanaryPromotionAndRollback(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	})
	registry := gateway.NewRegistry()
	builder := func(record profile.Record) (http.Handler, error) {
		resolved, snapshot, err := strategies.ResolveRecord(context.Background(), record)
		if err != nil {
			return nil, err
		}
		label := resolved.Config.AutoRouting.Strategy.Name
		if snapshot.Canary != nil {
			label += "|" + snapshot.Canary.Config.Name
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, label)
		}), nil
	}
	strategyBuilder := func(_ profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
		label := snapshot.Active.Config.Name
		if snapshot.Canary != nil {
			label += "|" + snapshot.Canary.Config.Name
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, label)
		}), nil
	}
	coordinator := gateway.NewCoordinator(profiles, registry, builder, strategyBuilder)
	record, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true,
		Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(profiles, strategies, coordinator, func() time.Time {
		return time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	})
	router := gateway.NewRouter(registry)
	assertStrategyRuntime(t, router, "20260802-001")

	overview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := overview.Snapshot.Active.Config
	config.Alias = "候选"
	candidate, err := service.CreateDraft(context.Background(), record.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(context.Background(), record.ID, candidate.ID, strategy.StateDraft, strategy.StateEvaluating); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(context.Background(), record.ID, candidate.ID, strategy.StateEvaluating, strategy.StateReady); err != nil {
		t.Fatal(err)
	}
	canary, err := service.StartCanary(
		context.Background(), record.ID, candidate.ID, 1000, overview.Snapshot.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertStrategyRuntime(t, router, "20260802-001|20260802-002")
	promoted, err := service.Promote(context.Background(), record.ID, canary.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertStrategyRuntime(t, router, "20260802-002")
	if _, err = service.Rollback(context.Background(), record.ID, promoted.Revision); err != nil {
		t.Fatal(err)
	}
	assertStrategyRuntime(t, router, "20260802-001")
}

func TestStrategyServiceOverviewDoesNotBootstrapDisabledAutoRouting(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, time.Now)
	record, err := profiles.Save(context.Background(), profile.SaveInput{
		Slug: "plain", DisplayName: "Plain", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://example.test"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := gateway.NewCoordinator(
		profiles,
		gateway.NewRegistry(),
		func(profile.Record) (http.Handler, error) { return http.NotFoundHandler(), nil },
	)
	service := NewStrategyService(profiles, strategies, coordinator, time.Now)

	overview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Snapshot.Active.ID != 0 || len(overview.Strategies) != 0 {
		t.Fatalf("overview=%+v", overview)
	}
	if _, err := strategies.Snapshot(context.Background(), record.ID); !errors.Is(err, strategy.ErrNotFound) {
		t.Fatalf("Snapshot() error=%v, want ErrNotFound", err)
	}
}

func TestStrategyServiceStartCanaryBuildFailureLeavesControlAndDataPlaneUnchanged(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, time.Now)
	registry := gateway.NewRegistry()
	failCanary := false
	builder := func(record profile.Record) (http.Handler, error) {
		resolved, snapshot, err := strategies.ResolveRecord(context.Background(), record)
		if err != nil {
			return nil, err
		}
		if failCanary && snapshot.Canary != nil {
			return nil, errors.New("injected canary build failure")
		}
		label := resolved.Config.AutoRouting.Strategy.Name
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, label)
		}), nil
	}
	strategyBuilder := func(_ profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
		if failCanary && snapshot.Canary != nil {
			return nil, errors.New("injected canary build failure")
		}
		label := snapshot.Active.Config.Name
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, label)
		}), nil
	}
	coordinator := gateway.NewCoordinator(profiles, registry, builder, strategyBuilder)
	record, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true,
		Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(profiles, strategies, coordinator, time.Now)
	overview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := overview.Snapshot.Active.Config
	candidate, err := service.CreateDraft(context.Background(), record.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(context.Background(), record.ID, candidate.ID, strategy.StateDraft, strategy.StateEvaluating); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(context.Background(), record.ID, candidate.ID, strategy.StateEvaluating, strategy.StateReady); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM routing_strategy_events WHERE profile_id = ?`,
		record.ID,
	).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	failCanary = true
	snapshot, err := service.StartCanary(
		context.Background(), record.ID, candidate.ID, 1000, overview.Snapshot.Revision,
	)
	if err == nil || snapshot.Revision != 0 || snapshot.Active.ID != 0 || snapshot.Canary != nil {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	after, err := strategies.Snapshot(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != overview.Snapshot.Revision ||
		after.Active.ID != overview.Snapshot.Active.ID ||
		after.Canary != nil || after.CanaryBPS != 0 {
		t.Fatalf("strategy snapshot changed after failed build: before=%+v after=%+v", overview.Snapshot, after)
	}
	versions, err := strategies.List(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) < 2 || versions[0].ID != candidate.ID || versions[0].State != strategy.StateReady {
		t.Fatalf("candidate state changed after failed build: %+v", versions)
	}
	var eventsAfter int
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM routing_strategy_events WHERE profile_id = ?`,
		record.ID,
	).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("strategy events changed after failed build: before=%d after=%d", eventsBefore, eventsAfter)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator became unready after a pre-publication build failure")
	}
	assertStrategyRuntime(t, gateway.NewRouter(registry), "20260802-001")
}

func TestStrategyServiceValidatesAndPublishesForDisabledProfileBeforeReenable(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	})
	registry := gateway.NewRegistry()
	failBuild := false
	render := func(snapshot strategy.Snapshot) (http.Handler, error) {
		if failBuild {
			return nil, errors.New("injected disabled-Profile strategy build failure")
		}
		label := snapshot.Active.Config.Name
		if snapshot.Canary != nil {
			label += "|" + snapshot.Canary.Config.Name
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, label)
		}), nil
	}
	build := func(record profile.Record) (http.Handler, error) {
		if !record.Config.AutoRouting.Enabled {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "fallback")
			}), nil
		}
		_, snapshot, err := strategies.ResolveRecord(context.Background(), record)
		if err != nil {
			return nil, err
		}
		return render(snapshot)
	}
	coordinator := gateway.NewCoordinator(
		profiles,
		registry,
		build,
		func(_ profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
			return render(snapshot)
		},
	)
	if _, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "fallback", DisplayName: "Fallback", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://fallback.example"),
	}, true); err != nil {
		t.Fatal(err)
	}
	record, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: false, Config: apiAutoRoutingConfig(),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(profiles, strategies, coordinator, func() time.Time {
		return time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	})
	overview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := overview.Snapshot.Active.Config
	config.Alias = "候选"
	candidate, err := service.CreateDraft(context.Background(), record.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(
		context.Background(), record.ID, candidate.ID, strategy.StateDraft, strategy.StateEvaluating,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Advance(
		context.Background(), record.ID, candidate.ID, strategy.StateEvaluating, strategy.StateReady,
	); err != nil {
		t.Fatal(err)
	}
	before, err := strategies.Snapshot(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsBefore, err := strategies.List(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := strategyEventCount(t, db, record.ID)
	failBuild = true
	failed, err := service.StartCanary(
		context.Background(), record.ID, candidate.ID, 1000, before.Revision,
	)
	if err == nil || failed.Revision != 0 || failed.Active.ID != 0 || failed.Canary != nil {
		t.Fatalf("failed snapshot=%+v err=%v", failed, err)
	}
	afterFailure, err := strategies.Snapshot(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsAfterFailure, err := strategies.List(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterFailure, before) || !reflect.DeepEqual(versionsAfterFailure, versionsBefore) {
		t.Fatalf(
			"failed disabled-Profile build changed strategy state: before=%+v after=%+v versionsBefore=%+v versionsAfter=%+v",
			before, afterFailure, versionsBefore, versionsAfterFailure,
		)
	}
	if got := strategyEventCount(t, db, record.ID); got != eventsBefore {
		t.Fatalf("failed disabled-Profile build changed events: before=%d after=%d", eventsBefore, got)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator became unready after disabled-Profile pre-publication build failure")
	}

	failBuild = false
	published, err := service.StartCanary(
		context.Background(), record.ID, candidate.ID, 1000, before.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != before.Revision+1 || published.Canary == nil || published.Canary.ID != candidate.ID {
		t.Fatalf("published snapshot=%+v", published)
	}
	if got := strategyEventCount(t, db, record.ID); got != eventsBefore+1 {
		t.Fatalf("successful disabled-Profile publication events=%d, want %d", got, eventsBefore+1)
	}
	disabledResponse := httptest.NewRecorder()
	gateway.NewRouter(registry).ServeHTTP(
		disabledResponse,
		httptest.NewRequest(http.MethodPost, "/auto/v1/messages", nil),
	)
	if disabledResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(disabledResponse.Body.String(), `"code":"profile_disabled"`) {
		t.Fatalf("disabled route status=%d body=%q", disabledResponse.Code, disabledResponse.Body.String())
	}

	record, err = coordinator.Save(context.Background(), profile.SaveInput{
		ID: record.ID, Slug: record.Slug, DisplayName: record.DisplayName,
		Enabled: true, Config: record.Config,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	enabledResponse := httptest.NewRecorder()
	gateway.NewRouter(registry).ServeHTTP(
		enabledResponse,
		httptest.NewRequest(http.MethodPost, "/auto/v1/messages", nil),
	)
	if enabledResponse.Code != http.StatusOK || enabledResponse.Body.String() != "20260802-001|20260802-002" {
		t.Fatalf("enabled route status=%d body=%q", enabledResponse.Code, enabledResponse.Body.String())
	}
}

func strategyEventCount(t *testing.T, db *sql.DB, profileID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM routing_strategy_events WHERE profile_id = ?`,
		profileID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStrategyServicePromoteBuildFailureLeavesControlAndDataPlaneUnchanged(t *testing.T) {
	fixture := newAtomicStrategyFixture(t)
	canary, err := fixture.service.StartCanary(
		context.Background(), fixture.record.ID, fixture.candidate.ID, 1000, fixture.initial.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertRuntime(t, "20260802-001|20260802-002")
	fixture.assertFailedPublicationUnchanged(t, func() (strategy.Snapshot, error) {
		return fixture.service.Promote(context.Background(), fixture.record.ID, canary.Revision)
	})
}

func TestStrategyServiceCancelCanaryBuildFailureLeavesControlAndDataPlaneUnchanged(t *testing.T) {
	fixture := newAtomicStrategyFixture(t)
	canary, err := fixture.service.StartCanary(
		context.Background(), fixture.record.ID, fixture.candidate.ID, 1000, fixture.initial.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertRuntime(t, "20260802-001|20260802-002")
	fixture.assertFailedPublicationUnchanged(t, func() (strategy.Snapshot, error) {
		return fixture.service.CancelCanary(context.Background(), fixture.record.ID, canary.Revision)
	})
}

func TestStrategyServiceRollbackBuildFailureLeavesControlAndDataPlaneUnchanged(t *testing.T) {
	fixture := newAtomicStrategyFixture(t)
	canary, err := fixture.service.StartCanary(
		context.Background(), fixture.record.ID, fixture.candidate.ID, 1000, fixture.initial.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := fixture.service.Promote(context.Background(), fixture.record.ID, canary.Revision)
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertRuntime(t, "20260802-002")
	fixture.assertFailedPublicationUnchanged(t, func() (strategy.Snapshot, error) {
		return fixture.service.Rollback(context.Background(), fixture.record.ID, promoted.Revision)
	})
}

func TestStrategyServiceConcurrentSameRevisionPublishesExactlyOneRuntime(t *testing.T) {
	fixture := newAtomicStrategyFixture(t)
	start := make(chan struct{})
	type result struct {
		snapshot strategy.Snapshot
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			snapshot, err := fixture.service.StartCanary(
				context.Background(), fixture.record.ID, fixture.candidate.ID, 1000, fixture.initial.Revision,
			)
			results <- result{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	successes := 0
	conflicts := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			if result.snapshot.Revision != fixture.initial.Revision+1 ||
				result.snapshot.Canary == nil || result.snapshot.Canary.ID != fixture.candidate.ID {
				t.Fatalf("successful snapshot=%+v", result.snapshot)
			}
		case errors.Is(result.err, strategy.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected publication error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	databaseSnapshot, err := fixture.strategies.Snapshot(context.Background(), fixture.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := fixture.service.Overview(context.Background(), fixture.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overview.Snapshot, databaseSnapshot) ||
		databaseSnapshot.Revision != fixture.initial.Revision+1 ||
		databaseSnapshot.Canary == nil || databaseSnapshot.Canary.ID != fixture.candidate.ID {
		t.Fatalf("database=%+v overview=%+v", databaseSnapshot, overview.Snapshot)
	}
	fixture.assertRuntime(t, "20260802-001|20260802-002")
}

func TestStrategyServiceSuccessfulPublicationSwapsOnlyNewRequests(t *testing.T) {
	fixture := newAtomicStrategyFixture(t)
	fixture.blockLabel = "20260802-001"
	fixture.entered = make(chan struct{})
	fixture.release = make(chan struct{})
	router := gateway.NewRouter(fixture.registry)
	oldResponse := httptest.NewRecorder()
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		router.ServeHTTP(oldResponse, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	}()
	<-fixture.entered

	published, err := fixture.service.StartCanary(
		context.Background(), fixture.record.ID, fixture.candidate.ID, 1000, fixture.initial.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != fixture.initial.Revision+1 {
		t.Fatalf("published revision=%d", published.Revision)
	}
	databaseSnapshot, err := fixture.strategies.Snapshot(context.Background(), fixture.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(databaseSnapshot, published) {
		t.Fatalf("database=%+v published=%+v", databaseSnapshot, published)
	}
	fixture.assertRuntime(t, "20260802-001|20260802-002")

	close(fixture.release)
	<-oldDone
	if oldResponse.Code != http.StatusOK || oldResponse.Body.String() != "20260802-001" {
		t.Fatalf("in-flight status=%d body=%q", oldResponse.Code, oldResponse.Body.String())
	}
}

type atomicStrategyFixture struct {
	db          *sql.DB
	strategies  *strategy.Store
	registry    *gateway.Registry
	coordinator *gateway.Coordinator
	service     *StrategyService
	record      profile.Record
	initial     strategy.Snapshot
	candidate   strategy.Version
	failBuild   bool
	blockLabel  string
	entered     chan struct{}
	release     chan struct{}
	blockOnce   sync.Once
}

func newAtomicStrategyFixture(t *testing.T) *atomicStrategyFixture {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, func() time.Time { return now })
	registry := gateway.NewRegistry()
	fixture := &atomicStrategyFixture{db: db, strategies: strategies, registry: registry}
	render := func(snapshot strategy.Snapshot) (http.Handler, error) {
		if fixture.failBuild {
			return nil, errors.New("injected strategy build failure")
		}
		label := snapshot.Active.Config.Name
		if snapshot.Canary != nil {
			label += "|" + snapshot.Canary.Config.Name
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if fixture.blockLabel == label && fixture.entered != nil && fixture.release != nil {
				fixture.blockOnce.Do(func() { close(fixture.entered) })
				<-fixture.release
			}
			_, _ = io.WriteString(w, label)
		}), nil
	}
	build := func(record profile.Record) (http.Handler, error) {
		_, snapshot, err := strategies.ResolveRecord(context.Background(), record)
		if err != nil {
			return nil, err
		}
		return render(snapshot)
	}
	fixture.coordinator = gateway.NewCoordinator(
		profiles,
		registry,
		build,
		func(_ profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
			return render(snapshot)
		},
	)
	fixture.record, err = fixture.coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = NewStrategyService(profiles, strategies, fixture.coordinator, func() time.Time { return now })
	overview, err := fixture.service.Overview(context.Background(), fixture.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.initial = overview.Snapshot
	config := overview.Snapshot.Active.Config
	config.Alias = "候选"
	fixture.candidate, err = fixture.service.CreateDraft(context.Background(), fixture.record.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.service.Advance(
		context.Background(), fixture.record.ID, fixture.candidate.ID, strategy.StateDraft, strategy.StateEvaluating,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.service.Advance(
		context.Background(), fixture.record.ID, fixture.candidate.ID, strategy.StateEvaluating, strategy.StateReady,
	); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *atomicStrategyFixture) assertFailedPublicationUnchanged(
	t *testing.T,
	publish func() (strategy.Snapshot, error),
) {
	t.Helper()
	before, err := f.strategies.Snapshot(context.Background(), f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsBefore, err := f.strategies.List(context.Background(), f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := f.eventCount(t)
	f.failBuild = true
	returned, err := publish()
	if err == nil || returned.Revision != 0 || returned.Active.ID != 0 || returned.Canary != nil {
		t.Fatalf("returned snapshot=%+v err=%v", returned, err)
	}
	after, err := f.strategies.Snapshot(context.Background(), f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsAfter, err := f.strategies.List(context.Background(), f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("strategy snapshot changed after failed build: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(versionsAfter, versionsBefore) {
		t.Fatalf("strategy versions changed after failed build: before=%+v after=%+v", versionsBefore, versionsAfter)
	}
	if eventsAfter := f.eventCount(t); eventsAfter != eventsBefore {
		t.Fatalf("strategy events changed after failed build: before=%d after=%d", eventsBefore, eventsAfter)
	}
	if !f.coordinator.Ready() {
		t.Fatal("Coordinator became unready after a pre-publication build failure")
	}
}

func (f *atomicStrategyFixture) assertRuntime(t *testing.T, want string) {
	t.Helper()
	assertStrategyRuntime(t, gateway.NewRouter(f.registry), want)
}

func (f *atomicStrategyFixture) eventCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM routing_strategy_events WHERE profile_id = ?`,
		f.record.ID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStrategyServiceGeneratesAnUnpublishedDraftFromReliableEvidence(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, func() time.Time { return now })
	evidence := evaluation.NewStore(db, func() time.Time { return now })
	registry := gateway.NewRegistry()
	coordinator := gateway.NewCoordinator(profiles, registry, func(record profile.Record) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	record, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(
		profiles, strategies, coordinator, func() time.Time { return now }, evidence,
	)
	overview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if err := evidence.RecordEvidence(context.Background(), evaluation.Evidence{
			ProfileID: record.ID, Strategy: overview.Snapshot.Active.Config.Name,
			Route: "balanced", TaskType: "simple", Difficulty: "easy",
			Risk: "normal", VisionMode: "none", CandidateModel: "fast",
			ReferenceModel: "strong", ReviewerModel: "strong",
			Outcome:    evaluation.OutcomeCandidateWin,
			Dimensions: evaluationDimensionOutcomes(evaluation.OutcomeCandidateWin),
		}); err != nil {
			t.Fatal(err)
		}
	}
	evidenceOverview, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidenceOverview.QualityEstimates) != 1 ||
		!evidenceOverview.QualityEstimates[0].Reliable ||
		evidenceOverview.EvaluationBudget.Day != "2026-08-31" {
		t.Fatalf("evidence overview=%+v", evidenceOverview)
	}

	draft, err := service.GenerateCandidate(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.State != strategy.StateDraft || draft.Config.Name != "20260831-001" ||
		draft.Config.Alias != "学习候选 2026-08-31" {
		t.Fatalf("draft=%+v", draft)
	}
	candidate := draft.Config.Routes[0].Candidates[0]
	if candidate.Model != "fast" || candidate.QualityScoreBPS >= 10_000 ||
		candidate.QualityScoreBPS <= 9000 || candidate.QualityScoreBPS == 9200 ||
		candidate.SevereErrorRateBPS <= 0 {
		t.Fatalf("learned candidate=%+v", candidate)
	}
	after, err := service.Overview(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Snapshot.Active.ID != overview.Snapshot.Active.ID || after.Snapshot.Active.Config.Name != "20260802-001" {
		t.Fatalf("candidate generation changed active strategy: %+v", after.Snapshot)
	}
}

func evaluationDimensionOutcomes(outcome evaluation.Outcome) map[evaluation.Dimension]evaluation.Outcome {
	result := make(map[evaluation.Dimension]evaluation.Outcome, len(evaluation.ReviewDimensions))
	for _, dimension := range evaluation.ReviewDimensions {
		result[dimension] = outcome
	}
	return result
}

func TestStrategyServiceRefusesCandidateWithoutReliableEvidence(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, time.Now)
	evidence := evaluation.NewStore(db, time.Now)
	coordinator := gateway.NewCoordinator(profiles, gateway.NewRegistry(), func(profile.Record) (http.Handler, error) {
		return http.NotFoundHandler(), nil
	})
	record, err := coordinator.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(profiles, strategies, coordinator, time.Now, evidence)
	if _, err := service.GenerateCandidate(context.Background(), record.ID); !errors.Is(err, evaluation.ErrInsufficientEvidence) {
		t.Fatalf("GenerateCandidate() error=%v", err)
	}
}

func TestModelPerformanceUsesTheEvidenceStrategyVersionPrior(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	profiles := profile.NewStore(db)
	strategies := strategy.NewStore(db, func() time.Time { return now })
	evidence := evaluation.NewStore(db, func() time.Time { return now })
	record, err := profiles.Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: apiAutoRoutingConfig(),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := strategies.Bootstrap(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	config := cloneStrategyConfig(snapshot.Active.Config)
	config.Name = "20260831-002"
	config.Routes[0].Candidates[0].QualityScoreBPS = 1000
	draft, err := strategies.CreateDraft(context.Background(), record, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.RecordEvidence(context.Background(), evaluation.Evidence{
		ProfileID: record.ID, Strategy: draft.Config.Name, Route: "balanced",
		TaskType: "simple", Difficulty: "easy", Risk: "normal", VisionMode: "none",
		CandidateModel: "fast", ReferenceModel: "strong", ReviewerModel: "strong",
		Outcome:    evaluation.OutcomeCandidateWin,
		Dimensions: evaluationDimensionOutcomes(evaluation.OutcomeCandidateWin),
	}); err != nil {
		t.Fatal(err)
	}
	service := NewStrategyService(profiles, strategies, nil, func() time.Time { return now }, evidence)
	page, err := service.ModelPerformance(context.Background(), evaluation.PerformanceFilter{
		ProfileID: &record.ID, Page: 1, PageSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("performance items=%+v", page.Items)
	}
	if !page.Items[0].PriorKnown || page.Items[0].Estimate.QualityMeanBPS >= 3000 {
		t.Fatalf("strategy prior was not used: %+v", page.Items[0])
	}
}

func assertStrategyRuntime(t *testing.T, handler http.Handler, expected string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != expected {
		t.Fatalf("status=%d body=%q, want %q", response.Code, response.Body.String(), expected)
	}
}
