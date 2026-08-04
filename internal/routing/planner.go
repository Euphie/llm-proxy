package routing

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/profile"
)

var (
	ErrRoutingDisabled = errors.New("intelligent routing is disabled")
	ErrNoCapableModel  = errors.New("no model satisfies the routing constraints")
)

type Risk string

const (
	RiskUnknown Risk = "unknown"
	RiskNormal  Risk = "normal"
	RiskHigh    Risk = "high"
)

type Difficulty string

const (
	DifficultyUnknown Difficulty = "unknown"
	DifficultyEasy    Difficulty = "easy"
	DifficultyMedium  Difficulty = "medium"
	DifficultyHard    Difficulty = "hard"
)

type ClassificationSource string

const (
	ClassificationSourceRule     ClassificationSource = "rule"
	ClassificationSourceAnalyzer ClassificationSource = "analyzer"
	ClassificationSourceFallback ClassificationSource = "fallback"
	ClassificationSourceSession  ClassificationSource = "session"
)

type Classification struct {
	TaskType      string
	Difficulty    Difficulty
	Risk          Risk
	ConfidenceBPS int
	Source        ClassificationSource
	ReasonCodes   []string
}

type SessionPreference struct {
	TaskType           string
	Difficulty         Difficulty
	RouteID            string
	Model              string
	MinQualityScoreBPS int
	Strategy           string
	CacheMetrics       map[string]CacheMetrics
}

type CacheMetrics struct {
	Samples             int64
	UncachedInputTokens int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
}

type VisionMode string

const (
	VisionNone      VisionMode = "none"
	VisionNative    VisionMode = "native"
	VisionComposite VisionMode = "composite"
)

type Planner struct {
	models                profile.ModelCatalog
	targets               map[string][]TargetPlan
	vision                profile.VisionRuntime
	auto                  profile.AutoRoutingRuntime
	strategy              profile.RoutingStrategyRuntime
	maxConfiguredRetries  int
	configuredRetryCounts []int
}

type ExecutionPlan struct {
	strategyName           string
	routeID                string
	model                  string
	visionMode             VisionMode
	usesStrongBaseline     bool
	estimatedCostMicroUSD  int64
	worstCaseCostMicroUSD  int64
	answerCallCostMicroUSD int64
	visionCallCostMicroUSD int64
	reason                 string
	budget                 profile.AttemptBudgetRuntime
	modelAttempts          []ModelAttemptPlan
	candidateDecisions     []CandidateDecision
	callGraph              CallGraphSnapshot
}

type CandidateDecision struct {
	Model                  string
	Decision               string
	Reason                 string
	QualityScoreBPS        int
	SevereErrorRateBPS     int
	ExpectedCostMicroUSD   int64
	AnswerCallCostMicroUSD int64
	VisionCallCostMicroUSD int64
	VisionMode             VisionMode
	UpstreamNodeIDs        []string
}

type ExecutionPlanSnapshot struct {
	StrategyName           string
	RouteID                string
	Model                  string
	VisionMode             VisionMode
	UsesStrongBaseline     bool
	EstimatedCostMicroUSD  int64
	WorstCaseCostMicroUSD  int64
	AnswerCallCostMicroUSD int64
	VisionCallCostMicroUSD int64
	Reason                 string
	ModelAttempts          []ModelAttemptSnapshot
}

type ModelAttemptPlan struct {
	model                  string
	targets                []TargetPlan
	visionMode             VisionMode
	qualityScoreBPS        int
	expectedCostMicroUSD   int64
	answerCallCostMicroUSD int64
	visionCallCostMicroUSD int64
}

type ModelAttemptSnapshot struct {
	Model                  string
	TargetIDs              []string
	VisionMode             VisionMode
	QualityScoreBPS        int
	ExpectedCostMicroUSD   int64
	AnswerCallCostMicroUSD int64
	VisionCallCostMicroUSD int64
}

type EvaluationPair struct {
	Candidate ModelAttemptPlan
	Reference ModelAttemptPlan
}

type EvaluationPairSnapshot struct {
	Candidate ModelAttemptSnapshot
	Reference ModelAttemptSnapshot
}

type TargetPlan struct {
	id       string
	upstream string
	protocol profile.Protocol
}

func NewPlanner(runtime profile.Runtime) (*Planner, error) {
	if !runtime.AutoRouting.Enabled {
		return nil, ErrRoutingDisabled
	}
	planner := &Planner{
		models:   cloneModels(runtime.Models),
		targets:  buildTargetPlans(runtime),
		vision:   runtime.Vision,
		auto:     cloneAutoRouting(runtime.AutoRouting),
		strategy: cloneStrategy(runtime.AutoRouting.Strategy),
	}
	retryCounts := make(map[int]struct{}, len(runtime.OverloadRules))
	for _, rule := range runtime.OverloadRules {
		planner.maxConfiguredRetries = max(planner.maxConfiguredRetries, rule.MaxRetries)
		retryCounts[rule.MaxRetries] = struct{}{}
	}
	if len(retryCounts) == 0 {
		retryCounts[0] = struct{}{}
	}
	planner.configuredRetryCounts = make([]int, 0, len(retryCounts))
	for retries := range retryCounts {
		planner.configuredRetryCounts = append(planner.configuredRetryCounts, retries)
	}
	sort.Ints(planner.configuredRetryCounts)
	return planner, nil
}

func (p *Planner) Plan(request Request, classification Classification) (ExecutionPlan, error) {
	return p.PlanWithPreference(request, classification, SessionPreference{})
}

func (p *Planner) PlanWithPreference(
	request Request,
	classification Classification,
	preference SessionPreference,
) (ExecutionPlan, error) {
	return p.planWithPreference(request, classification, preference, nil)
}

func (p *Planner) PlanWithBudget(
	request Request,
	classification Classification,
	preference SessionPreference,
	budget AttemptBudgetSnapshot,
) (ExecutionPlan, error) {
	return p.planWithPreference(request, classification, preference, &budget)
}

func (p *Planner) planWithPreference(
	request Request,
	classification Classification,
	preference SessionPreference,
	budget *AttemptBudgetSnapshot,
) (ExecutionPlan, error) {
	routeID := p.RouteID(classification)
	route := p.strategy.Routes[routeID]

	if classification.Source == ClassificationSourceFallback {
		plan, err := p.planStrongBaseline(request, routeID, "task analyzer fallback", preference)
		return p.finalizePlan(request, plan, budget, err)
	}
	if classification.Risk == RiskHigh {
		plan, err := p.planStrongBaseline(request, routeID, "high risk", preference)
		return p.finalizePlan(request, plan, budget, err)
	}

	candidates := append([]profile.RouteCandidateRuntime(nil), route.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Model < candidates[j].Model
	})

	ranked := make([]rankedCandidate, 0, len(candidates))
	decisions := make([]CandidateDecision, 0, len(candidates)+1)
	for _, candidate := range candidates {
		decision := CandidateDecision{
			Model: candidate.Model, QualityScoreBPS: candidate.QualityScoreBPS,
			SevereErrorRateBPS: candidate.SevereErrorRateBPS,
		}
		switch {
		case candidate.QualityScoreBPS < route.MinQualityBPS:
			decision.Decision = "rejected"
			decision.Reason = "quality_below_route_minimum"
			decisions = append(decisions, decision)
			continue
		case candidate.SevereErrorRateBPS > route.MaxSevereErrorRateBPS:
			decision.Decision = "rejected"
			decision.Reason = "severe_error_rate_above_route_maximum"
			decisions = append(decisions, decision)
			continue
		case candidate.QualityScoreBPS < preference.MinQualityScoreBPS:
			decision.Decision = "rejected"
			decision.Reason = "quality_below_session_minimum"
			decisions = append(decisions, decision)
			continue
		}
		planned, reason, ok := p.evaluateCandidateWithReason(
			request, candidate.Model, preference,
		)
		if !ok {
			decision.Decision = "rejected"
			decision.Reason = reason
			decisions = append(decisions, decision)
			continue
		}
		planned.qualityScoreBPS = candidate.QualityScoreBPS
		decision.Decision = "eligible"
		decision.Reason = "passed_all_gates"
		decision.ExpectedCostMicroUSD = planned.estimatedCost
		decision.AnswerCallCostMicroUSD = planned.answerCallCost
		decision.VisionCallCostMicroUSD = planned.visionCallCost
		decision.VisionMode = planned.visionMode
		decision.UpstreamNodeIDs = candidateTargetIDs(planned.targets)
		decisions = append(decisions, decision)
		ranked = append(ranked, rankedCandidate{plan: planned, metrics: candidate})
	}
	if len(ranked) == 0 {
		plan, err := p.planStrongBaseline(
			request,
			routeID,
			"no route candidate passed all gates",
			preference,
		)
		plan.candidateDecisions = append(decisions, plan.candidateDecisions...)
		return p.finalizePlan(request, plan, budget, err)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return betterCandidate(ranked[i].plan, ranked[j].plan, ranked[i].metrics, ranked[j].metrics)
	})
	reason := "lowest expected cost"
	if preference.Model != "" {
		for index := range ranked {
			if ranked[index].plan.model == preference.Model {
				ranked[0], ranked[index] = ranked[index], ranked[0]
				reason = "Session binding"
				break
			}
		}
	}
	attempts := p.plannedAttempts(request, route, ranked, preference)
	plan := p.executionPlan(
		routeID,
		attempts,
		attempts[0].model == p.auto.StrongBaselineModel,
		reason,
	)
	plan.candidateDecisions = markSelectedCandidate(decisions, attempts[0].model, reason)
	return p.finalizePlan(request, plan, budget, nil)
}

func (p *Planner) RouteID(classification Classification) string {
	if mapped, ok := p.strategy.RouteFor(
		classification.TaskType,
		string(classification.Difficulty),
	); ok {
		return mapped
	}
	return p.strategy.DefaultRoute
}

func (p *Planner) EvaluationPair(
	request Request,
	classification Classification,
	selectedModel string,
) (EvaluationPair, bool) {
	if classification.Source == ClassificationSourceFallback || classification.Risk == RiskHigh {
		return EvaluationPair{}, false
	}
	route := p.strategy.Routes[p.RouteID(classification)]
	reference, ok := p.evaluateCandidate(request, p.auto.StrongBaselineModel, SessionPreference{})
	if !ok {
		return EvaluationPair{}, false
	}
	reference.qualityScoreBPS = 10_000
	if metrics := candidatesForModel(route.Candidates, reference.model); metrics.Model != "" {
		reference.qualityScoreBPS = metrics.QualityScoreBPS
	}

	var candidate candidatePlan
	if selectedModel != "" && selectedModel != reference.model {
		metrics := candidatesForModel(route.Candidates, selectedModel)
		if metrics.Model == "" {
			return EvaluationPair{}, false
		}
		candidate, ok = p.evaluateCandidate(request, selectedModel, SessionPreference{})
		if !ok {
			return EvaluationPair{}, false
		}
		candidate.qualityScoreBPS = metrics.QualityScoreBPS
	} else {
		candidates := make([]rankedCandidate, 0, len(route.Candidates))
		for _, metrics := range route.Candidates {
			if metrics.Model == reference.model {
				continue
			}
			planned, capable := p.evaluateCandidate(request, metrics.Model, SessionPreference{})
			if !capable {
				continue
			}
			planned.qualityScoreBPS = metrics.QualityScoreBPS
			candidates = append(candidates, rankedCandidate{plan: planned, metrics: metrics})
		}
		if len(candidates) == 0 {
			return EvaluationPair{}, false
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return betterCandidate(
				candidates[i].plan, candidates[j].plan,
				candidates[i].metrics, candidates[j].metrics,
			)
		})
		candidate = candidates[0].plan
	}
	if candidate.estimatedCost >= reference.estimatedCost {
		return EvaluationPair{}, false
	}
	return EvaluationPair{
		Candidate: modelAttemptPlan(candidate),
		Reference: modelAttemptPlan(reference),
	}, true
}

func (p *Planner) planStrongBaseline(
	request Request,
	routeID string,
	reason string,
	preference SessionPreference,
) (ExecutionPlan, error) {
	candidate, _, ok := p.evaluateCandidateWithReason(
		request, p.auto.StrongBaselineModel, preference,
	)
	if !ok {
		return ExecutionPlan{}, fmt.Errorf("%w: strong baseline %q", ErrNoCapableModel, p.auto.StrongBaselineModel)
	}
	candidate.qualityScoreBPS = 10_000
	plan := p.executionPlan(routeID, []candidatePlan{candidate}, true, reason)
	plan.candidateDecisions = []CandidateDecision{{
		Model: p.auto.StrongBaselineModel, Decision: "selected", Reason: selectionReasonCode(reason),
		QualityScoreBPS: 10_000, ExpectedCostMicroUSD: candidate.estimatedCost,
		AnswerCallCostMicroUSD: candidate.answerCallCost,
		VisionCallCostMicroUSD: candidate.visionCallCost,
		VisionMode:             candidate.visionMode, UpstreamNodeIDs: candidateTargetIDs(candidate.targets),
	}}
	return plan, nil
}

func (p *Planner) executionPlan(
	routeID string,
	attempts []candidatePlan,
	baseline bool,
	reason string,
) ExecutionPlan {
	candidate := attempts[0]
	modelAttempts := make([]ModelAttemptPlan, 0, len(attempts))
	for _, attempt := range attempts {
		modelAttempts = append(modelAttempts, modelAttemptPlan(attempt))
	}
	return ExecutionPlan{
		strategyName:           p.strategy.Name,
		routeID:                routeID,
		model:                  candidate.model,
		visionMode:             candidate.visionMode,
		usesStrongBaseline:     baseline,
		estimatedCostMicroUSD:  candidate.estimatedCost,
		answerCallCostMicroUSD: candidate.answerCallCost,
		visionCallCostMicroUSD: candidate.visionCallCost,
		reason:                 reason,
		budget:                 p.strategy.Budget,
		modelAttempts:          modelAttempts,
	}
}

type rankedCandidate struct {
	plan    candidatePlan
	metrics profile.RouteCandidateRuntime
}

func (p *Planner) plannedAttempts(
	request Request,
	route profile.RouteRuntime,
	ranked []rankedCandidate,
	preference SessionPreference,
) []candidatePlan {
	capacity := min(
		len(ranked)+1,
		p.strategy.Budget.MaxModelSwitches+1,
	)
	attempts := []candidatePlan{ranked[0].plan}
	if capacity <= 1 {
		return attempts
	}

	baseline, baselineOK := p.evaluateCandidate(request, p.auto.StrongBaselineModel, preference)
	baseline.qualityScoreBPS = 10_000
	if metrics := candidatesForModel(route.Candidates, baseline.model); metrics.Model != "" {
		baseline.qualityScoreBPS = metrics.QualityScoreBPS
	}
	baselineAdded := baselineOK && baseline.model != attempts[0].model &&
		!containsAttempt(attempts, baseline.model)
	if baselineAdded {
		attempts = append(attempts, baseline)
	}
	for _, candidate := range ranked[1:] {
		if len(attempts) >= capacity {
			break
		}
		if candidate.plan.model == p.auto.StrongBaselineModel ||
			containsAttempt(attempts, candidate.plan.model) {
			continue
		}
		if baselineAdded {
			attempts = append(attempts, candidatePlan{})
			attempts[len(attempts)-1] = attempts[len(attempts)-2]
			attempts[len(attempts)-2] = candidate.plan
			continue
		}
		attempts = append(attempts, candidate.plan)
	}
	return attempts
}

func containsAttempt(attempts []candidatePlan, model string) bool {
	for _, attempt := range attempts {
		if attempt.model == model {
			return true
		}
	}
	return false
}

func candidateTargetIDs(targets []TargetPlan) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.id)
	}
	return ids
}

func markSelectedCandidate(
	decisions []CandidateDecision,
	selectedModel string,
	selectionReason string,
) []CandidateDecision {
	reason := selectionReasonCode(selectionReason)
	selected := append([]CandidateDecision(nil), decisions...)
	for index := range selected {
		if selected[index].Decision == "selected" {
			selected[index].Decision = "eligible"
			selected[index].Reason = "passed_all_gates"
		}
	}
	for index := range selected {
		if selected[index].Model == selectedModel && selected[index].Decision == "eligible" {
			selected[index].Decision = "selected"
			selected[index].Reason = reason
			break
		}
	}
	return selected
}

func selectionReasonCode(reason string) string {
	switch reason {
	case "Session binding":
		return "session_binding"
	case "first budget-compatible fallback":
		return "budget_compatible_fallback"
	case "high risk":
		return "high_risk"
	case "task analyzer fallback":
		return "task_analyzer_fallback"
	case "no route candidate passed all gates":
		return "no_route_candidate_passed_all_gates"
	default:
		return "lowest_expected_cost"
	}
}

func modelAttemptPlan(candidate candidatePlan) ModelAttemptPlan {
	return ModelAttemptPlan{
		model:                  candidate.model,
		targets:                cloneTargetPlans(candidate.targets),
		visionMode:             candidate.visionMode,
		qualityScoreBPS:        candidate.qualityScoreBPS,
		expectedCostMicroUSD:   candidate.estimatedCost,
		answerCallCostMicroUSD: candidate.answerCallCost,
		visionCallCostMicroUSD: candidate.visionCallCost,
	}
}

type candidatePlan struct {
	model           string
	targets         []TargetPlan
	visionMode      VisionMode
	qualityScoreBPS int
	estimatedCost   int64
	answerCallCost  int64
	visionCallCost  int64
}

func (p *Planner) evaluateCandidate(
	request Request,
	modelID string,
	preference SessionPreference,
) (candidatePlan, bool) {
	candidate, _, ok := p.evaluateCandidateWithReason(request, modelID, preference)
	return candidate, ok
}

func (p *Planner) evaluateCandidateWithReason(
	request Request,
	modelID string,
	preference SessionPreference,
) (candidatePlan, string, bool) {
	model, ok := p.models[modelID]
	if !ok || !model.HasContextWindow || !model.HasMaxOutputTokens ||
		!model.HasInputPrice || !model.HasOutputPrice {
		return candidatePlan{}, "model_facts_or_prices_incomplete", false
	}
	targets := p.targets[modelID]
	if len(targets) == 0 {
		return candidatePlan{}, "no_compatible_upstream_node", false
	}
	outputTokens := request.Facts.RequestedOutputTokens
	if outputTokens == 0 {
		outputTokens = min(4096, model.MaxOutputTokens)
	}
	if outputTokens <= 0 || outputTokens > model.MaxOutputTokens {
		return candidatePlan{}, "requested_output_exceeds_model_limit", false
	}
	requiresTools := request.Facts.HasTools ||
		(p.auto.SelfEscalation.Enabled && modelID != p.auto.StrongBaselineModel)
	if requiresTools && (!model.HasSupportsTools || !model.SupportsTools) {
		return candidatePlan{}, "tools_not_supported", false
	}
	if request.Facts.RequiresStructuredOutput &&
		(!model.HasSupportsStructuredOutput || !model.SupportsStructuredOutput) {
		return candidatePlan{}, "structured_output_not_supported", false
	}

	visionMode := VisionNone
	visionCost := int64(0)
	visionCallCost := int64(0)
	if request.Facts.HasImages {
		switch {
		case model.SupportsVision:
			visionMode = VisionNative
		case p.vision.Enabled:
			visionModel, exists := p.models[p.vision.Model]
			if !exists || !visionModel.HasInputPrice || !visionModel.HasOutputPrice {
				return candidatePlan{}, "vision_model_facts_or_prices_incomplete", false
			}
			if !visionTransportSupportsSources(p.vision.Transport, request.Facts.ImageSources) {
				return candidatePlan{}, "image_source_not_supported", false
			}
			targets = intersectTargetPlans(targets, p.targets[p.vision.Model])
			if len(targets) == 0 {
				return candidatePlan{}, "no_shared_vision_upstream_node", false
			}
			visionMode = VisionComposite
			visionCallCost = estimateCallCost(
				profile.VisionImagePromptReserveTokens,
				p.vision.MaxTokens,
				visionModel,
			)
			visionCost = multiplyCost(visionCallCost, request.Facts.ImageCount)
		default:
			return candidatePlan{}, "vision_not_supported", false
		}
	}

	answerInputTokens := request.Facts.EstimatedInputTokens
	if visionMode == VisionComposite {
		var fits bool
		answerInputTokens, fits = compositeAnswerInputTokens(request.Facts, p.vision)
		if !fits {
			return candidatePlan{}, "vision_description_context_overflow", false
		}
	}
	if p.auto.SelfEscalation.Enabled && modelID != p.auto.StrongBaselineModel {
		var fits bool
		answerInputTokens, fits = addCount(answerInputTokens, SelfEscalationReserveTokens)
		if !fits {
			return candidatePlan{}, "self_escalation_context_overflow", false
		}
	}
	if answerInputTokens > model.ContextWindow-outputTokens {
		return candidatePlan{}, "context_window_exceeded", false
	}
	expectedAnswerCost := estimateExpectedCallCost(
		answerInputTokens,
		outputTokens,
		model,
		preference.CacheMetrics[modelID],
		preference.Model == modelID,
	)
	answerCost := estimateHardCallCost(answerInputTokens, outputTokens, model)
	auxiliaryCost := visionCost
	estimated := addCost(expectedAnswerCost, auxiliaryCost)
	return candidatePlan{
		model: modelID, targets: cloneTargetPlans(targets), visionMode: visionMode,
		estimatedCost:  estimated,
		answerCallCost: answerCost, visionCallCost: visionCallCost,
	}, "passed_all_gates", true
}

func compositeAnswerInputTokens(
	facts RequestFacts,
	vision profile.VisionRuntime,
) (int, bool) {
	if vision.MaxTokens < 0 ||
		vision.MaxTokens > int(^uint(0)>>1)/profile.VisionDescriptionBytesPerToken {
		return 0, false
	}
	descriptionTokens := vision.MaxTokens * profile.VisionDescriptionBytesPerToken
	perImage, ok := addCount(descriptionTokens, profile.VisionDescriptionWrapperReserveTokens)
	if !ok || facts.ImageCount < 0 ||
		(facts.ImageCount > 0 && perImage > int(^uint(0)>>1)/facts.ImageCount) {
		return 0, false
	}
	return addCount(facts.EstimatedInputTokens, perImage*facts.ImageCount)
}

func betterCandidate(
	candidate candidatePlan,
	selected candidatePlan,
	candidateMetrics profile.RouteCandidateRuntime,
	selectedMetrics profile.RouteCandidateRuntime,
) bool {
	if candidate.estimatedCost != selected.estimatedCost {
		return candidate.estimatedCost < selected.estimatedCost
	}
	if candidateMetrics.QualityScoreBPS != selectedMetrics.QualityScoreBPS {
		return candidateMetrics.QualityScoreBPS > selectedMetrics.QualityScoreBPS
	}
	return candidate.model < selected.model
}

func candidatesForModel(
	candidates []profile.RouteCandidateRuntime,
	model string,
) profile.RouteCandidateRuntime {
	for _, candidate := range candidates {
		if candidate.Model == model {
			return candidate
		}
	}
	return profile.RouteCandidateRuntime{}
}

func estimateCallCost(inputTokens, outputTokens int, model profile.ModelCapability) int64 {
	return addCost(
		priceTokens(inputTokens, model.InputPriceMicroUSDPerMillion),
		priceTokens(outputTokens, model.OutputPriceMicroUSDPerMillion),
	)
}

func estimateHardCallCost(inputTokens, outputTokens int, model profile.ModelCapability) int64 {
	inputPrice := model.InputPriceMicroUSDPerMillion
	if model.HasCacheWritePrice && model.CacheWritePriceMicroUSDPerMillion > inputPrice {
		inputPrice = model.CacheWritePriceMicroUSDPerMillion
	}
	return addCost(
		priceTokens(inputTokens, inputPrice),
		priceTokens(outputTokens, model.OutputPriceMicroUSDPerMillion),
	)
}

func estimateExpectedCallCost(
	inputTokens int,
	outputTokens int,
	model profile.ModelCapability,
	metrics CacheMetrics,
	hot bool,
) int64 {
	if inputTokens <= 0 || metrics.Samples <= 0 ||
		!model.HasCacheReadPrice || !model.HasCacheWritePrice {
		return estimateCallCost(inputTokens, outputTokens, model)
	}
	total := addMetricTokens(metrics.UncachedInputTokens, metrics.CacheReadTokens)
	total = addMetricTokens(total, metrics.CacheWriteTokens)
	if total <= 0 || total == math.MaxInt64 {
		return estimateCallCost(inputTokens, outputTokens, model)
	}
	readTokens := 0
	writeTokens := 0
	if hot {
		readTokens = proportionalTokens(inputTokens, metrics.CacheReadTokens, total)
		writeTokens = proportionalTokens(inputTokens, metrics.CacheWriteTokens, total)
	} else {
		cacheable := addMetricTokens(metrics.CacheReadTokens, metrics.CacheWriteTokens)
		writeTokens = proportionalTokens(inputTokens, cacheable, total)
	}
	if readTokens < 0 || writeTokens < 0 || readTokens > inputTokens ||
		writeTokens > inputTokens-readTokens {
		return estimateCallCost(inputTokens, outputTokens, model)
	}
	uncachedTokens := inputTokens - readTokens - writeTokens
	return addCost(
		addCost(
			priceTokens(uncachedTokens, model.InputPriceMicroUSDPerMillion),
			priceTokens(readTokens, model.CacheReadPriceMicroUSDPerMillion),
		),
		addCost(
			priceTokens(writeTokens, model.CacheWritePriceMicroUSDPerMillion),
			priceTokens(outputTokens, model.OutputPriceMicroUSDPerMillion),
		),
	)
}

func proportionalTokens(tokens int, part int64, total int64) int {
	if tokens <= 0 || part <= 0 || total <= 0 {
		return 0
	}
	if int64(tokens) > math.MaxInt64/part {
		return tokens
	}
	return int(int64(tokens) * part / total)
}

func addMetricTokens(left, right int64) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func priceTokens(tokens int, price int64) int64 {
	if tokens <= 0 || price <= 0 {
		return 0
	}
	if int64(tokens) > math.MaxInt64/price {
		return math.MaxInt64
	}
	product := int64(tokens) * price
	return product/1_000_000 + boolInt64(product%1_000_000 != 0)
}

func addCost(left, right int64) int64 {
	if left == math.MaxInt64 || right == math.MaxInt64 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func multiplyCost(cost int64, count int) int64 {
	if cost == 0 || count <= 0 {
		return 0
	}
	if cost == math.MaxInt64 || int64(count) > math.MaxInt64/cost {
		return math.MaxInt64
	}
	return cost * int64(count)
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func cloneModels(models profile.ModelCatalog) profile.ModelCatalog {
	cloned := make(profile.ModelCatalog, len(models))
	for id, model := range models {
		cloned[id] = model
	}
	return cloned
}

func buildTargetPlans(runtime profile.Runtime) map[string][]TargetPlan {
	targets := make(map[string][]TargetPlan, len(runtime.Models))
	for model := range runtime.Models {
		for _, target := range runtime.RoutingTargets(model) {
			targets[model] = append(targets[model], TargetPlan{
				id: target.ID, upstream: target.Upstream, protocol: runtime.Protocol,
			})
		}
	}
	return targets
}

func cloneTargetPlans(targets []TargetPlan) []TargetPlan {
	return append([]TargetPlan(nil), targets...)
}

func intersectTargetPlans(answer []TargetPlan, vision []TargetPlan) []TargetPlan {
	visionTargets := make(map[string]struct{}, len(vision))
	for _, target := range vision {
		visionTargets[target.id] = struct{}{}
	}
	compatible := make([]TargetPlan, 0, len(answer))
	for _, target := range answer {
		if _, ok := visionTargets[target.id]; ok {
			compatible = append(compatible, target)
		}
	}
	return compatible
}

func visionTransportSupportsSources(
	transport profile.VisionTransport,
	sources []llmrequest.ImageSourceKind,
) bool {
	for _, source := range sources {
		switch transport {
		case profile.VisionTransportAnthropicMessages:
			if source != llmrequest.ImageSourceBase64 &&
				source != llmrequest.ImageSourceURL &&
				source != llmrequest.ImageSourceFileID {
				return false
			}
		case profile.VisionTransportOpenAIResponses:
			if source != llmrequest.ImageSourceURL &&
				source != llmrequest.ImageSourceDataURL &&
				source != llmrequest.ImageSourceFileID {
				return false
			}
		case profile.VisionTransportOpenAIChatCompletions:
			if source != llmrequest.ImageSourceURL && source != llmrequest.ImageSourceDataURL {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cloneAutoRouting(auto profile.AutoRoutingRuntime) profile.AutoRoutingRuntime {
	auto.Participants = append([]string(nil), auto.Participants...)
	auto.RiskPolicy.SensitiveTextPatterns = append([]string(nil), auto.RiskPolicy.SensitiveTextPatterns...)
	auto.RiskPolicy.SensitiveToolPatterns = append([]string(nil), auto.RiskPolicy.SensitiveToolPatterns...)
	auto.Strategy = cloneStrategy(auto.Strategy)
	return auto
}

func cloneStrategy(strategy profile.RoutingStrategyRuntime) profile.RoutingStrategyRuntime {
	cloned := strategy
	cloned.TaskRoutes = make(map[profile.TaskRouteKey]string, len(strategy.TaskRoutes))
	for key, route := range strategy.TaskRoutes {
		cloned.TaskRoutes[key] = route
	}
	cloned.Routes = make(map[string]profile.RouteRuntime, len(strategy.Routes))
	for id, route := range strategy.Routes {
		route.Candidates = append([]profile.RouteCandidateRuntime(nil), route.Candidates...)
		cloned.Routes[id] = route
	}
	return cloned
}

func (p ExecutionPlan) Strategy() string                     { return p.strategyName }
func (p ExecutionPlan) Route() string                        { return p.routeID }
func (p ExecutionPlan) Model() string                        { return p.model }
func (p ExecutionPlan) VisionMode() VisionMode               { return p.visionMode }
func (p ExecutionPlan) UsesStrongBaseline() bool             { return p.usesStrongBaseline }
func (p ExecutionPlan) EstimatedCostMicroUSD() int64         { return p.estimatedCostMicroUSD }
func (p ExecutionPlan) WorstCaseCostMicroUSD() int64         { return p.worstCaseCostMicroUSD }
func (p ExecutionPlan) AnswerCallCostMicroUSD() int64        { return p.answerCallCostMicroUSD }
func (p ExecutionPlan) VisionCallCostMicroUSD() int64        { return p.visionCallCostMicroUSD }
func (p ExecutionPlan) Budget() profile.AttemptBudgetRuntime { return p.budget }
func (p ExecutionPlan) Reason() string                       { return p.reason }

func (p ExecutionPlan) ModelAttempts() []ModelAttemptPlan {
	attempts := append([]ModelAttemptPlan(nil), p.modelAttempts...)
	for index := range attempts {
		attempts[index].targets = cloneTargetPlans(attempts[index].targets)
	}
	return attempts
}

func (p ExecutionPlan) CandidateDecisions() []CandidateDecision {
	decisions := append([]CandidateDecision(nil), p.candidateDecisions...)
	for index := range decisions {
		decisions[index].UpstreamNodeIDs = append(
			[]string(nil), decisions[index].UpstreamNodeIDs...,
		)
	}
	return decisions
}

func (p ExecutionPlan) NextStrongerModelAttempt(current int) (int, bool) {
	return NextStrongerModelAttempt(p.modelAttempts, current)
}

func NextStrongerModelAttempt(attempts []ModelAttemptPlan, current int) (int, bool) {
	if current < 0 || current >= len(attempts) {
		return 0, false
	}
	quality := attempts[current].qualityScoreBPS
	for index := current + 1; index < len(attempts); index++ {
		if attempts[index].qualityScoreBPS > quality {
			return index, true
		}
	}
	return 0, false
}

func (p ModelAttemptPlan) Model() string                 { return p.model }
func (p ModelAttemptPlan) Targets() []TargetPlan         { return cloneTargetPlans(p.targets) }
func (p ModelAttemptPlan) VisionMode() VisionMode        { return p.visionMode }
func (p ModelAttemptPlan) QualityScoreBPS() int          { return p.qualityScoreBPS }
func (p ModelAttemptPlan) ExpectedCostMicroUSD() int64   { return p.expectedCostMicroUSD }
func (p ModelAttemptPlan) AnswerCallCostMicroUSD() int64 { return p.answerCallCostMicroUSD }
func (p ModelAttemptPlan) VisionCallCostMicroUSD() int64 { return p.visionCallCostMicroUSD }

func (p ModelAttemptPlan) Snapshot() ModelAttemptSnapshot {
	targetIDs := make([]string, 0, len(p.targets))
	for _, target := range p.targets {
		targetIDs = append(targetIDs, target.id)
	}
	return ModelAttemptSnapshot{
		Model:                  p.model,
		TargetIDs:              targetIDs,
		VisionMode:             p.visionMode,
		QualityScoreBPS:        p.qualityScoreBPS,
		ExpectedCostMicroUSD:   p.expectedCostMicroUSD,
		AnswerCallCostMicroUSD: p.answerCallCostMicroUSD,
		VisionCallCostMicroUSD: p.visionCallCostMicroUSD,
	}
}

func (p TargetPlan) ID() string                 { return p.id }
func (p TargetPlan) Upstream() string           { return p.upstream }
func (p TargetPlan) Protocol() profile.Protocol { return p.protocol }

func (p EvaluationPair) Snapshot() EvaluationPairSnapshot {
	return EvaluationPairSnapshot{
		Candidate: p.Candidate.Snapshot(),
		Reference: p.Reference.Snapshot(),
	}
}

func (p ExecutionPlan) Snapshot() ExecutionPlanSnapshot {
	attempts := make([]ModelAttemptSnapshot, 0, len(p.modelAttempts))
	for _, attempt := range p.modelAttempts {
		attempts = append(attempts, attempt.Snapshot())
	}
	return ExecutionPlanSnapshot{
		StrategyName:           p.strategyName,
		RouteID:                p.routeID,
		Model:                  p.model,
		VisionMode:             p.visionMode,
		UsesStrongBaseline:     p.usesStrongBaseline,
		EstimatedCostMicroUSD:  p.estimatedCostMicroUSD,
		WorstCaseCostMicroUSD:  p.worstCaseCostMicroUSD,
		AnswerCallCostMicroUSD: p.answerCallCostMicroUSD,
		VisionCallCostMicroUSD: p.visionCallCostMicroUSD,
		Reason:                 p.reason,
		ModelAttempts:          attempts,
	}
}
