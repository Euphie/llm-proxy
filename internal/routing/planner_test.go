package routing

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestPlannerChoosesLowestCostQualifiedCandidate(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, ConfidenceBPS: 9500, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || plan.Route() != "balanced" ||
		plan.VisionMode() != VisionNone || plan.Strategy() != "20260802-001" {
		t.Fatalf("plan=%+v", plan.Snapshot())
	}
	if plan.EstimatedCostMicroUSD() <= 0 || plan.WorstCaseCostMicroUSD() < plan.EstimatedCostMicroUSD() {
		t.Fatalf("costs=%+v", plan.Snapshot())
	}
}

func TestPlannerKeepsSessionModelAndOnlyMovesToEqualOrHigherQuality(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	classification := Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}

	plan, err := planner.PlanWithPreference(request, classification, SessionPreference{
		Model: "strong", MinQualityScoreBPS: 9900,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || plan.Reason() != "Session binding" {
		t.Fatalf("plan=%+v", plan.Snapshot())
	}
	if attempts := plan.ModelAttempts(); len(attempts) != 1 ||
		attempts[0].Model() != "strong" || attempts[0].QualityScoreBPS() != 9900 {
		t.Fatalf("attempts=%+v", attempts)
	}
}

func TestPlannerUpgradesSessionWhenBoundModelCannotSatisfyRequest(t *testing.T) {
	config := routingConfig(false)
	unsupported := false
	config.Models[0].SupportsTools = &unsupported
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit"}]}`)

	plan, err := planner.PlanWithPreference(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}, SessionPreference{Model: "fast", MinQualityScoreBPS: 9200})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || plan.ModelAttempts()[0].QualityScoreBPS() < 9200 {
		t.Fatalf("plan=%+v", plan.Snapshot())
	}
}

func TestPlannerFreezesPrimaryAndStrongFallbackAttempts(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempts := plan.ModelAttempts()
	if len(attempts) != 2 || attempts[0].Model() != "fast" || attempts[1].Model() != "strong" {
		t.Fatalf("attempts=%+v", attempts)
	}
	if plan.WorstCaseCostMicroUSD() < attempts[1].AnswerCallCostMicroUSD()*int64(plan.Budget().MaxAnswerAttempts) {
		t.Fatalf("worst cost=%d does not cover strong fallback attempts", plan.WorstCaseCostMicroUSD())
	}

	attempts[0] = attempts[1]
	if got := plan.ModelAttempts()[0].Model(); got != "fast" {
		t.Fatalf("execution plan followed caller mutation: %q", got)
	}
}

func TestPlannerFreezesOrderedTargetsForEveryModelAttempt(t *testing.T) {
	config := routingConfig(false)
	config.Targets = []profile.TargetConfig{
		{ID: "region_b", Upstream: "https://region-b.example", Models: []string{"fast", "strong"}},
		{ID: "strong_backup", Upstream: "https://strong.example", Models: []string{"strong"}},
	}
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempts := plan.ModelAttempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts=%+v", plan.Snapshot())
	}
	fastTargets := attempts[0].Targets()
	strongTargets := attempts[1].Targets()
	if len(fastTargets) != 2 || fastTargets[0].ID() != profile.PrimaryTargetID ||
		fastTargets[1].ID() != "region_b" || len(strongTargets) != 3 ||
		strongTargets[1].ID() != "region_b" || strongTargets[2].ID() != "strong_backup" {
		t.Fatalf("fast=%+v strong=%+v", fastTargets, strongTargets)
	}
	fastTargets[0] = TargetPlan{}
	if got := plan.ModelAttempts()[0].Targets()[0].ID(); got != profile.PrimaryTargetID {
		t.Fatalf("plan followed caller mutation: %q", got)
	}
	snapshot := plan.Snapshot()
	if len(snapshot.ModelAttempts[0].TargetIDs) != 2 || snapshot.ModelAttempts[0].TargetIDs[1] != "region_b" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestPlannerHighRiskAlwaysUsesStrongBaseline(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"delete production data"}]}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskHigh, ConfidenceBPS: 9000, Source: ClassificationSourceAnalyzer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || !plan.UsesStrongBaseline() {
		t.Fatalf("plan=%+v", plan.Snapshot())
	}
	if attempts := plan.ModelAttempts(); len(attempts) != 1 || attempts[0].Model() != "strong" {
		t.Fatalf("high-risk attempts=%+v", attempts)
	}
}

func TestPlannerBuildsCostSavingEvaluationPairAroundStrongBaseline(t *testing.T) {
	planner, err := NewPlanner(routingRuntime(t, false))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	classification := Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}

	for _, selected := range []string{"fast", "strong"} {
		pair, ok := planner.EvaluationPair(request, classification, selected)
		if !ok {
			t.Fatalf("EvaluationPair(selected=%q) was unavailable", selected)
		}
		if pair.Candidate.Model() != "fast" || pair.Reference.Model() != "strong" {
			t.Fatalf("pair=%+v", pair.Snapshot())
		}
		if pair.Candidate.AnswerCallCostMicroUSD() >= pair.Reference.AnswerCallCostMicroUSD() {
			t.Fatalf("pair does not reduce cost: %+v", pair.Snapshot())
		}
	}
}

func TestPlannerDoesNotEvaluateHighRiskOrNonSavingPairs(t *testing.T) {
	config := routingConfig(false)
	expensiveInput := int64(30_000_000)
	expensiveOutput := int64(60_000_000)
	config.Models[0].InputPriceMicroUSDPerMillion = &expensiveInput
	config.Models[0].OutputPriceMicroUSDPerMillion = &expensiveOutput
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)

	if _, ok := planner.EvaluationPair(request, Classification{
		TaskType: "simple", Risk: RiskHigh, Source: ClassificationSourceRule,
	}, "strong"); ok {
		t.Fatal("high-risk request received an evaluation pair")
	}
	if _, ok := planner.EvaluationPair(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}, "strong"); ok {
		t.Fatal("non-saving candidate received an evaluation pair")
	}
}

func TestPlannerFiltersCapabilitiesAndQuality(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*profile.Config)
		body   string
		model  string
	}{
		{
			name: "tool capability",
			mutate: func(config *profile.Config) {
				unsupported := false
				config.Models[0].SupportsTools = &unsupported
			},
			body:  `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit a file"}]}`,
			model: "strong",
		},
		{
			name: "structured output capability",
			mutate: func(config *profile.Config) {
				config.Models[0].SupportsStructuredOutput = nil
			},
			body:  `{"model":"auto","max_tokens":1000,"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":"return json"}]}`,
			model: "strong",
		},
		{
			name: "quality gate",
			mutate: func(config *profile.Config) {
				config.AutoRouting.Strategy.Routes[0].Candidates[0].QualityScoreBPS = 8999
			},
			body:  `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`,
			model: "strong",
		},
		{
			name: "context window",
			mutate: func(config *profile.Config) {
				small := 100
				output := 50
				config.Models[0].ContextWindow = &small
				config.Models[0].MaxOutputTokens = &output
			},
			body:  `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`,
			model: "strong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := routingConfig(false)
			tt.mutate(&config)
			runtime := resolveRoutingRuntime(t, config)
			planner, err := NewPlanner(runtime)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planner.Plan(autoAnthropicRequest(t, tt.body), Classification{
				TaskType: "simple", Risk: RiskNormal, ConfidenceBPS: 9000, Source: ClassificationSourceRule,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Model() != tt.model {
				t.Fatalf("model=%q, want %q; plan=%+v", plan.Model(), tt.model, plan.Snapshot())
			}
		})
	}
}

func TestPlannerSelectsVisionModeAfterModel(t *testing.T) {
	tests := []struct {
		name       string
		vision     bool
		fastNative bool
		wantModel  string
		wantMode   VisionMode
	}{
		{name: "native", vision: true, fastNative: true, wantModel: "fast", wantMode: VisionNative},
		{name: "composite", vision: true, fastNative: false, wantModel: "fast", wantMode: VisionComposite},
		{name: "unsupported candidate excluded", vision: false, fastNative: false, wantModel: "strong", wantMode: VisionNative},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := routingConfig(tt.vision)
			config.Models[0].SupportsVision = &tt.fastNative
			runtime := resolveRoutingRuntime(t, config)
			planner, err := NewPlanner(runtime)
			if err != nil {
				t.Fatal(err)
			}
			request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"what is this"}]}]}`)
			plan, err := planner.Plan(request, Classification{
				TaskType: "simple", Risk: RiskNormal, ConfidenceBPS: 9000, Source: ClassificationSourceRule,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Model() != tt.wantModel || plan.VisionMode() != tt.wantMode {
				t.Fatalf("plan=%+v, want model=%q mode=%q", plan.Snapshot(), tt.wantModel, tt.wantMode)
			}
		})
	}
}

func TestPlannerFailsWhenStrongBaselineCannotSatisfyHardConstraints(t *testing.T) {
	config := routingConfig(false)
	unsupported := false
	config.Models[1].SupportsTools = &unsupported
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit"}]}`)

	_, err = planner.Plan(request, Classification{TaskType: "high_risk", Risk: RiskHigh})
	if !errors.Is(err, ErrNoCapableModel) {
		t.Fatalf("Plan() error=%v, want ErrNoCapableModel", err)
	}
}

func TestPlannerSnapshotDoesNotFollowRuntimeMutation(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	fast := runtime.Models["fast"]
	fast.InputPriceMicroUSDPerMillion = 99_000_000
	runtime.Models["fast"] = fast
	runtime.AutoRouting.Strategy.Routes["balanced"] = profile.RouteRuntime{
		ID: "balanced", MinQualityBPS: 10_000, MaxSevereErrorRateBPS: 0,
		Candidates: []profile.RouteCandidateRuntime{{Model: "strong", QualityScoreBPS: 10_000}},
	}

	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" {
		t.Fatalf("plan followed mutated runtime: %+v", plan.Snapshot())
	}
}

func autoAnthropicRequest(t *testing.T, body string) Request {
	t.Helper()
	request, err := ParseAutoRequest(
		profile.ProtocolAnthropic,
		http.MethodPost,
		"/v1/messages",
		"application/json",
		[]byte(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func routingRuntime(t *testing.T, vision bool) profile.Runtime {
	t.Helper()
	return resolveRoutingRuntime(t, routingConfig(vision))
}

func resolveRoutingRuntime(t *testing.T, config profile.Config) profile.Runtime {
	t.Helper()
	runtime, err := (profile.Record{
		Slug: "auto", DisplayName: "Auto", Enabled: true, Config: config,
	}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func routingConfig(vision bool) profile.Config {
	no := false
	yes := true
	contextWindow := 200_000
	maxOutput := 16_000
	fastInput := int64(100_000)
	fastOutput := int64(400_000)
	strongInput := int64(3_000_000)
	strongOutput := int64(15_000_000)
	visionInput := int64(200_000)
	visionOutput := int64(800_000)

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
	if vision {
		config.Vision.Enabled = true
		config.Vision.Model = "vision"
		config.Models = append(config.Models, profile.ModelCapabilityConfig{
			ID: "vision", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision:               &yes,
			InputPriceMicroUSDPerMillion: &visionInput, OutputPriceMicroUSDPerMillion: &visionOutput,
		})
	}
	config.AutoRouting = profile.AutoRoutingConfig{
		Enabled: true, Participants: []string{"fast", "strong"},
		StrongBaselineModel: "strong", TaskAnalyzerModel: "fast",
		AnalyzerTimeout: "5s", AnalyzerMinConfidenceBPS: 7000,
		Strategy: profile.RoutingStrategyConfig{
			Name: "20260802-001", Alias: "均衡策略", DefaultRoute: "balanced",
			TaskRoutes: []profile.TaskRouteConfig{
				{TaskType: "simple", Route: "balanced"},
				{TaskType: "high_risk", Route: "strong"},
			},
			Routes: []profile.RouteConfig{
				{
					ID: "balanced", MinQualityBPS: 9000, MaxSevereErrorRateBPS: 100,
					Candidates: []profile.RouteCandidateConfig{
						{Model: "fast", QualityScoreBPS: 9200, SevereErrorRateBPS: 50},
						{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
					},
				},
				{
					ID: "strong", MinQualityBPS: 9800, MaxSevereErrorRateBPS: 50,
					Candidates: []profile.RouteCandidateConfig{
						{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
					},
				},
			},
			Budget: profile.AttemptBudgetConfig{
				MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
				MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
				MaxWorstCaseCostMicroUSD: 500_000,
			},
		},
	}
	return config
}
