package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
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
	coordinator := gateway.NewCoordinator(profiles, registry, builder)
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

func TestStrategyServiceKeepsPublishedRuntimeWhenCanaryBuildFails(t *testing.T) {
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
	coordinator := gateway.NewCoordinator(profiles, registry, builder)
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
	failCanary = true
	snapshot, err := service.StartCanary(
		context.Background(), record.ID, candidate.ID, 1000, overview.Snapshot.Revision,
	)
	if !errors.Is(err, gateway.ErrRuntimeSync) || snapshot.Canary == nil {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	assertStrategyRuntime(t, gateway.NewRouter(registry), "20260802-001")
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
