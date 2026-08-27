package strategycompiler

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestGeneratedBudgetDoesNotExposeTargetSwitches(t *testing.T) {
	contents, err := json.Marshal(defaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	var budget map[string]any
	if err := json.Unmarshal(contents, &budget); err != nil {
		t.Fatal(err)
	}
	if _, exists := budget["max_target_switches"]; exists {
		t.Fatalf("generated budget exposed target switches: %s", contents)
	}
}

func TestCompileUsesObjectiveWeightPresets(t *testing.T) {
	want := map[Objective]profile.RoutingWeightsConfig{
		ObjectiveQuality:  {QualityBPS: 5_500, StabilityBPS: 2_500, CostBPS: 1_000, PerformanceBPS: 1_000},
		ObjectiveBalanced: {QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000},
		ObjectiveCost:     {QualityBPS: 3_000, StabilityBPS: 2_000, CostBPS: 4_000, PerformanceBPS: 1_000},
		ObjectiveLatency:  {QualityBPS: 3_000, StabilityBPS: 2_000, CostBPS: 1_000, PerformanceBPS: 4_000},
	}
	for objective, weights := range want {
		t.Run(string(objective), func(t *testing.T) {
			result := compileFixture(t, fixtureRecord(true), Intent{Objective: objective}, nil, fixtureEvaluationCatalog())
			for _, route := range result.Config.Routes {
				if route.Weights != weights {
					t.Fatalf("route %s weights=%+v want=%+v", route.ID, route.Weights, weights)
				}
			}
		})
	}
}

func TestCompileGeneratesExactDifficultyRoutesAndBaselineGuard(t *testing.T) {
	result := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveQuality}, nil, fixtureEvaluationCatalog())
	if result.Config.DefaultRoute != "general" {
		t.Fatalf("default route=%q want general", result.Config.DefaultRoute)
	}
	wantTasks := []string{"simple", "general", "reasoning", "math", "coding", "tool_use", "vision"}
	for _, task := range wantTasks {
		for _, difficulty := range []string{"easy", "medium", "hard"} {
			routeID, ok := configuredRouteFor(result.Config, task, difficulty)
			if !ok {
				t.Fatalf("task route %s/%s missing from %+v", task, difficulty, result.Config.TaskRoutes)
			}
			if difficulty == "hard" && routeID != "strong" {
				t.Fatalf("hard task %s mapped to %q want strong", task, routeID)
			}
			if difficulty != "hard" && routeID == "strong" {
				t.Fatalf("%s task %s unexpectedly mapped to strong", difficulty, task)
			}
		}
	}
	strong := routeForID(t, result.Config, "strong")
	if len(strong.Candidates) != 1 || strong.Candidates[0].Model != "strong" {
		t.Fatalf("strong route candidates=%+v", strong.Candidates)
	}
	for _, route := range result.Config.Routes {
		if route.MinQualityBPS != 8_000 || route.MinStabilityBPS != 8_000 ||
			route.MaxSevereErrorRateBPS != 500 {
			t.Fatalf("route %s gates=%+v", route.ID, route)
		}
		if route.ID != "strong" {
			for _, candidate := range route.Candidates {
				if candidate.Model == "strong" {
					t.Fatalf("ordinary route %s includes strong baseline candidate", route.ID)
				}
			}
		}
	}
	if !hasExplanation(result.Explanations, "high_risk_baseline_guard") {
		t.Fatalf("missing high-risk explanation: %+v", result.Explanations)
	}
}

func TestCompileUsesPublicPriorAndSystemProvisionalSafetyMetrics(t *testing.T) {
	withPrior := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveBalanced}, nil, fixtureEvaluationCatalog())
	fast := candidateForTaskDifficulty(t, withPrior.Config, "general", "easy", "fast")
	if fast.QualityScoreBPS != 8_600 || fast.StabilityScoreBPS != 8_000 ||
		fast.SevereErrorRateBPS != 500 || fast.ExpectedLatencyMS != 0 || fast.ProductionEligible {
		t.Fatalf("public-prior candidate=%+v", fast)
	}
	if !hasExplanation(withPrior.Explanations, "external_prior") || !hasExplanation(withPrior.Explanations, "system_provisional") {
		t.Fatalf("explanations=%+v", withPrior.Explanations)
	}

	empty := fixtureEvaluationCatalog()
	empty.Results = nil
	withoutPrior := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveBalanced}, nil, empty)
	fast = candidateForTaskDifficulty(t, withoutPrior.Config, "general", "easy", "fast")
	if fast.QualityScoreBPS != 8_000 || fast.StabilityScoreBPS != 8_000 ||
		fast.SevereErrorRateBPS != 500 || fast.ProductionEligible {
		t.Fatalf("zero-data candidate=%+v", fast)
	}
	if !hasExplanation(withoutPrior.Explanations, "insufficient_data") {
		t.Fatalf("missing insufficient-data explanation: %+v", withoutPrior.Explanations)
	}
}

func TestCompileColdStartCandidatesMeetNormalDifficultyRouteGates(t *testing.T) {
	empty := fixtureEvaluationCatalog()
	empty.Results = nil
	result := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveBalanced}, nil, empty)
	routeID, ok := configuredRouteFor(result.Config, "general", "medium")
	if !ok {
		t.Fatal("general/medium route is missing")
	}
	route := routeForID(t, result.Config, routeID)
	fast := candidateForRoute(t, result.Config, routeID, "fast")
	if fast.QualityScoreBPS < route.MinQualityBPS || fast.StabilityScoreBPS < route.MinStabilityBPS ||
		fast.SevereErrorRateBPS > route.MaxSevereErrorRateBPS {
		t.Fatalf("cold-start candidate=%+v is rejected by generated route gates=%+v", fast, route)
	}
}

func TestCompileUsesPersistedCanonicalIdentityForPublicEvidence(t *testing.T) {
	record := fixtureRecord(true)
	renames := map[string]string{
		"fast": "gateway-fast", "coder": "gateway-coder", "strong": "gateway-strong",
	}
	for index := range record.Config.Models {
		record.Config.Models[index].ID = renames[record.Config.Models[index].ID]
	}
	for index := range record.Config.AutoRouting.Participants {
		record.Config.AutoRouting.Participants[index] = renames[record.Config.AutoRouting.Participants[index]]
	}
	record.Config.AutoRouting.StrongBaselineModel = "gateway-strong"
	record.Config.AutoRouting.TaskAnalyzerModel = "gateway-fast"
	record.Config.AutoRouting.DynamicOptimization.ReviewerModel = "gateway-strong"

	result := compileFixture(t, record, Intent{Objective: ObjectiveBalanced}, nil, fixtureEvaluationCatalog())
	fast := candidateForTaskDifficulty(t, result.Config, "general", "easy", "gateway-fast")
	if fast.QualityScoreBPS != 8_600 || !hasExplanationForModel(result.Explanations, "external_prior", "gateway-fast") {
		t.Fatalf("candidate=%+v explanations=%+v", fast, result.Explanations)
	}
}

func TestCompileResolvesMissingCanonicalIdentityFromDeclaredModelIdentifier(t *testing.T) {
	record := fixtureRecord(true)
	for index := range record.Config.Models {
		record.Config.Models[index].CanonicalModelID = ""
	}

	result := compileFixture(t, record, Intent{Objective: ObjectiveBalanced}, nil, fixtureEvaluationCatalog())
	fast := candidateForTaskDifficulty(t, result.Config, "general", "easy", "fast")
	if fast.QualityScoreBPS != 8_600 || !hasExplanationForModel(result.Explanations, "external_prior", "fast") {
		t.Fatalf("candidate=%+v explanations=%+v", fast, result.Explanations)
	}
	if hasExplanationForModel(result.Explanations, "insufficient_data", "fast") {
		t.Fatalf("unexpected insufficient-data explanation: %+v", result.Explanations)
	}
}

func TestCompileDoesNotInferUnknownModelFamilies(t *testing.T) {
	record := fixtureRecord(true)
	renames := map[string]string{
		"fast": "fast-next", "coder": "coder-next", "strong": "strong-next",
	}
	for index := range record.Config.Models {
		model := &record.Config.Models[index]
		model.ID = renames[model.ID]
		model.CanonicalModelID = ""
	}
	for index := range record.Config.AutoRouting.Participants {
		record.Config.AutoRouting.Participants[index] = renames[record.Config.AutoRouting.Participants[index]]
	}
	record.Config.AutoRouting.StrongBaselineModel = "strong-next"
	record.Config.AutoRouting.TaskAnalyzerModel = "fast-next"
	record.Config.AutoRouting.DynamicOptimization.ReviewerModel = "strong-next"

	result := compileFixture(t, record, Intent{Objective: ObjectiveBalanced}, nil, fixtureEvaluationCatalog())
	fast := candidateForTaskDifficulty(t, result.Config, "general", "easy", "fast-next")
	if fast.QualityScoreBPS != 8_000 || hasExplanationForModel(result.Explanations, "external_prior", "fast-next") {
		t.Fatalf("candidate=%+v explanations=%+v", fast, result.Explanations)
	}
	if !hasExplanationForModel(result.Explanations, "insufficient_data", "fast-next") {
		t.Fatalf("missing insufficient-data explanation: %+v", result.Explanations)
	}
}

func TestCompileReliableLocalEvidenceOverridesPublicPrior(t *testing.T) {
	local := staticLocalEvidence{estimate: LocalEstimate{
		QualityMeanBPS: 9_800, QualityLowerBPS: 9_700, StabilityLowerBPS: 9_600,
		SevereErrorUpperBPS: 100, ExpectedLatencyMS: 320, EffectiveSamples: 42, Reliable: true,
	}}
	result := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveBalanced}, local, fixtureEvaluationCatalog())
	fast := candidateForTaskDifficulty(t, result.Config, "general", "easy", "fast")
	if fast.QualityScoreBPS != 9_700 || fast.StabilityScoreBPS != 9_600 ||
		fast.SevereErrorRateBPS != 100 || fast.ExpectedLatencyMS != 320 || !fast.ProductionEligible {
		t.Fatalf("local candidate=%+v", fast)
	}
	if !hasExplanation(result.Explanations, "local_evidence") {
		t.Fatalf("explanations=%+v", result.Explanations)
	}
}

func TestCompileRequestsLocalEvidencePerDifficulty(t *testing.T) {
	local := &difficultyLocalEvidence{keys: make(map[string]EvidenceKey)}
	result := compileFixture(t, fixtureRecord(true), Intent{Objective: ObjectiveBalanced}, local, fixtureEvaluationCatalog())
	for _, difficulty := range []string{"easy", "medium"} {
		key, ok := local.keys["general/"+difficulty+"/fast"]
		if !ok || key.Domain != "general" || key.Difficulty != difficulty {
			t.Fatalf("missing local evidence key for general/%s: %+v", difficulty, local.keys)
		}
		candidate := candidateForTaskDifficulty(t, result.Config, "general", difficulty, "fast")
		wantLatency := int64(100)
		if difficulty == "medium" {
			wantLatency = 200
		}
		if candidate.ExpectedLatencyMS != wantLatency {
			t.Fatalf("%s candidate=%+v want latency %d", difficulty, candidate, wantLatency)
		}
	}
}

func TestCompileEnabledProfileCanReplaceParticipantsAndRoles(t *testing.T) {
	record := fixtureRecord(true)
	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"coder", "fast"},
	}, nil, fixtureEvaluationCatalog())
	if !reflect.DeepEqual(result.Roles.Participants, []string{"coder", "fast"}) ||
		result.Roles.StrongBaselineModel == "" || result.Roles.TaskAnalyzerModel == "" ||
		!result.Roles.Apply {
		t.Fatalf("roles=%+v", result.Roles)
	}
	for _, route := range result.Config.Routes {
		for _, candidate := range route.Candidates {
			if candidate.Model == "strong" {
				t.Fatalf("replacement strategy still contains removed model: %+v", route.Candidates)
			}
		}
	}
	record.Config.AutoRouting.Participants = append([]string(nil), result.Roles.Participants...)
	record.Config.AutoRouting.StrongBaselineModel = result.Roles.StrongBaselineModel
	record.Config.AutoRouting.TaskAnalyzerModel = result.Roles.TaskAnalyzerModel
	record.Config.AutoRouting.Strategy = result.Config
	if _, err := record.Resolve(); err != nil {
		t.Fatalf("generated config is invalid: %v\n%+v", err, result.Config)
	}
}

func TestCompileDisabledProfileReturnsInitializationRecommendation(t *testing.T) {
	record := fixtureRecord(false)
	record.Config.AutoRouting = profile.AutoRoutingConfig{}
	result := compileFixture(t, record, Intent{
		Objective: ObjectiveCost, Participants: []string{"fast", "coder", "strong"},
	}, nil, fixtureEvaluationCatalog())
	if !result.Roles.Apply || len(result.Roles.Participants) != 3 ||
		result.Roles.StrongBaselineModel == "" || result.Roles.TaskAnalyzerModel != "fast" {
		t.Fatalf("initialization roles=%+v", result.Roles)
	}
	if result.Config.Name == "" || len(result.Config.Routes) == 0 {
		t.Fatalf("initialization config=%+v", result.Config)
	}
}

func TestCompileDisabledProfileRecommendsEvidenceBackedBaselineInsteadOfMostExpensive(t *testing.T) {
	record := fixtureRecord(false)
	record.Config.AutoRouting = profile.AutoRoutingConfig{}
	evaluations := fixtureEvaluationCatalog()
	for index := range evaluations.Results {
		result := &evaluations.Results[index]
		if result.ModelID == "acme/fast" && result.BaselineID == "acme/strong" {
			result.ScoreBPS = 5_500
			result.LowerBPS = 5_300
			result.UpperBPS = 5_700
		}
	}
	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"fast", "coder", "strong"},
	}, nil, evaluations)
	if result.Roles.StrongBaselineModel != "fast" || result.Roles.ReviewerModel != "fast" {
		t.Fatalf("roles=%+v want evidence-backed fast baseline", result.Roles)
	}
	if !hasExplanation(result.Explanations, "role_recommendation_evidence") {
		t.Fatalf("explanations=%+v", result.Explanations)
	}
}

func TestCompileBaselineCoverageDoesNotExpandGeneralEvidenceIntoEveryDomain(t *testing.T) {
	record := fixtureRecord(false)
	record.Config.AutoRouting = profile.AutoRoutingConfig{}
	digest := strings.Repeat("e", 64)
	evaluations := evalcatalog.Catalog{
		SchemaVersion: 1, Revision: digest,
		Sources: []evalcatalog.Source{{
			ID: "fixture", Name: "Fixture", URL: "https://example.test/results", License: "CC-BY-4.0",
			Version: "v1", RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), SHA256: digest,
		}},
		Results: []evalcatalog.Result{
			pairwiseFixture("general", "acme/fast", 6_000, 5_900, digest),
			pairwiseFixture("coding", "acme/coder", 4_500, 4_300, digest),
			pairwiseFixture("reasoning", "acme/coder", 4_500, 4_300, digest),
		},
	}
	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"fast", "coder", "strong"},
	}, nil, evaluations)
	if result.Roles.StrongBaselineModel != "strong" {
		t.Fatalf("roles=%+v want genuinely broader strong baseline", result.Roles)
	}
}

func TestCompileDisabledProfileUsesPriceWithinCapableModelsForProvisionalBaseline(t *testing.T) {
	record := fixtureRecord(false)
	record.Config.AutoRouting = profile.AutoRoutingConfig{}
	for index := range record.Config.Models {
		model := &record.Config.Models[index]
		if model.ID == "fast" {
			contextWindow, maxOutput := 200_000, 16_000
			model.ContextWindow, model.MaxOutputTokens = &contextWindow, &maxOutput
		}
		if model.ID == "strong" {
			contextWindow, maxOutput := 32_000, 4_000
			unsupported := false
			model.ContextWindow, model.MaxOutputTokens = &contextWindow, &maxOutput
			model.SupportsVision, model.SupportsTools, model.SupportsStructuredOutput = &unsupported, &unsupported, &unsupported
		}
	}
	evaluations := fixtureEvaluationCatalog()
	evaluations.Results = nil
	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"fast", "coder", "strong"},
	}, nil, evaluations)
	if result.Roles.StrongBaselineModel != "coder" {
		t.Fatalf("roles=%+v want conservative provisional coder baseline", result.Roles)
	}
	if !hasExplanation(result.Explanations, "role_recommendation_provisional") {
		t.Fatalf("explanations=%+v", result.Explanations)
	}
}

func TestCompileColdStartDoesNotReuseAnUnprovenCheapBaseline(t *testing.T) {
	record := fixtureRecord(true)
	record.Config.AutoRouting.StrongBaselineModel = "fast"
	record.Config.AutoRouting.DynamicOptimization.ReviewerModel = "fast"
	evaluations := fixtureEvaluationCatalog()
	evaluations.Results = nil

	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"fast", "coder", "strong"},
	}, nil, evaluations)
	if result.Roles.StrongBaselineModel != "strong" || result.Roles.ReviewerModel != "strong" {
		t.Fatalf("roles=%+v want conservative cold-start baseline strong", result.Roles)
	}
	strong := routeForID(t, result.Config, "strong")
	if len(strong.Candidates) != 1 {
		t.Fatalf("strong route candidates=%+v", strong.Candidates)
	}
	baseline := strong.Candidates[0]
	if baseline.QualityScoreBPS != generatedMinQualityBPS ||
		baseline.StabilityScoreBPS != generatedMinStabilityBPS ||
		baseline.SevereErrorRateBPS != generatedMaxSevereErrorBPS || !baseline.ProductionEligible {
		t.Fatalf("unproven baseline exposes fabricated perfect scores: %+v", baseline)
	}
	baselinePrice := representativePrice(modelConfigForTest(t, record, baseline.Model))
	for _, route := range result.Config.Routes {
		if route.ID == "strong" {
			continue
		}
		reachable := result.Config.MinNetSavingsBPS == 0
		for _, candidate := range route.Candidates {
			candidatePrice := representativePrice(modelConfigForTest(t, record, candidate.Model))
			if candidatePrice*10_000 <= baselinePrice*(10_000-int64(result.Config.MinNetSavingsBPS)) {
				reachable = true
				break
			}
		}
		if !reachable {
			t.Fatalf("route %q cannot satisfy %d bps savings against baseline %q", route.ID, result.Config.MinNetSavingsBPS, baseline.Model)
		}
	}
}

func TestCompileDisablesSavingsGateWhenNoCandidateCanMeetIt(t *testing.T) {
	record := fixtureRecord(false)
	record.Config.AutoRouting = profile.AutoRoutingConfig{}
	for index := range record.Config.Models {
		input, output := int64(100_000), int64(400_000)
		record.Config.Models[index].InputPriceMicroUSDPerMillion = &input
		record.Config.Models[index].OutputPriceMicroUSDPerMillion = &output
	}
	evaluations := fixtureEvaluationCatalog()
	evaluations.Results = nil

	result := compileFixture(t, record, Intent{
		Objective: ObjectiveBalanced, Participants: []string{"fast", "coder", "strong"},
	}, nil, evaluations)
	if result.Config.MinNetSavingsBPS != 0 {
		t.Fatalf("min_net_savings_bps=%d want 0 for equal-price cold start", result.Config.MinNetSavingsBPS)
	}
	if !hasExplanation(result.Explanations, "net_savings_guard_disabled") {
		t.Fatalf("explanations=%+v", result.Explanations)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	record := fixtureRecord(true)
	compiler := newFixtureCompiler(nil, fixtureEvaluationCatalog())
	first, err := compiler.Compile(context.Background(), record, Intent{Objective: ObjectiveQuality})
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(context.Background(), record, Intent{Objective: ObjectiveQuality})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic results:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestCompileRejectsInvalidIntent(t *testing.T) {
	compiler := newFixtureCompiler(nil, fixtureEvaluationCatalog())
	for _, intent := range []Intent{
		{Objective: "fastest-cheapest"},
		{Objective: ObjectiveBalanced, LatencyTargetMS: intPointer(-1)},
		{Objective: ObjectiveBalanced, MaxCostPerRequestMicroUSD: int64Pointer(-1)},
	} {
		if _, err := compiler.Compile(context.Background(), fixtureRecord(true), intent); err == nil {
			t.Fatalf("Compile accepted intent=%+v", intent)
		}
	}
}

func compileFixture(
	t *testing.T,
	record profile.Record,
	intent Intent,
	local LocalEvidenceProvider,
	evaluations evalcatalog.Catalog,
) Result {
	t.Helper()
	result, err := newFixtureCompiler(local, evaluations).Compile(context.Background(), record, intent)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newFixtureCompiler(local LocalEvidenceProvider, evaluations evalcatalog.Catalog) *Compiler {
	return New(
		staticModelCatalog{catalog: fixtureModelCatalog()},
		staticEvaluationCatalog{catalog: evaluations},
		local,
		func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) },
	)
}

type staticModelCatalog struct{ catalog modelcatalog.Catalog }

func (catalog staticModelCatalog) Current() modelcatalog.Catalog { return catalog.catalog }

type staticEvaluationCatalog struct{ catalog evalcatalog.Catalog }

func (catalog staticEvaluationCatalog) Current() evalcatalog.Catalog { return catalog.catalog }

type staticLocalEvidence struct{ estimate LocalEstimate }

func (provider staticLocalEvidence) CandidateEstimate(context.Context, EvidenceKey) (LocalEstimate, bool, error) {
	return provider.estimate, true, nil
}

type difficultyLocalEvidence struct{ keys map[string]EvidenceKey }

func (provider *difficultyLocalEvidence) CandidateEstimate(_ context.Context, key EvidenceKey) (LocalEstimate, bool, error) {
	provider.keys[key.Domain+"/"+key.Difficulty+"/"+key.CandidateModel] = key
	latency := int64(100)
	if key.Difficulty == "medium" {
		latency = 200
	}
	return LocalEstimate{
		QualityMeanBPS: 9_900, QualityLowerBPS: 9_800, StabilityLowerBPS: 9_700,
		SevereErrorUpperBPS: 10, ExpectedLatencyMS: latency, EffectiveSamples: 40, Reliable: true,
	}, true, nil
}

func fixtureRecord(autoEnabled bool) profile.Record {
	supportsVision := true
	supportsTools := true
	supportsStructured := true
	contextWindow := 128_000
	maxOutput := 8_192
	prices := map[string][2]int64{
		"fast": {100_000, 400_000}, "coder": {500_000, 1_500_000}, "strong": {3_000_000, 15_000_000},
	}
	canonicalIDs := map[string]string{
		"fast": "acme/fast", "coder": "acme/coder", "strong": "acme/strong",
	}
	config := profile.NewConfig(profile.ProtocolAnthropic, "https://example.test")
	for _, id := range []string{"fast", "coder", "strong"} {
		price := prices[id]
		input, output := price[0], price[1]
		config.Models = append(config.Models, profile.ModelCapabilityConfig{
			ID: id, CanonicalModelID: canonicalIDs[id], ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &supportsVision, SupportsTools: &supportsTools,
			SupportsStructuredOutput:     &supportsStructured,
			InputPriceMicroUSDPerMillion: &input, OutputPriceMicroUSDPerMillion: &output,
		})
	}
	config.AutoRouting = profile.AutoRoutingConfig{
		Enabled: autoEnabled, Participants: []string{"fast", "coder", "strong"},
		StrongBaselineModel: "strong", TaskAnalyzerModel: "fast", AnalyzerTimeout: "5s",
		DynamicOptimization: profile.DynamicOptimizationConfig{ReviewerModel: "strong"},
		Strategy: profile.RoutingStrategyConfig{
			Name: "20260804-001", DefaultRoute: "general",
			Routes: []profile.RouteConfig{{
				ID: "general", MinQualityBPS: 8_000, MinStabilityBPS: 8_000, MaxSevereErrorRateBPS: 500,
				Weights: profile.RoutingWeightsConfig{QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000},
				Candidates: []profile.RouteCandidateConfig{
					{Model: "fast", QualityScoreBPS: 8_500, StabilityScoreBPS: 8_000, SevereErrorRateBPS: 500},
					{Model: "coder", QualityScoreBPS: 8_500, StabilityScoreBPS: 8_000, SevereErrorRateBPS: 500},
					{Model: "strong", QualityScoreBPS: 10_000, StabilityScoreBPS: 9_500, SevereErrorRateBPS: 100},
				},
			}},
			Budget: fixtureBudget(),
		},
	}
	return profile.Record{ID: 7, Slug: "auto", DisplayName: "Auto", Enabled: true, Config: config}
}

func modelConfigForTest(t *testing.T, record profile.Record, modelID string) profile.ModelCapabilityConfig {
	t.Helper()
	for _, model := range record.Config.Models {
		if model.ID == modelID {
			return model
		}
	}
	t.Fatalf("model %q not found", modelID)
	return profile.ModelCapabilityConfig{}
}

func fixtureBudget() profile.AttemptBudgetConfig {
	return profile.AttemptBudgetConfig{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
		MaxRetriesPerTarget: 1, MaxModelSwitches: 1,
		Deadline: "2m", MaxWorstCaseCostMicroUSD: 500_000,
	}
}

func fixtureModelCatalog() modelcatalog.Catalog {
	digest := strings.Repeat("c", 64)
	return modelcatalog.Catalog{
		Source: modelcatalog.Source{Revision: digest},
		Models: []modelcatalog.Model{
			{ID: "fast", CanonicalID: "acme/fast", APIIDs: []string{"fast"}},
			{ID: "coder", CanonicalID: "acme/coder", APIIDs: []string{"coder"}},
			{ID: "strong", CanonicalID: "acme/strong", APIIDs: []string{"strong"}},
		},
	}
}

func fixtureEvaluationCatalog() evalcatalog.Catalog {
	digest := strings.Repeat("d", 64)
	catalog := evalcatalog.Catalog{
		SchemaVersion: 1, Revision: digest, RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		Sources: []evalcatalog.Source{{
			ID: "fixture", Name: "Fixture", URL: "https://example.test/results", License: "CC-BY-4.0",
			Version: "v1", RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), SHA256: digest,
		}},
	}
	for _, domain := range []string{"simple", "general", "reasoning", "math", "coding", "tool_use", "vision"} {
		fastScore, fastLower := 4_500, 4_300
		coderScore, coderLower := 4_000, 3_800
		if domain == "coding" || domain == "reasoning" {
			fastScore, fastLower = 4_200, 4_000
			coderScore, coderLower = 4_900, 4_700
		}
		catalog.Results = append(catalog.Results,
			pairwiseFixture(domain, "acme/fast", fastScore, fastLower, digest),
			pairwiseFixture(domain, "acme/coder", coderScore, coderLower, digest),
		)
	}
	return catalog
}

func pairwiseFixture(domain, model string, score, lower int, digest string) evalcatalog.Result {
	return evalcatalog.Result{
		SourceID: "fixture", Benchmark: "fixture-" + domain, Domain: domain,
		ModelID: model, BaselineID: "acme/strong", Metric: evalcatalog.MetricPairwise,
		ScoreBPS: score, LowerBPS: lower, UpperBPS: min(score+200, 10_000), Samples: 100,
		SettingsSHA256: digest,
	}
}

func candidateForRoute(t *testing.T, config profile.RoutingStrategyConfig, routeID, model string) profile.RouteCandidateConfig {
	t.Helper()
	for _, route := range config.Routes {
		if route.ID != routeID {
			continue
		}
		for _, candidate := range route.Candidates {
			if candidate.Model == model {
				return candidate
			}
		}
	}
	t.Fatalf("candidate %s/%s not found in %+v", routeID, model, config.Routes)
	return profile.RouteCandidateConfig{}
}

func candidateForTaskDifficulty(
	t *testing.T,
	config profile.RoutingStrategyConfig,
	taskType, difficulty, model string,
) profile.RouteCandidateConfig {
	t.Helper()
	routeID, ok := configuredRouteFor(config, taskType, difficulty)
	if !ok {
		t.Fatalf("route for %s/%s not found in %+v", taskType, difficulty, config.TaskRoutes)
	}
	return candidateForRoute(t, config, routeID, model)
}

func configuredRouteFor(config profile.RoutingStrategyConfig, taskType, difficulty string) (string, bool) {
	for _, taskRoute := range config.TaskRoutes {
		if taskRoute.TaskType == taskType && taskRoute.Difficulty == difficulty {
			return taskRoute.Route, true
		}
	}
	return "", false
}

func routeForID(t *testing.T, config profile.RoutingStrategyConfig, routeID string) profile.RouteConfig {
	t.Helper()
	for _, route := range config.Routes {
		if route.ID == routeID {
			return route
		}
	}
	t.Fatalf("route %q not found in %+v", routeID, config.Routes)
	return profile.RouteConfig{}
}

func containsCandidate(route profile.RouteConfig, model string) bool {
	for _, candidate := range route.Candidates {
		if candidate.Model == model {
			return true
		}
	}
	return false
}

func hasExplanation(explanations []Explanation, code string) bool {
	for _, explanation := range explanations {
		if explanation.Code == code {
			return true
		}
	}
	return false
}

func hasExplanationForModel(explanations []Explanation, code, model string) bool {
	for _, explanation := range explanations {
		if explanation.Code == code && explanation.Model == model {
			return true
		}
	}
	return false
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
