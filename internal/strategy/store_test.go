package strategy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestStoreBootstrapsActiveStrategyAndGeneratesDateSequence(t *testing.T) {
	store, record := newStrategyTestStore(t)
	snapshot, err := store.Bootstrap(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.Active.State != StateActive ||
		snapshot.Active.Config.Name != "20260802-001" || snapshot.Canary != nil {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if snapshot.Active.Rating.Grade != "C" ||
		snapshot.Active.Rating.QualityFloorBPS != 9200 ||
		snapshot.Active.Rating.SevereErrorCeilingBPS != 50 ||
		snapshot.Active.Rating.PassingRoutes != 1 {
		t.Fatalf("rating=%+v", snapshot.Active.Rating)
	}

	again, err := store.Bootstrap(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if again.Active.ID != snapshot.Active.ID || again.Revision != 1 {
		t.Fatalf("second bootstrap=%+v", again)
	}
	name, err := store.NextName(context.Background(), record.ID, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	if err != nil || name != "20260802-002" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestRatingIsDisplayOnlyAndConservativeAcrossRoutes(t *testing.T) {
	config := strategyTestConfig().AutoRouting.Strategy
	config.Routes[0].Candidates = []profile.RouteCandidateConfig{
		{Model: "fast", QualityScoreBPS: 9900, SevereErrorRateBPS: 50},
	}
	rating := rate(config)
	if rating.Grade != "A" || rating.DisplayScoreBPS != 9907 {
		t.Fatalf("A rating=%+v", rating)
	}

	config.Routes = append(config.Routes, profile.RouteConfig{
		ID: "unproven", MinQualityBPS: 9500, MaxSevereErrorRateBPS: 100,
		Candidates: []profile.RouteCandidateConfig{{
			Model: "fast", QualityScoreBPS: 9400, SevereErrorRateBPS: 10,
		}},
	})
	rating = rate(config)
	if rating.Grade != "D" || rating.DisplayScoreBPS != 0 ||
		rating.PassingRoutes != 1 || rating.TotalRoutes != 2 {
		t.Fatalf("conservative rating=%+v", rating)
	}
}

func TestStoreKeepsPublishedVersionsImmutableAndUsesCASLifecycle(t *testing.T) {
	store, record := newStrategyTestStore(t)
	initial, err := store.Bootstrap(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}

	candidateConfig := record.Config.AutoRouting.Strategy
	candidateConfig.Name = "20260802-002"
	candidateConfig.Alias = "候选"
	candidateConfig.Routes[0].Candidates[0].QualityScoreBPS = 9400
	candidate, err := store.CreateDraft(context.Background(), record, candidateConfig)
	if err != nil {
		t.Fatal(err)
	}
	candidateConfig.Alias = "候选-修订"
	candidate, err = store.UpdateDraft(context.Background(), record, candidate.ID, candidateConfig)
	if err != nil || candidate.Config.Alias != "候选-修订" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	renamed := candidateConfig
	renamed.Name = "20260802-003"
	if _, err := store.UpdateDraft(context.Background(), record, candidate.ID, renamed); !errors.Is(err, ErrImmutable) {
		t.Fatalf("rename error=%v, want ErrImmutable", err)
	}
	if candidate, err = store.Advance(
		context.Background(), record.ID, candidate.ID, StateDraft, StateEvaluating,
	); err != nil || candidate.State != StateEvaluating {
		t.Fatalf("evaluating=%+v err=%v", candidate, err)
	}
	if _, err := store.UpdateDraft(context.Background(), record, candidate.ID, candidateConfig); !errors.Is(err, ErrImmutable) {
		t.Fatalf("UpdateDraft() error=%v, want ErrImmutable", err)
	}
	if candidate, err = store.Advance(
		context.Background(), record.ID, candidate.ID, StateEvaluating, StateReady,
	); err != nil || candidate.State != StateReady {
		t.Fatalf("ready=%+v err=%v", candidate, err)
	}

	canary, err := store.StartCanary(context.Background(), record.ID, candidate.ID, 2500, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if canary.Revision != 2 || canary.Canary == nil || canary.Canary.ID != candidate.ID || canary.CanaryBPS != 2500 {
		t.Fatalf("canary=%+v", canary)
	}
	if _, err := store.Promote(context.Background(), record.ID, initial.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Promote() error=%v, want ErrConflict", err)
	}

	active, err := store.Promote(context.Background(), record.ID, canary.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if active.Revision != 3 || active.Active.ID != candidate.ID ||
		active.LastKnownGood == nil || active.LastKnownGood.ID != initial.Active.ID || active.Canary != nil {
		t.Fatalf("active=%+v", active)
	}

	rolledBack, err := store.Rollback(context.Background(), record.ID, active.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Revision != 4 || rolledBack.Active.ID != initial.Active.ID ||
		rolledBack.LastKnownGood == nil || rolledBack.LastKnownGood.ID != candidate.ID {
		t.Fatalf("rolled back=%+v", rolledBack)
	}
}

func TestStoreRejectsStrategyThatCannotResolveInItsProfile(t *testing.T) {
	store, record := newStrategyTestStore(t)
	if _, err := store.Bootstrap(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	invalid := record.Config.AutoRouting.Strategy
	invalid.Name = "20260802-002"
	invalid.Routes[0].Candidates[0].Model = "outside-profile"
	if _, err := store.CreateDraft(context.Background(), record, invalid); !errors.Is(err, profile.ErrInvalidConfig) {
		t.Fatalf("CreateDraft() error=%v, want profile.ErrInvalidConfig", err)
	}
}

func TestStoreResolvesActiveVersionAndCanCancelCanary(t *testing.T) {
	store, record := newStrategyTestStore(t)
	initial, err := store.Bootstrap(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	candidateConfig := record.Config.AutoRouting.Strategy
	candidateConfig.Name = "20260802-002"
	candidate, err := store.CreateDraft(context.Background(), record, candidateConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Advance(context.Background(), record.ID, candidate.ID, StateDraft, StateEvaluating); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Advance(context.Background(), record.ID, candidate.ID, StateEvaluating, StateReady); err != nil {
		t.Fatal(err)
	}
	canary, err := store.StartCanary(context.Background(), record.ID, candidate.ID, 1000, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := store.CancelCanary(context.Background(), record.ID, canary.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Revision != 3 || canceled.Canary != nil || canceled.CanaryBPS != 0 {
		t.Fatalf("canceled=%+v", canceled)
	}
	versions, err := store.List(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].ID != candidate.ID || versions[0].State != StateReady {
		t.Fatalf("versions=%+v", versions)
	}

	record.Config.AutoRouting.Strategy.Name = "stale-profile-copy"
	resolved, snapshot, err := store.ResolveRecord(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.AutoRouting.Strategy.Name != initial.Active.Config.Name ||
		snapshot.Active.ID != initial.Active.ID {
		t.Fatalf("resolved=%+v snapshot=%+v", resolved.Config.AutoRouting.Strategy, snapshot)
	}
}

func newStrategyTestStore(t *testing.T) (*Store, profile.Record) {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := strategyTestConfig()
	record, err := profile.NewStore(db).Save(context.Background(), profile.SaveInput{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: config,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	}), record
}

func strategyTestConfig() profile.Config {
	no := false
	yes := true
	contextWindow := 200_000
	maxOutput := 16_000
	fastInput := int64(100_000)
	fastOutput := int64(400_000)
	strongInput := int64(3_000_000)
	strongOutput := int64(15_000_000)
	config := profile.NewConfig(profile.ProtocolAnthropic, "https://example.test")
	config.Models = []profile.ModelCapabilityConfig{
		{
			ID: "fast", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &no, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &fastInput, OutputPriceMicroUSDPerMillion: &fastOutput,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &yes, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &strongInput, OutputPriceMicroUSDPerMillion: &strongOutput,
		},
	}
	config.AutoRouting = profile.AutoRoutingConfig{
		Enabled: true, Participants: []string{"fast", "strong"},
		StrongBaselineModel: "strong", TaskAnalyzerModel: "fast",
		AnalyzerTimeout: "5s", AnalyzerMinConfidenceBPS: 7000,
		Strategy: profile.RoutingStrategyConfig{
			Name: "20260802-001", Alias: "当前", DefaultRoute: "balanced",
			TaskRoutes: []profile.TaskRouteConfig{{TaskType: "simple", Route: "balanced"}},
			Routes: []profile.RouteConfig{{
				ID: "balanced", MinQualityBPS: 9000, MaxSevereErrorRateBPS: 100,
				Candidates: []profile.RouteCandidateConfig{
					{Model: "fast", QualityScoreBPS: 9200, SevereErrorRateBPS: 50},
					{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
				},
			}},
			Budget: profile.AttemptBudgetConfig{
				MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
				MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
				MaxWorstCaseCostMicroUSD: 500_000,
			},
		},
	}
	return config
}
