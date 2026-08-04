package profile

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResolveRiskPolicyDefaultsAndExplicitEmptyLists(t *testing.T) {
	record := validAutoRoutingRecord()
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtime.AutoRouting.RiskPolicy.SensitiveTextPatterns, defaultSensitiveTextPatterns) ||
		!reflect.DeepEqual(runtime.AutoRouting.RiskPolicy.SensitiveToolPatterns, defaultSensitiveToolPatterns) ||
		runtime.AutoRouting.RiskPolicy.LongContextThresholdBPS != 7500 {
		t.Fatalf("default risk policy=%+v", runtime.AutoRouting.RiskPolicy)
	}

	record.Config.AutoRouting.RiskPolicy = RiskPolicyConfig{
		Version:                 RiskPolicyVersion2,
		SensitiveTextPatterns:   []string{},
		SensitiveToolPatterns:   []string{},
		LongContextThresholdBPS: 6200,
	}
	runtime, err = record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.RiskPolicy.SensitiveTextPatterns == nil ||
		runtime.AutoRouting.RiskPolicy.SensitiveToolPatterns == nil ||
		len(runtime.AutoRouting.RiskPolicy.SensitiveTextPatterns) != 0 ||
		len(runtime.AutoRouting.RiskPolicy.SensitiveToolPatterns) != 0 ||
		runtime.AutoRouting.RiskPolicy.LongContextThresholdBPS != 6200 {
		t.Fatalf("explicit empty risk policy=%+v", runtime.AutoRouting.RiskPolicy)
	}
}

func TestRiskPolicyV2DefaultsIgnoreUnversionedLegacyRules(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.RiskPolicy = RiskPolicyConfig{
		SensitiveTextPatterns: []string{"edit the file", "shell"},
		SensitiveToolPatterns: []string{"exec", "apply_patch"},
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.RiskPolicy.Version != 2 {
		t.Fatalf("risk version=%d", runtime.AutoRouting.RiskPolicy.Version)
	}
	for _, pattern := range append(
		append([]string(nil), runtime.AutoRouting.RiskPolicy.SensitiveTextPatterns...),
		runtime.AutoRouting.RiskPolicy.SensitiveToolPatterns...,
	) {
		if pattern == "edit the file" || pattern == "shell" || pattern == "exec" || pattern == "apply_patch" {
			t.Fatalf("legacy broad pattern remained active: %q", pattern)
		}
	}
}

func TestRiskPolicyConfigJSONPreservesExplicitEmptyListsAndOrder(t *testing.T) {
	want := RiskPolicyConfig{
		Version:                  RiskPolicyVersion2,
		SensitiveTextPatterns:    []string{"second", "first"},
		SensitiveToolPatterns:    []string{},
		StructuredOutputHighRisk: true,
		LongContextThresholdBPS:  8125,
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got RiskPolicyConfig
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.SensitiveToolPatterns == nil {
		t.Fatalf("round trip=%+v JSON=%s", got, body)
	}
}

func TestResolveRiskPolicyClonesConfiguredSlices(t *testing.T) {
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.RiskPolicy = RiskPolicyConfig{
		Version:               RiskPolicyVersion2,
		SensitiveTextPatterns: []string{"original text"},
		SensitiveToolPatterns: []string{"original_tool"},
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	record.Config.AutoRouting.RiskPolicy.SensitiveTextPatterns[0] = "mutated text"
	record.Config.AutoRouting.RiskPolicy.SensitiveToolPatterns[0] = "mutated_tool"
	if runtime.AutoRouting.RiskPolicy.SensitiveTextPatterns[0] != "original text" ||
		runtime.AutoRouting.RiskPolicy.SensitiveToolPatterns[0] != "original_tool" {
		t.Fatalf("runtime followed config mutation: %+v", runtime.AutoRouting.RiskPolicy)
	}
}

func TestResolveRiskPolicyRejectsActionableInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		policy RiskPolicyConfig
		want   string
	}{
		{name: "blank text pattern", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, SensitiveTextPatterns: []string{" "}}, want: "sensitive text pattern 1"},
		{name: "untrimmed tool pattern", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, SensitiveToolPatterns: []string{" exec"}}, want: "sensitive tool pattern 1"},
		{name: "duplicate ignoring case", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, SensitiveTextPatterns: []string{"Deploy", "deploy"}}, want: "duplicate"},
		{name: "too many patterns", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, SensitiveToolPatterns: make([]string, maxRiskPatterns+1)}, want: "at most"},
		{name: "pattern too long", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, SensitiveTextPatterns: []string{strings.Repeat("x", maxRiskPatternRunes+1)}}, want: "at most"},
		{name: "threshold above maximum", policy: RiskPolicyConfig{Version: RiskPolicyVersion2, LongContextThresholdBPS: 10001}, want: "between 1 and 10000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validAutoRoutingRecord()
			record.Config.AutoRouting.RiskPolicy = tt.policy
			_, err := record.Resolve()
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig containing %q", err, tt.want)
			}
		})
	}
}

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

	_, err := record.Resolve()
	if !errors.Is(err, ErrInvalidConfig) ||
		!strings.Contains(err.Error(), `task analyzer model "fast" must confirm tool support`) {
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
	if !got.Enabled || got.SampleRateBPS != 1250 || got.DailyBudgetMicroUSD != 250_000 ||
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
