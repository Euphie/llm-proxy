package profile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLegacyTargetSwitchBudgetIsDiscarded(t *testing.T) {
	var budget AttemptBudgetConfig
	if err := json.Unmarshal([]byte(`{
		"max_answer_attempts":2,
		"max_auxiliary_calls":2,
		"max_total_outbound_calls":5,
		"max_retries_per_target":1,
		"max_target_switches":3,
		"max_model_switches":1,
		"deadline":"2m",
		"max_worst_case_cost_micro_usd":500000
	}`), &budget); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(contents, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, exists := persisted["max_target_switches"]; exists {
		t.Fatalf("removed target-switch budget survived round trip: %s", contents)
	}
}

func TestResolveAutoRoutingSessionLockTokenThreshold(t *testing.T) {
	record := validAutoRoutingRecord()
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.SessionLockTokenThreshold != 100_000 {
		t.Fatalf("default threshold=%d", runtime.AutoRouting.SessionLockTokenThreshold)
	}

	record.Config.AutoRouting.SessionLockTokenThreshold = 250_000
	runtime, err = record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.SessionLockTokenThreshold != 250_000 {
		t.Fatalf("configured threshold=%d", runtime.AutoRouting.SessionLockTokenThreshold)
	}
}

func TestResolveAutoRoutingRejectsNegativeSessionLockTokenThreshold(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.SessionLockTokenThreshold = -1

	_, err := record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "Session lock token threshold") {
		t.Fatalf("Resolve() error=%v", err)
	}
}

func TestResolveAutoRoutingPreservesCandidateProductionEligibility(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.Strategy.Routes[0].Candidates[0].ProductionEligible = true

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.AutoRouting.Strategy.Routes["balanced"].Candidates[0].ProductionEligible {
		t.Fatal("candidate production eligibility was lost")
	}
}

func TestResolveAutoRoutingContract(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.Strategy.MinNetSavingsBPS = 1_000
	record.Config.AutoRouting.Strategy.LatencyTargetMS = 600

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
		runtime.AutoRouting.Strategy.DefaultRoute != "balanced" ||
		runtime.AutoRouting.Strategy.MinNetSavingsBPS != 1_000 ||
		runtime.AutoRouting.Strategy.LatencyTargetMS != 600 {
		t.Fatalf("strategy=%+v", runtime.AutoRouting.Strategy)
	}
	if got := runtime.Models["fast"].InputPriceMicroUSDPerMillion; got != 100_000 {
		t.Fatalf("input price=%d", got)
	}
	if !runtime.Models["strong"].HasSupportsTools ||
		!runtime.Models["strong"].SupportsTools ||
		!runtime.Models["strong"].HasSupportsAgentWorkflow ||
		!runtime.Models["strong"].SupportsAgentWorkflow {
		t.Fatalf("tool capability=%+v", runtime.Models["strong"])
	}
}

func TestResolveAutoRoutingRejectsInvalidStrategyGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RoutingStrategyConfig)
	}{
		{name: "negative savings", mutate: func(strategy *RoutingStrategyConfig) { strategy.MinNetSavingsBPS = -1 }},
		{name: "savings above maximum", mutate: func(strategy *RoutingStrategyConfig) { strategy.MinNetSavingsBPS = 10_001 }},
		{name: "negative latency", mutate: func(strategy *RoutingStrategyConfig) { strategy.LatencyTargetMS = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validAutoRoutingRecord()
			tt.mutate(&record.Config.AutoRouting.Strategy)
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestResolveSelfEscalationRequiresAReservedModelSwitch(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.SelfEscalation.Enabled = true
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.AutoRouting.SelfEscalation.Enabled {
		t.Fatalf("self escalation=%+v", runtime.AutoRouting.SelfEscalation)
	}

	record.Config.AutoRouting.Strategy.Budget.MaxModelSwitches = 0
	_, err = record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "self escalation requires") {
		t.Fatalf("Resolve() error=%v", err)
	}
}

func TestResolveSelfEscalationRequiresToolSupportForNonBaselineCandidates(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.SelfEscalation.Enabled = true
	record.Config.AutoRouting.TaskAnalyzerModel = "strong"
	unsupported := false
	record.Config.Models[0].SupportsTools = &unsupported
	record.Config.Models[0].SupportsAgentWorkflow = &unsupported

	_, err := record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) ||
		!strings.Contains(err.Error(), `candidate model "fast" must confirm tool support`) {
		t.Fatalf("Resolve() error=%v", err)
	}

	record.Config.AutoRouting.Strategy.Routes[0].Candidates =
		record.Config.AutoRouting.Strategy.Routes[0].Candidates[1:]
	if _, err := record.Resolve(); err != nil {
		t.Fatalf("unused participant should not block self escalation: %v", err)
	}
}

func TestResolveAutoRoutingRequiresTaskAnalyzerToolSupport(t *testing.T) {
	record := validAutoRoutingRecord()
	unsupported := false
	record.Config.Models[0].SupportsTools = &unsupported
	record.Config.Models[0].SupportsAgentWorkflow = &unsupported

	_, err := record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) ||
		!strings.Contains(err.Error(), `task analyzer model "fast" must confirm tool support`) {
		t.Fatalf("Resolve() error=%v", err)
	}
}

func TestResolveRejectsAgentWorkflowWithoutToolSupport(t *testing.T) {
	record := validAutoRoutingRecord()
	unsupported := false
	record.Config.Models[0].SupportsTools = &unsupported

	_, err := record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) ||
		!strings.Contains(err.Error(), "supports_agent_workflow requires supports_tools") {
		t.Fatalf("Resolve() error=%v", err)
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

func TestResolveDynamicOptimizationContract(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.DynamicOptimization = DynamicOptimizationConfig{
		Enabled:             true,
		AutoUpdatePolicy:    true,
		SampleRateBPS:       1250,
		DailyBudgetMicroUSD: 250_000,
		ReviewerModel:       "strong",
		MaxConcurrency:      3,
		QueueCapacity:       128,
		TaskTimeout:         "90s",
	}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	got := runtime.AutoRouting.DynamicOptimization
	if !got.Enabled || !got.AutoUpdatePolicy || got.SampleRateBPS != 1250 || got.DailyBudgetMicroUSD != 250_000 ||
		got.ReviewerModel != "strong" || got.MaxConcurrency != 3 ||
		got.QueueCapacity != 128 || got.TaskTimeout != 90*time.Second {
		t.Fatalf("dynamic optimization=%+v", got)
	}
}

func TestResolveDynamicOptimizationRejectsIncompleteSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DynamicOptimizationConfig, *Record)
	}{
		{
			name: "missing reviewer",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.ReviewerModel = ""
			},
		},
		{
			name: "reviewer outside catalog",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.ReviewerModel = "other"
			},
		},
		{
			name: "reviewer missing price",
			mutate: func(config *DynamicOptimizationConfig, record *Record) {
				config.ReviewerModel = "reviewer"
				supportsVision := false
				record.Config.Models = append(record.Config.Models, ModelCapabilityConfig{
					ID: "reviewer", SupportsVision: &supportsVision,
				})
			},
		},
		{
			name: "reviewer missing context capability",
			mutate: func(config *DynamicOptimizationConfig, record *Record) {
				config.ReviewerModel = "reviewer"
				supportsVision := false
				inputPrice := int64(100_000)
				outputPrice := int64(400_000)
				record.Config.Models = append(record.Config.Models, ModelCapabilityConfig{
					ID: "reviewer", SupportsVision: &supportsVision,
					InputPriceMicroUSDPerMillion:  &inputPrice,
					OutputPriceMicroUSDPerMillion: &outputPrice,
				})
			},
		},
		{
			name: "zero sample rate",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.SampleRateBPS = 0
			},
		},
		{
			name: "sample rate above one hundred percent",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.SampleRateBPS = 10_001
			},
		},
		{
			name: "zero daily budget",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.DailyBudgetMicroUSD = 0
			},
		},
		{
			name: "zero concurrency",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.MaxConcurrency = 0
			},
		},
		{
			name: "oversized queue",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.QueueCapacity = 4097
			},
		},
		{
			name: "timeout above credential lifetime",
			mutate: func(config *DynamicOptimizationConfig, _ *Record) {
				config.TaskTimeout = "11m"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validAutoRoutingRecord()
			config := DynamicOptimizationConfig{
				Enabled:             true,
				SampleRateBPS:       1000,
				DailyBudgetMicroUSD: 100_000,
				ReviewerModel:       "strong",
				MaxConcurrency:      2,
				QueueCapacity:       64,
				TaskTimeout:         "1m",
			}
			tt.mutate(&config, &record)
			record.Config.AutoRouting.DynamicOptimization = config
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestResolveDynamicOptimizationDisabledIgnoresEmptySettings(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.DynamicOptimization = DynamicOptimizationConfig{}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.DynamicOptimization.Enabled {
		t.Fatal("dynamic optimization unexpectedly enabled")
	}
}

func TestResolveDynamicOptimizationRejectsAutomaticUpdatesWithoutEvaluation(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.DynamicOptimization = DynamicOptimizationConfig{
		AutoUpdatePolicy: true,
	}

	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
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
			name: "only one recorded model",
			mutate: func(r *Record) {
				r.Config.Models = r.Config.Models[:1]
				r.Config.AutoRouting.Participants = []string{"fast"}
				r.Config.AutoRouting.StrongBaselineModel = "fast"
			},
		},
		{
			name: "only one participant",
			mutate: func(r *Record) {
				r.Config.AutoRouting.Participants = []string{"strong"}
				r.Config.AutoRouting.TaskAnalyzerModel = "strong"
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

func TestResolveAutoRoutingRequiresExecutableVisionModel(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{
			name: "vision capability is false",
			mutate: func(record *Record) {
				unsupported := false
				record.Config.Models[2].SupportsVision = &unsupported
			},
		},
		{
			name: "context window is missing",
			mutate: func(record *Record) {
				record.Config.Models[2].ContextWindow = nil
			},
		},
		{
			name: "max output is missing",
			mutate: func(record *Record) {
				record.Config.Models[2].MaxOutputTokens = nil
			},
		},
		{
			name: "configured output exceeds model output",
			mutate: func(record *Record) {
				record.Config.Vision.MaxTokens = 16_001
			},
		},
		{
			name: "one image prompt reserve exceeds context",
			mutate: func(record *Record) {
				contextWindow := 6_000
				maxOutput := 2_048
				record.Config.Models[2].ContextWindow = &contextWindow
				record.Config.Models[2].MaxOutputTokens = &maxOutput
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validAutoRoutingRecord()
			yes := true
			contextWindow := 200_000
			maxOutput := 16_000
			inputPrice := int64(200_000)
			outputPrice := int64(800_000)
			record.Config.Vision.Enabled = true
			record.Config.Vision.Model = "vision"
			record.Config.Models = append(record.Config.Models, ModelCapabilityConfig{
				ID: "vision", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
				SupportsVision:               &yes,
				InputPriceMicroUSDPerMillion: &inputPrice, OutputPriceMicroUSDPerMillion: &outputPrice,
			})
			test.mutate(&record)
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
			}
		})
	}
}

func validAutoRoutingRecord() Record {
	supportsVision := false
	supportsTools := true
	supportsAgentWorkflow := true
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
			SupportsAgentWorkflow:         &supportsAgentWorkflow,
			SupportsStructuredOutput:      &supportsStructuredOutput,
			InputPriceMicroUSDPerMillion:  &fastInputPrice,
			OutputPriceMicroUSDPerMillion: &fastOutputPrice,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens,
			SupportsVision: &supportsVision, SupportsTools: &supportsTools,
			SupportsAgentWorkflow:         &supportsAgentWorkflow,
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
				{TaskType: "reasoning", Route: "strong"},
			},
			Routes: []RouteConfig{
				{
					ID: "balanced", MinQualityBPS: 9000, MinStabilityBPS: 8000,
					MaxSevereErrorRateBPS: 100,
					Weights: RoutingWeightsConfig{
						QualityBPS: 4000, StabilityBPS: 2500, CostBPS: 2500, PerformanceBPS: 1000,
					},
					Candidates: []RouteCandidateConfig{
						{Model: "fast", QualityScoreBPS: 9200, StabilityScoreBPS: 9300, SevereErrorRateBPS: 50, ExpectedLatencyMS: 250},
						{Model: "strong", QualityScoreBPS: 9900, StabilityScoreBPS: 9900, SevereErrorRateBPS: 10, ExpectedLatencyMS: 800},
					},
				},
				{
					ID: "strong", MinQualityBPS: 9800, MinStabilityBPS: 9800,
					MaxSevereErrorRateBPS: 50,
					Weights: RoutingWeightsConfig{
						QualityBPS: 4000, StabilityBPS: 2500, CostBPS: 2500, PerformanceBPS: 1000,
					},
					Candidates: []RouteCandidateConfig{
						{Model: "strong", QualityScoreBPS: 9900, StabilityScoreBPS: 9900, SevereErrorRateBPS: 10, ExpectedLatencyMS: 800},
					},
				},
			},
			Budget: AttemptBudgetConfig{
				MaxAnswerAttempts:        2,
				MaxAuxiliaryCalls:        2,
				MaxTotalOutboundCalls:    5,
				MaxRetriesPerTarget:      1,
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

func TestAutoRoutingRequiresCompleteFourDimensionalScoring(t *testing.T) {
	record := validAutoRoutingRecord()
	route := &record.Config.AutoRouting.Strategy.Routes[0]

	route.Weights = RoutingWeightsConfig{
		QualityBPS: 4000, StabilityBPS: 2500, CostBPS: 2500, PerformanceBPS: 999,
	}
	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("weights sum Resolve() error=%v, want ErrInvalidConfig", err)
	}

	record = validAutoRoutingRecord()
	record.Config.AutoRouting.Strategy.Routes[0].Candidates[0].StabilityScoreBPS = 10_001
	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("stability Resolve() error=%v, want ErrInvalidConfig", err)
	}

	record = validAutoRoutingRecord()
	record.Config.AutoRouting.Strategy.Routes[0].Candidates[0].ExpectedLatencyMS = -1
	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("latency Resolve() error=%v, want ErrInvalidConfig", err)
	}

	runtime, err := validAutoRoutingRecord().Resolve()
	if err != nil {
		t.Fatal(err)
	}
	resolved := runtime.AutoRouting.Strategy.Routes["balanced"]
	if resolved.MinStabilityBPS != 8000 || resolved.Weights.QualityBPS != 4000 ||
		resolved.Weights.StabilityBPS != 2500 || resolved.Weights.CostBPS != 2500 ||
		resolved.Weights.PerformanceBPS != 1000 ||
		resolved.Candidates[0].StabilityScoreBPS != 9300 ||
		resolved.Candidates[0].ExpectedLatencyMS != 250 {
		t.Fatalf("resolved scoring=%+v", resolved)
	}
}
