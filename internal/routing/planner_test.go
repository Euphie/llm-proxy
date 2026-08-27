package routing

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestPlannerRejectsGraphThatDoesNotFitBudgetRemainingAfterAnalyzer(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 1
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 1
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 2
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 600
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), runtime.AutoRouting.Strategy.Budget)
	defer cancel()
	if err := budget.ReserveCall(ctx, CallAnalyzer, 250); err != nil {
		t.Fatal(err)
	}

	_, err = planner.PlanWithBudget(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceAnalyzer},
		SessionPreference{},
		budget.Snapshot(),
	)
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("PlanWithBudget() error=%v, want ErrAttemptBudgetExceeded", err)
	}
}

func TestPlannerZeroCostLimitAllowsPositiveCostCallGraph(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 0
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatalf("plan with unlimited cost budget: %v", err)
	}
	if plan.WorstCaseCostMicroUSD() <= 0 {
		t.Fatalf("worst-case cost=%d, want positive", plan.WorstCaseCostMicroUSD())
	}
}

func TestPlannerUsesCacheWritePriceForHardBudgetButNormalPriceWithoutHistory(t *testing.T) {
	config := routingConfig(false)
	cacheRead := int64(10_000)
	cacheWrite := int64(2_000_000)
	config.Models[0].CacheReadPriceMicroUSDPerMillion = &cacheRead
	config.Models[0].CacheWritePriceMicroUSDPerMillion = &cacheWrite
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := runtime.Models["fast"]
	wantExpected := estimateCallCost(request.Facts.EstimatedInputTokens, 1000, model)
	wantHard := estimateHardCallCost(request.Facts.EstimatedInputTokens, 1000, model)
	if plan.Model() != "fast" || plan.EstimatedCostMicroUSD() != wantExpected ||
		plan.AnswerCallCostMicroUSD() != wantHard || wantHard <= wantExpected {
		t.Fatalf("model=%q expected=%d want_expected=%d hard=%d want_hard=%d",
			plan.Model(), plan.EstimatedCostMicroUSD(), wantExpected,
			plan.AnswerCallCostMicroUSD(), wantHard)
	}
}

func TestPlannerExpectedCostUsesHotSessionCacheAndColdSwitchWrite(t *testing.T) {
	config := routingConfig(false)
	fastRead := int64(10_000)
	fastWrite := int64(2_000_000)
	config.Models[0].CacheReadPriceMicroUSDPerMillion = &fastRead
	config.Models[0].CacheWritePriceMicroUSDPerMillion = &fastWrite
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":"hello"}]
	}`)
	metrics := map[string]CacheMetrics{
		"fast": {
			Samples: 20, UncachedInputTokens: 1000,
			CacheReadTokens: 8000, CacheWriteTokens: 1000,
		},
	}
	hotPlan, err := planner.PlanWithPreference(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceSession,
	}, SessionPreference{
		TaskType: "simple", RouteID: "balanced", Model: "fast",
		MinQualityScoreBPS: 9000, Strategy: runtime.AutoRouting.Strategy.Name,
		CacheMetrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	coldPlan, err := planner.PlanWithPreference(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}, SessionPreference{CacheMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if hotPlan.EstimatedCostMicroUSD() >= coldPlan.EstimatedCostMicroUSD() {
		t.Fatalf("hot expected=%d cold expected=%d", hotPlan.EstimatedCostMicroUSD(), coldPlan.EstimatedCostMicroUSD())
	}
	if hotPlan.AnswerCallCostMicroUSD() != coldPlan.AnswerCallCostMicroUSD() {
		t.Fatalf("hard costs differ: hot=%d cold=%d", hotPlan.AnswerCallCostMicroUSD(), coldPlan.AnswerCallCostMicroUSD())
	}
}

func TestPlannerWorstCaseIncludesVisionRetriesForEveryCompositeModelAttempt(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 2, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 6
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 8
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}
		]}]
	}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 2 {
		t.Fatalf("graph attempts=%+v", graph.Attempts)
	}
	for _, attempt := range graph.Attempts {
		if len(attempt.VisionCallsPerImage) != 1 || attempt.VisionCallsPerImage[0] != 3 {
			t.Fatalf("attempt=%+v, want three vision calls for one image", attempt)
		}
		reservation := attempt.Reservation()
		if reservation.Model != attempt.Model || reservation.Target != attempt.TargetID ||
			len(reservation.VisionCallsPerImage) != 1 || reservation.VisionCallsPerImage[0] != 3 ||
			reservation.AnswerCallCostMicroUSD <= 0 || reservation.VisionCallCostMicroUSD <= 0 {
			t.Fatalf("attempt reservation=%+v", reservation)
		}
	}
	visionCost := plan.ModelAttempts()[0].VisionCallCostMicroUSD()
	fastAnswerCost := plan.ModelAttempts()[0].AnswerCallCostMicroUSD()
	strongAnswerCost := plan.ModelAttempts()[1].AnswerCallCostMicroUSD()
	wantWorst := max(
		visionCost*6+strongAnswerCost,
		visionCost*3+fastAnswerCost+strongAnswerCost,
	)
	if graph.WorstCaseCostMicroUSD != wantWorst ||
		plan.WorstCaseCostMicroUSD() != wantWorst {
		t.Fatalf("worst cost graph=%d plan=%d want=%d", graph.WorstCaseCostMicroUSD, plan.WorstCaseCostMicroUSD(), wantWorst)
	}
}

func TestPlannerCompositeAnswerCostAndContextIncludeBoundedVisionDescriptions(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},
			{"type":"image","source":{"type":"url","url":"https://example.test/b.png"}}
		]}]
	}`)
	plan, err := planner.Plan(request, Classification{TaskType: "simple", Risk: RiskNormal})
	if err != nil {
		t.Fatal(err)
	}
	inputBound, ok := compositeAnswerInputTokens(request.Facts, runtime.Vision)
	if !ok {
		t.Fatal("composite input bound overflowed")
	}
	model := runtime.Models[plan.Model()]
	wantCost := estimateCallCost(inputBound, 1000, model)
	if plan.AnswerCallCostMicroUSD() != wantCost || inputBound <= request.Facts.EstimatedInputTokens {
		t.Fatalf("answer_cost=%d want=%d input_bound=%d base=%d",
			plan.AnswerCallCostMicroUSD(), wantCost, inputBound, request.Facts.EstimatedInputTokens)
	}

	tooSmall := inputBound + 999
	configuredOutput := 1000
	for index := range config.Models[:2] {
		config.Models[index].ContextWindow = &tooSmall
		config.Models[index].MaxOutputTokens = &configuredOutput
	}
	planner, err = NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(request, Classification{TaskType: "simple", Risk: RiskNormal}); !errors.Is(err, ErrNoCapableModel) {
		t.Fatalf("Plan() error=%v, want ErrNoCapableModel", err)
	}
}

func TestPlannerPathEnvelopeKeepsVisualFallbackWithOneAnswerSlot(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 1, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 1
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 4
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 5
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},
			{"type":"text","text":"inspect production access"}
		]}]
	}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "high_risk", Risk: RiskHigh, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 1 || graph.AnswerCalls != 1 ||
		graph.AuxiliaryCalls != 2 || graph.TotalOutboundCalls != 3 {
		t.Fatalf("path envelope=%+v", graph)
	}
	if graph.Attempts[0].TargetID != profile.PrimaryTargetID {
		t.Fatalf("attempts=%+v", graph.Attempts)
	}
	want := plan.VisionCallCostMicroUSD()*2 + plan.AnswerCallCostMicroUSD()
	if graph.WorstCaseCostMicroUSD != want {
		t.Fatalf("worst=%d want=%d", graph.WorstCaseCostMicroUSD, want)
	}
}

func TestPlannerPathEnvelopeDoesNotSumMutuallyExclusiveCompositeBranches(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 1, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 4
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 5
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},
			{"type":"text","text":"inspect production access"}
		]}]
	}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "high_risk", Risk: RiskHigh, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 1 || graph.AnswerCalls != 1 ||
		graph.AuxiliaryCalls != 2 || graph.TotalOutboundCalls != 3 {
		t.Fatalf("path envelope=%+v", graph)
	}
	visionCost := plan.VisionCallCostMicroUSD()
	answerCost := plan.AnswerCallCostMicroUSD()
	wantWorst := visionCost*2 + answerCost
	if graph.WorstCaseCostMicroUSD != wantWorst {
		t.Fatalf("worst=%d want=%d", graph.WorstCaseCostMicroUSD, wantWorst)
	}
}

func TestPlannerUncachedCompositeSuccessRequiresPhysicalVisionCall(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 0, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 1
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 2
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	runtime := resolveRoutingRuntime(t, config)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},
			{"type":"text","text":"inspect production access"}
		]}]
	}`)
	classification := Classification{
		TaskType: "high_risk", Risk: RiskHigh, Source: ClassificationSourceRule,
	}

	plan, err := planner.Plan(request, classification)
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 1 || graph.AuxiliaryCalls != 1 ||
		graph.AnswerCalls != 1 || graph.TotalOutboundCalls != 2 {
		t.Fatalf("impossible zero-call success made fallback reachable: %+v", graph)
	}
	if graph.WorstCaseCostMicroUSD < plan.VisionCallCostMicroUSD()+plan.AnswerCallCostMicroUSD() {
		t.Fatalf("worst cost omitted required visual call: %+v", graph)
	}

	_, err = planner.PlanWithBudget(
		request,
		classification,
		SessionPreference{},
		AttemptBudgetSnapshot{
			AuxiliaryCalls: 1, TotalOutboundCalls: 1,
			WorstCaseCostMicroUSD: plan.VisionCallCostMicroUSD(),
		},
	)
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("PlanWithBudget() error=%v, want exhausted auxiliary rejection", err)
	}

	twoImageRequest := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},
			{"type":"image","source":{"type":"url","url":"https://example.test/b.png"}},
			{"type":"text","text":"compare"}
		]}]
	}`)
	_, err = planner.Plan(twoImageRequest, classification)
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("two-image Plan() error=%v, want per-image visual admission rejection", err)
	}
}

func TestPlannerKeepsShorterConfiguredRetryPathReachable(t *testing.T) {
	config := routingConfig(false)
	config.OverloadRules = []profile.RetryRule{
		{Status: 429, MaxRetries: 1, Delay: "0s", Jitter: "0s"},
		{Status: 503, MaxRetries: 3, Delay: "0s", Jitter: "0s"},
	}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 3
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 3
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 3
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"delete production data"}]}`),
		Classification{TaskType: "high_risk", Risk: RiskHigh, Source: ClassificationSourceRule},
	)
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 1 || graph.AnswerCalls != 3 || graph.TotalOutboundCalls != 3 {
		t.Fatalf("short retry path was not retained: %+v", graph)
	}
}

func TestPlannerChargesConsumedAnalyzerExactlyOnce(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), runtime.AutoRouting.Strategy.Budget)
	defer cancel()
	if err := budget.ReserveCall(ctx, CallAnalyzer, 250); err != nil {
		t.Fatal(err)
	}
	plan, err := planner.PlanWithBudget(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceAnalyzer},
		SessionPreference{},
		budget.Snapshot(),
	)
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	wantEstimated := int64(250) + plan.AnswerCallCostMicroUSD()
	if plan.EstimatedCostMicroUSD() != wantEstimated {
		t.Fatalf("estimated=%d want=%d", plan.EstimatedCostMicroUSD(), wantEstimated)
	}
	if graph.ConsumedBeforePlanMicroUSD != 250 ||
		plan.WorstCaseCostMicroUSD() != 250+graph.WorstCaseCostMicroUSD {
		t.Fatalf("plan=%+v graph=%+v", plan.Snapshot(), graph)
	}
}

func TestPlannerKeepsFallbackWhenExactPerNodeCostFits(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 1
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 2
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 16_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule},
	)
	if err != nil {
		t.Fatal(err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 2 || graph.Attempts[0].Model != "fast" ||
		graph.Attempts[1].Model != "strong" {
		t.Fatalf("graph attempts=%+v", graph.Attempts)
	}
	want := plan.ModelAttempts()[0].AnswerCallCostMicroUSD() +
		plan.ModelAttempts()[1].AnswerCallCostMicroUSD()
	if graph.WorstCaseCostMicroUSD != want {
		t.Fatalf("worst=%d want=%d", graph.WorstCaseCostMicroUSD, want)
	}
}

func TestPlannerTruncatesOptionalFallbackThatDoesNotFitRemainingCost(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 1
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 2
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 1_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule},
	)
	if err != nil {
		t.Fatalf("optional strong fallback should be truncated: %v", err)
	}
	graph := plan.CallGraph()
	if len(graph.Attempts) != 1 || graph.Attempts[0].Model != "fast" {
		t.Fatalf("graph attempts=%+v", graph.Attempts)
	}
	if attempts := plan.ModelAttempts(); len(attempts) != 1 || attempts[0].Model() != "fast" {
		t.Fatalf("executable model attempts=%+v", attempts)
	}
	if graph.WorstCaseCostMicroUSD != plan.AnswerCallCostMicroUSD() {
		t.Fatalf("worst=%d answer=%d", graph.WorstCaseCostMicroUSD, plan.AnswerCallCostMicroUSD())
	}
}

func TestPlannerGraphAppliesModelAndAnswerRetryLimits(t *testing.T) {
	t.Run("model switches", func(t *testing.T) {
		config := routingConfig(false)
		config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 4
		config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 4
		config.AutoRouting.Strategy.Budget.MaxModelSwitches = 0
		planner, err := NewPlanner(resolveRoutingRuntime(t, config))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(
			autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
			Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule},
		)
		if err != nil {
			t.Fatal(err)
		}
		graph := plan.CallGraph()
		if len(graph.Attempts) != 1 || graph.ModelSwitches != 0 ||
			graph.Attempts[0].TargetID != profile.PrimaryTargetID {
			t.Fatalf("graph=%+v", graph)
		}
	})

	t.Run("answer retries", func(t *testing.T) {
		config := routingConfig(false)
		config.OverloadRules = []profile.RetryRule{{
			Status: 503, MaxRetries: 4, Delay: "0s", Jitter: "0s",
		}}
		config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 3
		config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 3
		config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 2
		planner, err := NewPlanner(resolveRoutingRuntime(t, config))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(
			autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"delete production data"}]}`),
			Classification{TaskType: "high_risk", Risk: RiskHigh, Source: ClassificationSourceRule},
		)
		if err != nil {
			t.Fatal(err)
		}
		graph := plan.CallGraph()
		if len(graph.Attempts) != 1 || graph.Attempts[0].AnswerCalls != 3 || graph.AnswerCalls != 3 {
			t.Fatalf("graph=%+v", graph)
		}
		want := plan.AnswerCallCostMicroUSD() * 3
		if graph.WorstCaseCostMicroUSD != want {
			t.Fatalf("worst=%d want=%d", graph.WorstCaseCostMicroUSD, want)
		}
	})
}

func TestPlannerGraphFailsClosedOnImageCallCountOverflow(t *testing.T) {
	config := routingConfig(true)
	unsupported := false
	config.Models[1].SupportsVision = &unsupported
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 1, Delay: "0s", Jitter: "0s",
	}}
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Operation: OperationAnthropicMessages,
		Model:     AutoModel,
		Facts: RequestFacts{
			EstimatedInputTokens: 1, RequestedOutputTokens: 1,
			HasImages: true, ImageCount: math.MaxInt,
		},
	}
	_, err = planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if !errors.Is(err, ErrAttemptBudgetExceeded) && !errors.Is(err, ErrNoCapableModel) {
		t.Fatalf("Plan() error=%v, want overflow rejection", err)
	}
}

func TestPlannerGraphFailsClosedOnOverflowedBudgetSnapshot(t *testing.T) {
	planner, err := NewPlanner(routingRuntime(t, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = planner.PlanWithBudget(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule},
		SessionPreference{},
		AttemptBudgetSnapshot{
			WorstCaseCostMicroUSD: math.MaxInt64,
			HeldCostMicroUSD:      math.MaxInt64,
		},
	)
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("PlanWithBudget() error=%v, want overflow rejection", err)
	}
}

func TestPlannerSelectsBudgetCompatibleNativeFallbackWhenCompositeEnvelopeDoesNotFit(t *testing.T) {
	config := routingConfig(true)
	config.OverloadRules = []profile.RetryRule{{
		Status: 503, MaxRetries: 10, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 1
	config.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 11
	config.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 12
	config.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 20_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{
			"model":"auto","max_tokens":1000,
			"messages":[{"role":"user","content":[
				{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},
				{"type":"text","text":"inspect"}
			]}]
		}`),
		Classification{TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || plan.VisionMode() != VisionNative ||
		len(plan.CallGraph().Attempts) != 1 {
		t.Fatalf("plan=%+v graph=%+v", plan.Snapshot(), plan.CallGraph())
	}
}

func TestPlannerChoosesHighestWeightedQualifiedCandidate(t *testing.T) {
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

func TestPlannerChoosesHighestWeightedScoreAndRecordsSubscores(t *testing.T) {
	config := routingConfig(false)
	route := &config.AutoRouting.Strategy.Routes[0]
	route.MinStabilityBPS = 0
	route.Weights = profile.RoutingWeightsConfig{
		QualityBPS: 5500, StabilityBPS: 3000, CostBPS: 500, PerformanceBPS: 1000,
	}
	route.Candidates[0].StabilityScoreBPS = 500
	route.Candidates[0].ExpectedLatencyMS = 100
	route.Candidates[1].StabilityScoreBPS = 9900
	route.Candidates[1].ExpectedLatencyMS = 1000
	runtime := resolveRoutingRuntime(t, config)
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
	if plan.Model() != "strong" || plan.Reason() != "highest weighted routing score" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
	}
	decisions := plan.CandidateDecisions()
	if len(decisions) != 2 || decisions[0].CostEfficiencyScoreBPS != 10_000 ||
		decisions[0].PerformanceScoreBPS != 10_000 ||
		decisions[1].CostEfficiencyScoreBPS <= 0 || decisions[1].PerformanceScoreBPS != 1000 ||
		decisions[1].RoutingScoreBPS <= decisions[0].RoutingScoreBPS {
		t.Fatalf("decisions=%+v", decisions)
	}
}

func TestPlannerScoresUnknownLatencyConservatively(t *testing.T) {
	config := routingConfig(false)
	route := &config.AutoRouting.Strategy.Routes[0]
	route.Weights = profile.RoutingWeightsConfig{PerformanceBPS: 10_000}
	route.Candidates[0].ExpectedLatencyMS = 0
	runtime := resolveRoutingRuntime(t, config)
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
	decisions := plan.CandidateDecisions()
	if len(decisions) != 2 || decisions[0].Model != "fast" ||
		decisions[0].PerformanceScoreBPS != 0 || plan.Model() != "strong" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), decisions)
	}
}

func TestPlannerScoresAllUnknownLatenciesAsZero(t *testing.T) {
	config := routingConfig(false)
	route := &config.AutoRouting.Strategy.Routes[0]
	route.Weights = profile.RoutingWeightsConfig{PerformanceBPS: 10_000}
	for index := range route.Candidates {
		route.Candidates[index].ExpectedLatencyMS = 0
	}
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range plan.CandidateDecisions() {
		if decision.PerformanceScoreBPS != 0 {
			t.Fatalf("candidate %q performance score=%d, want 0", decision.Model, decision.PerformanceScoreBPS)
		}
	}
}

func TestPlannerAdvertisedToolsRemainACapabilityConstraint(t *testing.T) {
	runtime := routingRuntime(t, false)
	planner, err := NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"tools":[{"name":"weather"}],"messages":[{"role":"user","content":"check weather"}]}`)
	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceAnalyzer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Facts.HasTools || len(request.Facts.ActualToolOperations) != 0 || plan.Model() != "fast" {
		t.Fatalf("facts=%+v plan=%+v", request.Facts, plan.Snapshot())
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
	config.AutoRouting.TaskAnalyzerModel = "strong"
	unsupported := false
	config.Models[0].SupportsTools = &unsupported
	config.Models[0].SupportsAgentWorkflow = &unsupported
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
	wantWorst := attempts[0].AnswerCallCostMicroUSD() + attempts[1].AnswerCallCostMicroUSD()
	if plan.WorstCaseCostMicroUSD() != wantWorst {
		t.Fatalf("worst cost=%d want exact per-node sum=%d", plan.WorstCaseCostMicroUSD(), wantWorst)
	}

	attempts[0] = attempts[1]
	if got := plan.ModelAttempts()[0].Model(); got != "fast" {
		t.Fatalf("execution plan followed caller mutation: %q", got)
	}
}

func TestExecutionPlanFindsOnlyStrictlyStrongerEscalationAttempt(t *testing.T) {
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
	index, ok := plan.NextStrongerModelAttempt(0)
	if !ok || index != 1 || plan.ModelAttempts()[index].Model() != "strong" {
		t.Fatalf("index=%d ok=%v attempts=%+v", index, ok, plan.Snapshot().ModelAttempts)
	}
	if _, ok := plan.NextStrongerModelAttempt(index); ok {
		t.Fatal("strongest attempt unexpectedly has an escalation target")
	}
}

func TestPlannerFreezesSingleProfileUpstreamForEveryModelAttempt(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 5
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
	if len(fastTargets) != 1 || fastTargets[0].ID() != profile.PrimaryTargetID ||
		fastTargets[0].Upstream() != config.Upstream || len(strongTargets) != 1 ||
		strongTargets[0].ID() != profile.PrimaryTargetID || strongTargets[0].Upstream() != config.Upstream {
		t.Fatalf("fast=%+v strong=%+v", fastTargets, strongTargets)
	}
	if fastTargets[0].Protocol() != profile.ProtocolAnthropic {
		t.Fatalf("upstream protocol=%q, want anthropic", fastTargets[0].Protocol())
	}
	fastTargets[0] = TargetPlan{}
	if got := plan.ModelAttempts()[0].Targets()[0].ID(); got != profile.PrimaryTargetID {
		t.Fatalf("plan followed caller mutation: %q", got)
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
	decisions := plan.CandidateDecisions()
	if len(decisions) != 1 || decisions[0].QualityScoreBPS != 9900 ||
		decisions[0].StabilityScoreBPS != 9900 ||
		decisions[0].SevereErrorRateBPS != 10 ||
		decisions[0].ExpectedLatencyMS != 800 ||
		decisions[0].CostEfficiencyScoreBPS != 10_000 ||
		decisions[0].PerformanceScoreBPS != 10_000 ||
		decisions[0].RoutingScoreBPS <= 0 {
		t.Fatalf("high-risk decisions=%+v", decisions)
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

func TestPlannerKeepsShadowEvaluationForProductionIneligibleCandidate(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.Routes[0].Candidates[0].ProductionEligible = false
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	classification := Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}
	if _, ok := planner.EvaluationPair(request, classification, "fast"); !ok {
		t.Fatal("production-ineligible candidate was excluded from Shadow evaluation")
	}
	plan, err := planner.Plan(request, classification)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || decisionReason(plan.CandidateDecisions(), "fast") != "production_not_eligible" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
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
		TaskType: "unknown", Risk: RiskUnknown, Source: ClassificationSourceFallback,
	}, "strong"); ok {
		t.Fatal("analyzer fallback received an evaluation pair")
	}
	if _, ok := planner.EvaluationPair(request, Classification{
		TaskType: "simple", Risk: RiskUnknown, Source: ClassificationSourceRule,
	}, "strong"); ok {
		t.Fatal("unknown-risk request received an evaluation pair")
	}
	if _, ok := planner.EvaluationPair(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	}, "strong"); ok {
		t.Fatal("non-saving candidate received an evaluation pair")
	}
}

func TestPlannerDoesNotEvaluateUnknownRisk(t *testing.T) {
	planner, err := NewPlanner(routingRuntime(t, false))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	if _, ok := planner.EvaluationPair(request, Classification{
		TaskType: "simple", Risk: RiskUnknown, Source: ClassificationSourceRule,
	}, "fast"); ok {
		t.Fatal("unknown-risk request received an evaluation pair")
	}
}

func TestPlannerFiltersCapabilitiesAndQuality(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*profile.Config)
		body   string
		model  string
		reason string
	}{
		{
			name: "tool capability",
			mutate: func(config *profile.Config) {
				config.AutoRouting.TaskAnalyzerModel = "strong"
				unsupported := false
				config.Models[0].SupportsTools = &unsupported
				config.Models[0].SupportsAgentWorkflow = &unsupported
			},
			body:   `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit a file"}]}`,
			model:  "strong",
			reason: "tools_not_supported",
		},
		{
			name: "structured output capability",
			mutate: func(config *profile.Config) {
				config.Models[0].SupportsStructuredOutput = nil
			},
			body:   `{"model":"auto","max_tokens":1000,"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":"return json"}]}`,
			model:  "strong",
			reason: "structured_output_not_supported",
		},
		{
			name: "agent workflow capability",
			mutate: func(config *profile.Config) {
				unsupported := false
				config.Models[0].SupportsAgentWorkflow = &unsupported
			},
			body:   `{"model":"auto","max_tokens":1000,"system":"Available user-invocable skills (invoke with Skill tool):","tools":[{"name":"Skill"}],"messages":[{"role":"user","content":"inspect the project"}]}`,
			model:  "strong",
			reason: "agent_workflow_not_supported",
		},
		{
			name: "quality gate",
			mutate: func(config *profile.Config) {
				config.AutoRouting.Strategy.Routes[0].Candidates[0].QualityScoreBPS = 8999
			},
			body:   `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`,
			model:  "strong",
			reason: "quality_below_route_minimum",
		},
		{
			name: "context window",
			mutate: func(config *profile.Config) {
				small := 100
				output := 50
				config.Models[0].ContextWindow = &small
				config.Models[0].MaxOutputTokens = &output
			},
			body:   `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`,
			model:  "strong",
			reason: "requested_output_exceeds_model_limit",
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
			decisions := plan.CandidateDecisions()
			if len(decisions) < 2 || decisions[0].Model != "fast" ||
				decisions[0].Decision != "rejected" || decisions[0].Reason != tt.reason ||
				decisions[len(decisions)-1].Model != "strong" ||
				decisions[len(decisions)-1].Decision != "selected" {
				t.Fatalf("candidate decisions=%+v, want fast rejected by %q then strong selected", decisions, tt.reason)
			}
		})
	}
}

func TestPlannerRejectsNonBaselineOutsideLatencyTarget(t *testing.T) {
	for _, tt := range []struct {
		name      string
		latencyMS int64
	}{
		{name: "unknown", latencyMS: 0},
		{name: "above target", latencyMS: 501},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := routingConfig(false)
			config.AutoRouting.Strategy.LatencyTargetMS = 500
			config.AutoRouting.Strategy.Routes[0].Candidates[0].ExpectedLatencyMS = tt.latencyMS
			planner, err := NewPlanner(resolveRoutingRuntime(t, config))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planner.Plan(
				autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
				Classification{TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal},
			)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Model() != "strong" || decisionReason(plan.CandidateDecisions(), "fast") != "latency_target_not_met" {
				t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
			}
		})
	}
}

func TestPlannerGuardedStrategyForcesHardAndUnknownDifficultyToBaseline(t *testing.T) {
	config := routingConfig(false)
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	for _, difficulty := range []Difficulty{DifficultyHard, DifficultyUnknown} {
		plan, err := planner.Plan(request, Classification{
			TaskType: "simple", Difficulty: difficulty, Risk: RiskNormal, Source: ClassificationSourceRule,
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Model() != "strong" || plan.Reason() != "guarded difficulty baseline" {
			t.Fatalf("difficulty=%s plan=%+v", difficulty, plan.Snapshot())
		}
	}
}

func TestPlannerRejectsNonBaselineWhenAnalyzerErasesNetSavings(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.MinNetSavingsBPS = 1_000
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`)
	baseline, err := planner.Plan(request, Classification{TaskType: "simple", Risk: RiskHigh})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.PlanWithBudget(
		request,
		Classification{TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal},
		SessionPreference{},
		AttemptBudgetSnapshot{WorstCaseCostMicroUSD: baseline.EstimatedCostMicroUSD()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || decisionReason(plan.CandidateDecisions(), "fast") != "net_savings_below_minimum" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
	}
}

func TestPlannerNetSavingsUsesCappedRetryAndBaselineFallbackProbability(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.MinNetSavingsBPS = 1_000
	config.AutoRouting.Strategy.Routes[0].Candidates[0].StabilityScoreBPS = 9_500
	config.OverloadRules = []profile.RetryRule{{Status: 429, MaxRetries: 1, Delay: "1ms", Jitter: "0s"}}
	strongInput := *config.Models[1].InputPriceMicroUSDPerMillion
	strongOutput := *config.Models[1].OutputPriceMicroUSDPerMillion
	fastInput := strongInput * 83 / 100
	fastOutput := strongOutput * 83 / 100
	config.Models[0].InputPriceMicroUSDPerMillion = &fastInput
	config.Models[0].OutputPriceMicroUSDPerMillion = &fastOutput
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || decisionReason(plan.CandidateDecisions(), "fast") != "highest_weighted_routing_score" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
	}
}

func TestPlannerExpectedLifecycleCostUsesCappedGeometricFailureProbability(t *testing.T) {
	planner := &Planner{
		strategy: profile.RoutingStrategyRuntime{Budget: profile.AttemptBudgetRuntime{
			MaxRetriesPerTarget: 2,
			MaxModelSwitches:    1,
		}},
		maxConfiguredRetries: 2,
	}
	for _, tt := range []struct {
		name         string
		stabilityBPS int
		want         int64
	}{
		{name: "twenty percent failure", stabilityBPS: 8_000, want: 132_000},
		{name: "small failure probability", stabilityBPS: 9_999, want: 100_011},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := planner.expectedCandidateLifecycleCost(
				Request{},
				candidatePlan{estimatedCost: 100_000},
				candidatePlan{estimatedCost: 1_000_000},
				profile.RouteCandidateRuntime{StabilityScoreBPS: tt.stabilityBPS},
			)
			if got != tt.want {
				t.Fatalf("expected lifecycle cost=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestPlannerNetSavingsIncludesSampledEvaluationOverhead(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.MinNetSavingsBPS = 1_000
	config.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	config.AutoRouting.Strategy.Budget.MaxModelSwitches = 0
	config.AutoRouting.Strategy.Routes[0].Candidates[0].StabilityScoreBPS = 10_000
	config.AutoRouting.Strategy.Routes[0].Candidates[0].SevereErrorRateBPS = 0
	config.AutoRouting.DynamicOptimization = profile.DynamicOptimizationConfig{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 8, TaskTimeout: "1m",
	}
	strongInput := *config.Models[1].InputPriceMicroUSDPerMillion
	strongOutput := *config.Models[1].OutputPriceMicroUSDPerMillion
	fastInput := strongInput * 83 / 100
	fastOutput := strongOutput * 83 / 100
	config.Models[0].InputPriceMicroUSDPerMillion = &fastInput
	config.Models[0].OutputPriceMicroUSDPerMillion = &fastOutput
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
		Classification{TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || decisionReason(plan.CandidateDecisions(), "fast") != "net_savings_below_minimum" {
		t.Fatalf("plan=%+v decisions=%+v", plan.Snapshot(), plan.CandidateDecisions())
	}
}

func decisionReason(decisions []CandidateDecision, model string) string {
	for _, decision := range decisions {
		if decision.Model == model {
			return decision.Reason
		}
	}
	return ""
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

func TestPlannerExcludesFileIDCompositeOverChatTransport(t *testing.T) {
	config := routingConfig(true)
	config.Protocol = profile.ProtocolOpenAI
	config.Vision.Transport = profile.VisionTransportOpenAIChatCompletions
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoOpenAIRequest(
		t,
		"/v1/responses",
		`{"model":"auto","max_output_tokens":1000,"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-123"},{"type":"input_text","text":"inspect"}]}]}`,
	)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || plan.VisionMode() != VisionNative {
		t.Fatalf("plan=%+v, want native strong model", plan.Snapshot())
	}
}

func TestPlannerCompositeUsesSingleProfileUpstream(t *testing.T) {
	config := routingConfig(true)
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"inspect"}]}]}`)

	plan, err := planner.Plan(request, Classification{
		TaskType: "simple", Risk: RiskNormal, Source: ClassificationSourceRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := plan.ModelAttempts()[0].Targets()
	if len(targets) != 1 || targets[0].ID() != profile.PrimaryTargetID ||
		targets[0].Upstream() != config.Upstream {
		t.Fatalf("composite targets=%+v, want only profile upstream", targets)
	}
}

func TestRouteSpecificityIncludesDifficulty(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.TaskRoutes = []profile.TaskRouteConfig{
		{TaskType: "coding", Difficulty: "hard", Route: "strong"},
		{TaskType: "coding", Route: "balanced"},
		{Difficulty: "easy", Route: "strong"},
	}
	planner, err := NewPlanner(resolveRoutingRuntime(t, config))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		task       string
		difficulty Difficulty
		want       string
	}{
		{task: "coding", difficulty: DifficultyHard, want: "strong"},
		{task: "coding", difficulty: DifficultyEasy, want: "balanced"},
		{task: "general", difficulty: DifficultyEasy, want: "strong"},
		{task: "unknown", difficulty: DifficultyUnknown, want: "balanced"},
	}
	for _, tt := range tests {
		if got := planner.RouteID(Classification{TaskType: tt.task, Difficulty: tt.difficulty}); got != tt.want {
			t.Fatalf("RouteID(%q, %q)=%q, want %q", tt.task, tt.difficulty, got, tt.want)
		}
	}
}

func TestPlannerFailsWhenStrongBaselineCannotSatisfyHardConstraints(t *testing.T) {
	config := routingConfig(false)
	unsupported := false
	config.Models[1].SupportsTools = &unsupported
	config.Models[1].SupportsAgentWorkflow = &unsupported
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

func autoOpenAIRequest(t *testing.T, path string, body string) Request {
	t.Helper()
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		path,
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
			SupportsVision: &no, SupportsTools: &yes, SupportsAgentWorkflow: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &fastInput, OutputPriceMicroUSDPerMillion: &fastOutput,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &yes, SupportsTools: &yes, SupportsAgentWorkflow: &yes, SupportsStructuredOutput: &yes,
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
				{TaskType: "reasoning", Route: "strong"},
			},
			Routes: []profile.RouteConfig{
				{
					ID: "balanced", MinQualityBPS: 9000, MinStabilityBPS: 8000,
					MaxSevereErrorRateBPS: 100,
					Weights: profile.RoutingWeightsConfig{
						QualityBPS: 4000, StabilityBPS: 2500, CostBPS: 2500, PerformanceBPS: 1000,
					},
					Candidates: []profile.RouteCandidateConfig{
						{Model: "fast", ProductionEligible: true, QualityScoreBPS: 9200, StabilityScoreBPS: 9300, SevereErrorRateBPS: 50, ExpectedLatencyMS: 250},
						{Model: "strong", ProductionEligible: true, QualityScoreBPS: 9900, StabilityScoreBPS: 9900, SevereErrorRateBPS: 10, ExpectedLatencyMS: 800},
					},
				},
				{
					ID: "strong", MinQualityBPS: 9800, MinStabilityBPS: 9800,
					MaxSevereErrorRateBPS: 50,
					Weights: profile.RoutingWeightsConfig{
						QualityBPS: 4000, StabilityBPS: 2500, CostBPS: 2500, PerformanceBPS: 1000,
					},
					Candidates: []profile.RouteCandidateConfig{
						{Model: "strong", ProductionEligible: true, QualityScoreBPS: 9900, StabilityScoreBPS: 9900, SevereErrorRateBPS: 10, ExpectedLatencyMS: 800},
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
