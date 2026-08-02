package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

func TestCoordinatorSerializesCommitThroughRuntimePublish(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	initial, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        "coding",
		DisplayName: "Coding",
		Enabled:     true,
		Config:      profile.NewConfig(profile.ProtocolAnthropic, "https://initial.example"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	builder := upstreamBuilder
	if err := registry.Load([]profile.Record{initial}, initial.ID, builder); err != nil {
		t.Fatal(err)
	}

	aPersisted := make(chan struct{})
	releaseA := make(chan struct{})
	blockingStore := &saveBlockingStore{
		Store: store,
		afterSave: func(record profile.Record) {
			if record.Config.Upstream == "https://a.example" {
				close(aPersisted)
				<-releaseA
			}
		},
	}
	coordinator := NewCoordinator(blockingStore, registry, builder)

	aDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Save(context.Background(), profile.SaveInput{
			ID:          initial.ID,
			Slug:        initial.Slug,
			DisplayName: initial.DisplayName,
			Enabled:     true,
			Config: profile.NewConfig(
				profile.ProtocolAnthropic,
				"https://a.example",
			),
		}, false)
		aDone <- err
	}()
	<-aPersisted

	persistedA, err := store.Get(context.Background(), initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedA.Config.Upstream != "https://a.example" {
		t.Fatalf("persisted upstream=%q", persistedA.Config.Upstream)
	}
	if coordinator.mu.TryLock() {
		coordinator.mu.Unlock()
		t.Fatal("coordinator released its writer lock before runtime publish")
	}

	bStarted := make(chan struct{})
	bDone := make(chan error, 1)
	go func() {
		close(bStarted)
		_, err := coordinator.Save(context.Background(), profile.SaveInput{
			ID:          initial.ID,
			Slug:        initial.Slug,
			DisplayName: initial.DisplayName,
			Enabled:     true,
			Config: profile.NewConfig(
				profile.ProtocolAnthropic,
				"https://b.example",
			),
		}, false)
		bDone <- err
	}()
	<-bStarted
	close(releaseA)

	if err := <-aDone; err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("save B: %v", err)
	}

	persistedB, err := store.Get(context.Background(), initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedB.Config.Upstream != "https://b.example" {
		t.Fatalf("persisted upstream=%q", persistedB.Config.Upstream)
	}
	res := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(
		res,
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if res.Code != http.StatusOK || res.Body.String() != "https://b.example" {
		t.Fatalf("runtime status=%d body=%q", res.Code, res.Body.String())
	}
}

func TestCoordinatorPublishesModelCapabilitySnapshotAtomically(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	nativeVision := true
	config := profile.NewConfig(profile.ProtocolAnthropic, "https://upstream.example")
	config.Models = []profile.ModelCapabilityConfig{{
		ID:             "main-model",
		SupportsVision: &nativeVision,
	}}
	initial, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        "coding",
		DisplayName: "Coding",
		Enabled:     true,
		Config:      config,
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	builder := func(record profile.Record) (http.Handler, error) {
		runtime, err := record.Resolve()
		if err != nil {
			return nil, err
		}
		capability, ok := runtime.Models.Lookup("main-model")
		if !ok {
			return nil, errors.New("main-model capability missing")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if capability.SupportsVision {
				close(oldStarted)
				<-releaseOld
				_, _ = io.WriteString(w, "bypass")
				return
			}
			_, _ = io.WriteString(w, "enhance")
		}), nil
	}
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{initial}, initial.ID, builder); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(registry)

	oldResponse := httptest.NewRecorder()
	oldDone := make(chan struct{})
	go func() {
		router.ServeHTTP(
			oldResponse,
			httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		)
		close(oldDone)
	}()
	<-oldStarted

	nativeVision = false
	config.Models[0].SupportsVision = &nativeVision
	coordinator := NewCoordinator(store, registry, builder)
	if _, err := coordinator.Save(context.Background(), profile.SaveInput{
		ID:          initial.ID,
		Slug:        initial.Slug,
		DisplayName: initial.DisplayName,
		Enabled:     true,
		Config:      config,
	}, false); err != nil {
		t.Fatal(err)
	}

	nextResponse := httptest.NewRecorder()
	router.ServeHTTP(
		nextResponse,
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if nextResponse.Code != http.StatusOK || nextResponse.Body.String() != "enhance" {
		t.Fatalf("next request status=%d body=%q", nextResponse.Code, nextResponse.Body.String())
	}

	close(releaseOld)
	<-oldDone
	if oldResponse.Code != http.StatusOK || oldResponse.Body.String() != "bypass" {
		t.Fatalf("in-flight request status=%d body=%q", oldResponse.Code, oldResponse.Body.String())
	}
}

// Break caught: a strategy publication prepared from a stale Profile can resurrect an old slug or configuration.
func TestCoordinatorStrategyPublicationBuildsFromLatestProfileInsideWriterLock(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	initial := saveCoordinatorRecord(t, store, "initial", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{initial}, initial.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Save(context.Background(), profile.SaveInput{
		ID: initial.ID, Slug: "latest", DisplayName: initial.DisplayName, Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://latest.example"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	updated.Config.AutoRouting.Enabled = true
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{updated}, defaultID: updated.ID},
		registry,
		upstreamBuilder,
		func(record profile.Record, _ strategy.Snapshot) (http.Handler, error) {
			return upstreamBuilder(record)
		},
	)
	prospective := strategy.Snapshot{Revision: 2, Active: strategy.Version{ID: 22}}
	committed, err := coordinator.publishStrategy(
		context.Background(), initial.ID, prospective,
		func(context.Context) (strategy.Snapshot, error) { return prospective, nil },
	)
	if err != nil || committed.Revision != prospective.Revision {
		t.Fatalf("committed=%+v err=%v", committed, err)
	}
	assertCoordinatorRoute(t, registry, "/latest/v1/messages", http.StatusOK, updated.Config.Upstream)
	assertCoordinatorRoute(t, registry, "/initial/v1/messages", http.StatusNotFound, "")
}

// Break caught: caller cancellation between preparation and CAS can commit DB state without swapping runtime.
func TestCoordinatorStrategyPublicationUsesBoundedContextIndependentOfCallerCancellation(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "strategy", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	prospective := strategy.Snapshot{Revision: 2, Active: strategy.Version{ID: 22}}
	record.Config.AutoRouting.Enabled = true
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{record}, defaultID: record.ID},
		registry,
		upstreamBuilder,
		func(profile.Record, strategy.Snapshot) (http.Handler, error) {
			cancelCaller()
			return responseHandler("strategy-revision-2"), nil
		},
	)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	commitCalled := false
	committed, err := coordinator.publishStrategy(
		callerCtx, record.ID, prospective,
		func(ctx context.Context) (strategy.Snapshot, error) {
			commitCalled = true
			if err := ctx.Err(); err != nil {
				t.Fatalf("publication context inherited caller cancellation: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 6*time.Second {
				t.Fatalf("publication context has invalid deadline: %v %t", deadline, ok)
			}
			return prospective, nil
		},
	)
	if err != nil || !commitCalled || committed.Revision != prospective.Revision {
		t.Fatalf("committed=%+v commitCalled=%t err=%v", committed, commitCalled, err)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator became unready after successful strategy publication")
	}
	assertCoordinatorRoute(t, registry, "/v1/messages", http.StatusOK, "strategy-revision-2")
}

// Break caught: a builder mutating aliased strategy slices can publish a runtime different from the committed Snapshot.
func TestCoordinatorRejectsStrategyBuilderMutationBeforeCommit(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "strategy", true)
	record.Config.AutoRouting.Enabled = true
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{record}, defaultID: record.ID},
		registry,
		upstreamBuilder,
		func(_ profile.Record, snapshot strategy.Snapshot) (http.Handler, error) {
			snapshot.Active.Config.TaskRoutes[0].Route = "mutated"
			snapshot.Active.Config.Routes[0].Candidates[0].Model = "mutated"
			return responseHandler("mutated-runtime"), nil
		},
	)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()
	prospective := strategy.Snapshot{
		Revision: 2,
		Active: strategy.Version{ID: 22, Config: profile.RoutingStrategyConfig{
			TaskRoutes: []profile.TaskRouteConfig{{TaskType: "simple", Route: "balanced"}},
			Routes: []profile.RouteConfig{{
				ID: "balanced", Candidates: []profile.RouteCandidateConfig{{Model: "fast"}},
			}},
		}},
	}
	commitCalled := false
	committed, err := coordinator.publishStrategy(
		context.Background(), record.ID, prospective,
		func(context.Context) (strategy.Snapshot, error) {
			commitCalled = true
			return prospective, nil
		},
	)
	if err == nil || commitCalled || committed.Revision != 0 {
		t.Fatalf("committed=%+v commitCalled=%t err=%v", committed, commitCalled, err)
	}
	if registry.current.Load() != before {
		t.Fatal("mutating builder changed Registry")
	}
	if !coordinator.Ready() {
		t.Fatal("mutating builder made Coordinator unready")
	}
}

// Break caught: a stale strategy request can publish after a concurrent Profile update disables Auto routing.
func TestCoordinatorRejectsStrategyPublicationWhenLatestProfileDisablesAuto(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "strategy", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	buildCalled := false
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{record}, defaultID: record.ID},
		registry,
		upstreamBuilder,
		func(profile.Record, strategy.Snapshot) (http.Handler, error) {
			buildCalled = true
			return responseHandler("must-not-publish"), nil
		},
	)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()
	commitCalled := false
	_, err = coordinator.publishStrategy(
		context.Background(), record.ID,
		strategy.Snapshot{Revision: 2, Active: strategy.Version{ID: 22}},
		func(context.Context) (strategy.Snapshot, error) {
			commitCalled = true
			return strategy.Snapshot{}, nil
		},
	)
	if !errors.Is(err, profile.ErrInvalidConfig) || buildCalled || commitCalled {
		t.Fatalf("buildCalled=%t commitCalled=%t err=%v", buildCalled, commitCalled, err)
	}
	if registry.current.Load() != before || !coordinator.Ready() {
		t.Fatal("rejected strategy publication changed runtime readiness or Registry")
	}
}

// Break caught: disabled Profiles still need their prospective strategy validated before DB commit.
func TestCoordinatorRejectsDisabledProfileStrategyWhenRuntimeBuildFails(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "strategy", true)
	record.Enabled = false
	record.Config.AutoRouting.Enabled = true
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	buildErr := errors.New("prospective strategy is incompatible with latest model catalog")
	buildCalled := false
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{record}, defaultID: record.ID},
		registry,
		upstreamBuilder,
		func(got profile.Record, _ strategy.Snapshot) (http.Handler, error) {
			buildCalled = true
			if got.Enabled {
				t.Fatal("strategy builder did not receive disabled latest Profile")
			}
			return nil, buildErr
		},
	)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()
	commitCalled := false
	committed, err := coordinator.publishStrategy(
		context.Background(), record.ID,
		strategy.Snapshot{Revision: 2, Active: strategy.Version{ID: 22}},
		func(context.Context) (strategy.Snapshot, error) {
			commitCalled = true
			return strategy.Snapshot{}, nil
		},
	)
	if !errors.Is(err, buildErr) || !buildCalled || commitCalled || committed.Revision != 0 {
		t.Fatalf("committed=%+v buildCalled=%t commitCalled=%t err=%v", committed, buildCalled, commitCalled, err)
	}
	if registry.current.Load() != before || !coordinator.Ready() {
		t.Fatal("rejected disabled-Profile strategy publication changed runtime readiness or Registry")
	}
}

func TestCoordinatorPublishesDisabledProfileWithoutInstallingBuiltHandler(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "strategy", true)
	record.Enabled = false
	record.Config.AutoRouting.Enabled = true
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	buildCalls := 0
	coordinator := NewCoordinator(
		&coordinatorSnapshotStore{ProfileStore: store, records: []profile.Record{record}, defaultID: record.ID},
		registry,
		upstreamBuilder,
		func(profile.Record, strategy.Snapshot) (http.Handler, error) {
			buildCalls++
			return responseHandler("validated"), nil
		},
	)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	prospective := strategy.Snapshot{Revision: 2, Active: strategy.Version{ID: 22}}
	commitCalls := 0
	committed, err := coordinator.publishStrategy(
		context.Background(), record.ID, prospective,
		func(context.Context) (strategy.Snapshot, error) {
			commitCalls++
			return prospective, nil
		},
	)
	if err != nil || committed.Revision != prospective.Revision || buildCalls != 1 || commitCalls != 1 {
		t.Fatalf(
			"committed=%+v buildCalls=%d commitCalls=%d err=%v",
			committed, buildCalls, commitCalls, err,
		)
	}
	item := registry.current.Load().byID[record.ID]
	if item == nil || item.record.Enabled || item.handler != nil {
		t.Fatalf("disabled runtime=%+v, want disabled record with nil handler", item)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator became unready after valid disabled-Profile publication")
	}
}

type coordinatorSnapshotStore struct {
	ProfileStore
	records   []profile.Record
	defaultID int64
}

func (s *coordinatorSnapshotStore) LoadSnapshot(context.Context) ([]profile.Record, int64, error) {
	return append([]profile.Record(nil), s.records...), s.defaultID, nil
}

func TestCoordinatorResyncsAfterIncrementalPublishFailure(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	initial := saveCoordinatorRecord(t, store, "coding", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{initial}, initial.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}

	publishErr := errors.New("transient build failure")
	var updatedBuilds atomic.Int32
	builder := func(record profile.Record) (http.Handler, error) {
		if record.Config.Upstream == "https://updated.example" &&
			updatedBuilds.Add(1) == 1 {
			return nil, publishErr
		}
		return upstreamBuilder(record)
	}
	coordinator := NewCoordinator(store, registry, builder)
	_, err = coordinator.Save(context.Background(), profile.SaveInput{
		ID:          initial.ID,
		Slug:        initial.Slug,
		DisplayName: initial.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://updated.example",
		),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if updatedBuilds.Load() != 2 {
		t.Fatalf("updated Profile builds=%d, want 2", updatedBuilds.Load())
	}

	res := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(
		res,
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if res.Code != http.StatusOK || res.Body.String() != "https://updated.example" {
		t.Fatalf("runtime status=%d body=%q", res.Code, res.Body.String())
	}
}

func TestCoordinatorReportsRuntimeSyncFailureAndKeepsAtomicSnapshot(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	initial := saveCoordinatorRecord(t, store, "coding", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{initial}, initial.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()

	buildErr := errors.New("persistent build failure")
	builder := func(record profile.Record) (http.Handler, error) {
		if record.Config.Upstream == "https://broken.example" {
			return nil, buildErr
		}
		return upstreamBuilder(record)
	}
	coordinator := NewCoordinator(store, registry, builder)
	_, err = coordinator.Save(context.Background(), profile.SaveInput{
		ID:          initial.ID,
		Slug:        initial.Slug,
		DisplayName: initial.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://broken.example",
		),
	}, false)
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, buildErr) {
		t.Fatalf("Save error=%v", err)
	}
	if registry.current.Load() != before {
		t.Fatal("failed incremental publish and resync changed the registry snapshot")
	}

	persisted, err := store.Get(context.Background(), initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Config.Upstream != "https://broken.example" {
		t.Fatalf("persisted upstream=%q", persisted.Config.Upstream)
	}
}

// Break caught: leaving a persisted Profile snapshot unpublished after an external state transition makes it eligible.
func TestCoordinatorReloadPublishesCompleteSnapshot(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "persisted", true)
	registry := NewRegistry()
	coordinator := NewCoordinator(store, registry, upstreamBuilder)

	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if response.Code != http.StatusOK ||
		response.Body.String() != record.Config.Upstream {
		t.Fatalf("runtime status=%d body=%q", response.Code, response.Body.String())
	}
}

// Break caught: exposing a partial or broken snapshot when a full Profile reload cannot build every runtime.
func TestCoordinatorReloadFailureKeepsAtomicSnapshot(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "persisted", true)
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, upstreamBuilder); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()

	if _, err := store.Save(context.Background(), profile.SaveInput{
		ID:          record.ID,
		Slug:        record.Slug,
		DisplayName: record.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://broken.example",
		),
	}, false); err != nil {
		t.Fatal(err)
	}
	buildErr := errors.New("reload build failure")
	coordinator := NewCoordinator(store, registry, func(record profile.Record) (http.Handler, error) {
		if record.Config.Upstream == "https://broken.example" {
			return nil, buildErr
		}
		return upstreamBuilder(record)
	})

	err = coordinator.Reload(context.Background())
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, buildErr) {
		t.Fatalf("Reload error=%v", err)
	}
	if registry.current.Load() != before {
		t.Fatal("failed reload changed the registry snapshot")
	}
}

func TestCoordinatorSetDefaultReusesExistingHandlers(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	first := saveCoordinatorRecord(t, store, "first", true)
	second := saveCoordinatorRecord(t, store, "second", false)
	var builds atomic.Int32
	builder := func(record profile.Record) (http.Handler, error) {
		builds.Add(1)
		return &identityHandler{body: record.Slug}, nil
	}
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{first, second}, first.ID, builder); err != nil {
		t.Fatal(err)
	}
	builds.Store(0)
	before := registry.current.Load()

	coordinator := NewCoordinator(store, registry, builder)
	if err := coordinator.SetDefault(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}

	after := registry.current.Load()
	if after.defaultID != second.ID {
		t.Fatalf("defaultID=%d, want %d", after.defaultID, second.ID)
	}
	if builds.Load() != 0 {
		t.Fatalf("builder calls=%d, want 0", builds.Load())
	}
	for _, id := range []int64{first.ID, second.ID} {
		if after.byID[id] != before.byID[id] ||
			after.byID[id].handler != before.byID[id].handler {
			t.Fatalf("Profile %d runtime or handler was replaced", id)
		}
	}
}

// Break caught: caller cancellation or a failed resync can leave a committed Save invisible to later successful mutations.
func TestCoordinatorSaveRepairsDirtySnapshotBeforeNextMutation(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	initial := saveCoordinatorRecord(t, store, "initial", true)
	unrelated := saveCoordinatorRecord(t, store, "unrelated", false)
	registry := NewRegistry()
	if err := registry.Load(
		[]profile.Record{initial, unrelated},
		initial.ID,
		upstreamBuilder,
	); err != nil {
		t.Fatal(err)
	}

	snapshotErr := errors.New("snapshot temporarily unavailable")
	callerContextKey := coordinatorTestContextKey{}
	callerContextValue := "save-publication"
	callerCtx, cancelCaller := context.WithCancel(
		context.WithValue(
			context.Background(),
			callerContextKey,
			callerContextValue,
		),
	)
	defer cancelCaller()

	faults := &coordinatorFaultStore{Store: store}
	faults.observeLoadContexts(callerContextKey)
	faults.afterSave = func(record profile.Record) {
		if record.ID == initial.ID &&
			record.Config.Upstream == "https://committed.example" {
			faults.setLoadFailure(snapshotErr)
			cancelCaller()
		}
	}
	coordinator := NewCoordinator(faults, registry, upstreamBuilder)

	_, err = coordinator.Save(callerCtx, profile.SaveInput{
		ID:          initial.ID,
		Slug:        initial.Slug,
		DisplayName: initial.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://committed.example",
		),
	}, false)
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, snapshotErr) {
		t.Fatalf("Save error=%v", err)
	}
	if coordinator.Ready() {
		t.Fatal("Coordinator remained ready after failed post-commit synchronization")
	}
	observations := faults.stopObservingLoadContexts()
	if len(observations) != 2 {
		t.Fatalf("post-commit snapshot attempts=%d, want 2", len(observations))
	}
	for index, observation := range observations {
		if observation.err != nil {
			t.Fatalf("post-commit context %d error=%v", index, observation.err)
		}
		if observation.value != callerContextValue {
			t.Fatalf(
				"post-commit context %d value=%v, want %q",
				index,
				observation.value,
				callerContextValue,
			)
		}
		if !observation.hasDeadline ||
			observation.timeout <= 0 ||
			observation.timeout > 6*time.Second {
			t.Fatalf(
				"post-commit context %d has bounded deadline=%t",
				index,
				observation.hasDeadline,
			)
		}
	}
	assertCoordinatorRoute(
		t,
		registry,
		"/v1/messages",
		http.StatusOK,
		"https://initial.example",
	)

	saveCalls := faults.saveCallCount()
	_, err = coordinator.Save(context.Background(), profile.SaveInput{
		ID:          unrelated.ID,
		Slug:        unrelated.Slug,
		DisplayName: unrelated.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://must-not-commit.example",
		),
	}, false)
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, snapshotErr) {
		t.Fatalf("repair-gated Save error=%v", err)
	}
	if faults.saveCallCount() != saveCalls {
		t.Fatal("repair failure did not prevent the new Save")
	}
	persistedUnrelated, err := store.Get(context.Background(), unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedUnrelated.Config.Upstream != unrelated.Config.Upstream {
		t.Fatalf(
			"unrelated persisted upstream=%q",
			persistedUnrelated.Config.Upstream,
		)
	}

	faults.setLoadFailure(nil)
	_, err = coordinator.Save(context.Background(), profile.SaveInput{
		ID:          unrelated.ID,
		Slug:        unrelated.Slug,
		DisplayName: unrelated.DisplayName,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://unrelated-updated.example",
		),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator did not become ready after full repair and publication")
	}
	assertCoordinatorRoute(
		t,
		registry,
		"/v1/messages",
		http.StatusOK,
		"https://committed.example",
	)
	assertCoordinatorRoute(
		t,
		registry,
		"/unrelated/v1/messages",
		http.StatusOK,
		"https://unrelated-updated.example",
	)
}

// Break caught: a committed Delete can remain routable when a later unrelated mutation only publishes incrementally.
func TestCoordinatorDeleteRepairsDirtySnapshotBeforeNextMutation(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	initial := saveCoordinatorRecord(t, store, "initial", true)
	deleted := saveCoordinatorRecord(t, store, "deleted", false)
	replacement := saveCoordinatorRecord(t, store, "replacement", false)
	registry := NewRegistry()
	if err := registry.Load(
		[]profile.Record{initial, deleted, replacement},
		initial.ID,
		upstreamBuilder,
	); err != nil {
		t.Fatal(err)
	}

	snapshotErr := errors.New("delete snapshot temporarily unavailable")
	faults := &coordinatorFaultStore{Store: store}
	faults.afterDelete = func(id int64) {
		if id == deleted.ID {
			faults.setLoadFailure(snapshotErr)
		}
	}
	coordinator := NewCoordinator(faults, registry, upstreamBuilder)

	err = coordinator.Delete(context.Background(), deleted.ID, 0)
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, snapshotErr) {
		t.Fatalf("Delete error=%v", err)
	}
	if coordinator.Ready() {
		t.Fatal("Coordinator remained ready after failed Delete synchronization")
	}
	if _, err := store.Get(context.Background(), deleted.ID); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("persisted deleted Profile error=%v", err)
	}
	assertCoordinatorRoute(
		t,
		registry,
		"/deleted/v1/messages",
		http.StatusOK,
		deleted.Config.Upstream,
	)

	faults.setLoadFailure(nil)
	if err := coordinator.SetDefault(context.Background(), replacement.ID); err != nil {
		t.Fatal(err)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator did not become ready after Delete repair")
	}
	assertCoordinatorRoute(
		t,
		registry,
		"/v1/messages",
		http.StatusOK,
		replacement.Config.Upstream,
	)
	assertCoordinatorRoute(
		t,
		registry,
		"/deleted/v1/messages",
		http.StatusNotFound,
		"",
	)
}

// Break caught: activation and health checks cannot distinguish an unpublished runtime or may rebuild handlers on every check.
func TestCoordinatorReadyTracksReloadWithoutRebuilding(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := profile.NewStore(db)
	record := saveCoordinatorRecord(t, store, "ready", true)
	faults := &coordinatorFaultStore{Store: store}
	registry := NewRegistry()
	var builds atomic.Int32
	coordinator := NewCoordinator(
		faults,
		registry,
		func(record profile.Record) (http.Handler, error) {
			builds.Add(1)
			return upstreamBuilder(record)
		},
	)

	if coordinator.Ready() {
		t.Fatal("new Coordinator is ready before activation")
	}
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator is not ready after successful activation")
	}
	if builds.Load() != 1 {
		t.Fatalf("activation builds=%d, want 1", builds.Load())
	}
	for range 3 {
		if !coordinator.Ready() {
			t.Fatal("ready Coordinator changed state during readiness query")
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("builds after readiness queries=%d, want 1", builds.Load())
	}

	snapshotErr := errors.New("activation snapshot unavailable")
	faults.setLoadFailure(snapshotErr)
	err = coordinator.Reload(context.Background())
	if !errors.Is(err, ErrRuntimeSync) || !errors.Is(err, snapshotErr) {
		t.Fatalf("Reload error=%v", err)
	}
	if coordinator.Ready() {
		t.Fatal("Coordinator remained ready after failed activation")
	}

	faults.setLoadFailure(nil)
	if err := coordinator.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !coordinator.Ready() {
		t.Fatal("Coordinator did not recover readiness")
	}
	if builds.Load() != 2 {
		t.Fatalf("builds after recovery=%d, want 2", builds.Load())
	}
	response := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if response.Code != http.StatusOK ||
		response.Body.String() != record.Config.Upstream {
		t.Fatalf("runtime status=%d body=%q", response.Code, response.Body.String())
	}
}

type saveBlockingStore struct {
	*profile.Store
	afterSave func(profile.Record)
}

func (s *saveBlockingStore) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	record, err := s.Store.Save(ctx, input, makeDefault)
	if err == nil && s.afterSave != nil {
		s.afterSave(record)
	}
	return record, err
}

func saveCoordinatorRecord(
	t *testing.T,
	store *profile.Store,
	slug string,
	makeDefault bool,
) profile.Record {
	t.Helper()
	record, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        slug,
		DisplayName: slug,
		Enabled:     true,
		Config: profile.NewConfig(
			profile.ProtocolAnthropic,
			"https://"+slug+".example",
		),
	}, makeDefault)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

type coordinatorTestContextKey struct{}

type coordinatorLoadContextObservation struct {
	err         error
	value       any
	timeout     time.Duration
	hasDeadline bool
}

type coordinatorFaultStore struct {
	*profile.Store

	mu               sync.Mutex
	loadFailure      error
	loadContextKey   any
	observeLoads     bool
	loadObservations []coordinatorLoadContextObservation
	saveCalls        int
	afterSave        func(profile.Record)
	afterDelete      func(int64)
}

func (s *coordinatorFaultStore) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	s.mu.Lock()
	s.saveCalls++
	s.mu.Unlock()

	record, err := s.Store.Save(ctx, input, makeDefault)
	if err == nil && s.afterSave != nil {
		s.afterSave(record)
	}
	return record, err
}

func (s *coordinatorFaultStore) LoadSnapshot(
	ctx context.Context,
) ([]profile.Record, int64, error) {
	s.mu.Lock()
	failure := s.loadFailure
	if s.observeLoads {
		deadline, hasDeadline := ctx.Deadline()
		s.loadObservations = append(
			s.loadObservations,
			coordinatorLoadContextObservation{
				err:         ctx.Err(),
				value:       ctx.Value(s.loadContextKey),
				timeout:     time.Until(deadline),
				hasDeadline: hasDeadline,
			},
		)
	}
	s.mu.Unlock()

	if failure != nil {
		return nil, 0, failure
	}
	return s.Store.LoadSnapshot(ctx)
}

func (s *coordinatorFaultStore) Delete(
	ctx context.Context,
	id int64,
	replacementDefaultID int64,
) error {
	if err := s.Store.Delete(ctx, id, replacementDefaultID); err != nil {
		return err
	}
	if s.afterDelete != nil {
		s.afterDelete(id)
	}
	return nil
}

func (s *coordinatorFaultStore) setLoadFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadFailure = err
}

func (s *coordinatorFaultStore) observeLoadContexts(key any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadContextKey = key
	s.observeLoads = true
	s.loadObservations = nil
}

func (s *coordinatorFaultStore) stopObservingLoadContexts() []coordinatorLoadContextObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeLoads = false
	return append([]coordinatorLoadContextObservation(nil), s.loadObservations...)
}

func (s *coordinatorFaultStore) saveCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveCalls
}

func assertCoordinatorRoute(
	t *testing.T,
	registry *Registry,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, path, nil),
	)
	if response.Code != wantStatus {
		t.Fatalf("%s: runtime status=%d, want %d", path, response.Code, wantStatus)
	}
	if wantBody != "" && response.Body.String() != wantBody {
		t.Fatalf("%s: runtime body=%q, want %q", path, response.Body.String(), wantBody)
	}
}

func upstreamBuilder(record profile.Record) (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, record.Config.Upstream)
	}), nil
}
