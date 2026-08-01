package profile

import (
	"errors"
	"testing"
	"time"
)

func TestResolveAutoRoutingContract(t *testing.T) {
	record := validAutoRoutingRecord()

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	if !runtime.AutoRouting.Enabled {
		t.Fatal("auto routing is disabled")
	}
	if runtime.AutoRouting.StrongBaselineModel != "strong" ||
		runtime.AutoRouting.TaskAnalyzerModel != "fast" {
		t.Fatalf("roles=%+v", runtime.AutoRouting)
	}
	if runtime.AutoRouting.AnalyzerTimeout != 5*time.Second ||
		runtime.AutoRouting.AnalyzerMinConfidenceBPS != 7000 ||
		runtime.AutoRouting.SessionTTL != 24*time.Hour {
		t.Fatalf("analyzer settings=%+v", runtime.AutoRouting)
	}
	if runtime.AutoRouting.Strategy.Name != "20260802-001" ||
		runtime.AutoRouting.Strategy.DefaultRoute != "balanced" {
		t.Fatalf("strategy=%+v", runtime.AutoRouting.Strategy)
	}
	if got := runtime.Models["fast"].InputPriceMicroUSDPerMillion; got != 100_000 {
		t.Fatalf("input price=%d", got)
	}
	if !runtime.Models["strong"].HasSupportsTools ||
		!runtime.Models["strong"].SupportsTools {
		t.Fatalf("tool capability=%+v", runtime.Models["strong"])
	}
}

func TestResolveAutoRoutingAcceptsZeroPrices(t *testing.T) {
	record := validAutoRoutingRecord()
	zero := int64(0)
	record.Config.Models[0].InputPriceMicroUSDPerMillion = &zero
	record.Config.Models[0].OutputPriceMicroUSDPerMillion = &zero

	if _, err := record.Resolve(); err != nil {
		t.Fatalf("Resolve() error=%v, want zero prices accepted", err)
	}
}

func TestResolveAutoRoutingDisabledDoesNotRequireRolesOrPrices(t *testing.T) {
	supportsVision := false
	record := Record{
		Slug: "explicit", DisplayName: "Explicit", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	record.Config.Models = []ModelCapabilityConfig{{
		ID: "explicit-model", SupportsVision: &supportsVision,
	}}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.Enabled {
		t.Fatal("auto routing unexpectedly enabled")
	}
}

func TestResolveAutoRoutingRejectsIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{
			name: "no participants",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Participants = nil
			},
		},
		{
			name: "baseline not participant",
			mutate: func(r *Record) {
				r.Config.AutoRouting.StrongBaselineModel = "other"
			},
		},
		{
			name: "analyzer not cataloged",
			mutate: func(r *Record) {
				r.Config.AutoRouting.TaskAnalyzerModel = "other"
			},
		},
		{
			name: "participant missing input price",
			mutate: func(r *Record) {
				r.Config.Models[0].InputPriceMicroUSDPerMillion = nil
			},
		},
		{
			name: "participant missing output price",
			mutate: func(r *Record) {
				r.Config.Models[0].OutputPriceMicroUSDPerMillion = nil
			},
		},
		{
			name: "vision model missing price",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.Model = "vision"
				supportsVision := true
				r.Config.Models = append(r.Config.Models, ModelCapabilityConfig{
					ID: "vision", SupportsVision: &supportsVision,
				})
			},
		},
		{
			name: "invalid strategy name",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.Name = "latest"
			},
		},
		{
			name: "default route missing",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.DefaultRoute = "missing"
			},
		},
		{
			name: "candidate not participant",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.Routes[0].Candidates[0].Model = "other"
			},
		},
		{
			name: "task route missing",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.TaskRoutes[0].Route = "missing"
			},
		},
		{
			name: "invalid analyzer confidence",
			mutate: func(r *Record) {
				r.Config.AutoRouting.AnalyzerMinConfidenceBPS = 10_001
			},
		},
		{
			name: "invalid Session TTL",
			mutate: func(r *Record) {
				r.Config.AutoRouting.SessionTTL = "1s"
			},
		},
		{
			name: "invalid deadline",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.Budget.Deadline = "0s"
			},
		},
		{
			name: "total calls below required calls",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validAutoRoutingRecord()
			tt.mutate(&record)
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
			}
		})
	}
}

func validAutoRoutingRecord() Record {
	supportsVision := false
	supportsTools := true
	supportsStructuredOutput := true
	contextWindow := 200_000
	maxOutputTokens := 16_000
	fastInputPrice := int64(100_000)
	fastOutputPrice := int64(400_000)
	strongInputPrice := int64(3_000_000)
	strongOutputPrice := int64(15_000_000)

	config := NewConfig(ProtocolAnthropic, "https://example.test")
	config.Models = []ModelCapabilityConfig{
		{
			ID: "fast", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens,
			SupportsVision: &supportsVision, SupportsTools: &supportsTools,
			SupportsStructuredOutput:      &supportsStructuredOutput,
			InputPriceMicroUSDPerMillion:  &fastInputPrice,
			OutputPriceMicroUSDPerMillion: &fastOutputPrice,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens,
			SupportsVision: &supportsVision, SupportsTools: &supportsTools,
			SupportsStructuredOutput:      &supportsStructuredOutput,
			InputPriceMicroUSDPerMillion:  &strongInputPrice,
			OutputPriceMicroUSDPerMillion: &strongOutputPrice,
		},
	}
	config.AutoRouting = AutoRoutingConfig{
		Enabled:                  true,
		Participants:             []string{"fast", "strong"},
		StrongBaselineModel:      "strong",
		TaskAnalyzerModel:        "fast",
		AnalyzerTimeout:          "5s",
		AnalyzerMinConfidenceBPS: 7000,
		Strategy: RoutingStrategyConfig{
			Name:         "20260802-001",
			Alias:        "均衡策略",
			DefaultRoute: "balanced",
			TaskRoutes: []TaskRouteConfig{
				{TaskType: "simple", Route: "balanced"},
				{TaskType: "high_risk", Route: "strong"},
			},
			Routes: []RouteConfig{
				{
					ID: "balanced", MinQualityBPS: 9000, MaxSevereErrorRateBPS: 100,
					Candidates: []RouteCandidateConfig{
						{Model: "fast", QualityScoreBPS: 9200, SevereErrorRateBPS: 50},
						{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
					},
				},
				{
					ID: "strong", MinQualityBPS: 9800, MaxSevereErrorRateBPS: 50,
					Candidates: []RouteCandidateConfig{
						{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
					},
				},
			},
			Budget: AttemptBudgetConfig{
				MaxAnswerAttempts:        2,
				MaxAuxiliaryCalls:        2,
				MaxTotalOutboundCalls:    5,
				MaxRetriesPerTarget:      1,
				MaxTargetSwitches:        0,
				MaxModelSwitches:         1,
				Deadline:                 "2m",
				MaxWorstCaseCostMicroUSD: 500_000,
			},
		},
	}

	return Record{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: config,
	}
}
