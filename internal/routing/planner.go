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
	RiskNormal Risk = "normal"
	RiskHigh   Risk = "high"
)

type ClassificationSource string

const (
	ClassificationSourceRule     ClassificationSource = "rule"
	ClassificationSourceAnalyzer ClassificationSource = "analyzer"
	ClassificationSourceFallback ClassificationSource = "fallback"
)

type Classification struct {
	TaskType      string
	Risk          Risk
	ConfidenceBPS int
	Source        ClassificationSource
}

type SessionPreference struct {
	Model              string
	MinQualityScoreBPS int
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
	callGraph              CallGraphSnapshot
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
	answerCallCostMicroUSD int64
	visionCallCostMicroUSD int64
}

type ModelAttemptSnapshot struct {
	Model                  string
	TargetIDs              []string
	VisionMode             VisionMode
	QualityScoreBPS        int
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

	if classification.Risk == RiskHigh {
		plan, err := p.planStrongBaseline(request, routeID, "high risk")
		return p.finalizePlan(request, plan, budget, err)
	}

	candidates := append([]profile.RouteCandidateRuntime(nil), route.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Model < candidates[j].Model
	})

	ranked := make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.QualityScoreBPS < route.MinQualityBPS ||
			candidate.SevereErrorRateBPS > route.MaxSevereErrorRateBPS ||
			candidate.QualityScoreBPS < preference.MinQualityScoreBPS {
			continue
		}
		planned, ok := p.evaluateCandidate(request, candidate.Model)
		if !ok {
			continue
		}
		planned.qualityScoreBPS = candidate.QualityScoreBPS
		ranked = append(ranked, rankedCandidate{plan: planned, metrics: candidate})
	}
	if len(ranked) == 0 {
		plan, err := p.planStrongBaseline(
			request,
			routeID,
			"no route candidate passed all gates",
		)
		return p.finalizePlan(request, plan, budget, err)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return betterCandidate(ranked[i].plan, ranked[j].plan, ranked[i].metrics, ranked[j].metrics)
	})
	reason := "lowest complete cost"
	if preference.Model != "" {
		for index := range ranked {
			if ranked[index].plan.model == preference.Model {
				ranked[0], ranked[index] = ranked[index], ranked[0]
				reason = "Session binding"
				break
			}
		}
	}
	attempts := p.plannedAttempts(request, route, ranked)
	plan := p.executionPlan(
		routeID,
		attempts,
		attempts[0].model == p.auto.StrongBaselineModel,
		reason,
	)
	return p.finalizePlan(request, plan, budget, nil)
}

func (p *Planner) RouteID(classification Classification) string {
	if mapped, ok := p.strategy.TaskRoutes[classification.TaskType]; ok {
		return mapped
	}
	return p.strategy.DefaultRoute
}

func (p *Planner) EvaluationPair(
	request Request,
	classification Classification,
	selectedModel string,
) (EvaluationPair, bool) {
	if classification.Risk == RiskHigh {
		return EvaluationPair{}, false
	}
	route := p.strategy.Routes[p.RouteID(classification)]
	reference, ok := p.evaluateCandidate(request, p.auto.StrongBaselineModel)
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
		candidate, ok = p.evaluateCandidate(request, selectedModel)
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
			planned, capable := p.evaluateCandidate(request, metrics.Model)
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
	if candidate.answerCallCost >= reference.answerCallCost {
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
) (ExecutionPlan, error) {
	candidate, ok := p.evaluateCandidate(request, p.auto.StrongBaselineModel)
	if !ok {
		return ExecutionPlan{}, fmt.Errorf("%w: strong baseline %q", ErrNoCapableModel, p.auto.StrongBaselineModel)
	}
	candidate.qualityScoreBPS = 10_000
	return p.executionPlan(routeID, []candidatePlan{candidate}, true, reason), nil
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
) []candidatePlan {
	capacity := min(
		len(ranked)+1,
		p.strategy.Budget.MaxModelSwitches+1,
	)
	attempts := []candidatePlan{ranked[0].plan}
	if capacity <= 1 {
		return attempts
	}

	baseline, baselineOK := p.evaluateCandidate(request, p.auto.StrongBaselineModel)
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

func modelAttemptPlan(candidate candidatePlan) ModelAttemptPlan {
	return ModelAttemptPlan{
		model:                  candidate.model,
		targets:                cloneTargetPlans(candidate.targets),
		visionMode:             candidate.visionMode,
		qualityScoreBPS:        candidate.qualityScoreBPS,
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
) (candidatePlan, bool) {
	model, ok := p.models[modelID]
	if !ok || !model.HasContextWindow || !model.HasMaxOutputTokens ||
		!model.HasInputPrice || !model.HasOutputPrice {
		return candidatePlan{}, false
	}
	targets := p.targets[modelID]
	if len(targets) == 0 {
		return candidatePlan{}, false
	}
	outputTokens := request.Facts.RequestedOutputTokens
	if outputTokens == 0 {
		outputTokens = min(4096, model.MaxOutputTokens)
	}
	if outputTokens <= 0 || outputTokens > model.MaxOutputTokens {
		return candidatePlan{}, false
	}
	if request.Facts.HasTools && (!model.HasSupportsTools || !model.SupportsTools) {
		return candidatePlan{}, false
	}
	if request.Facts.RequiresStructuredOutput &&
		(!model.HasSupportsStructuredOutput || !model.SupportsStructuredOutput) {
		return candidatePlan{}, false
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
				return candidatePlan{}, false
			}
			if !visionTransportSupportsSources(p.vision.Transport, request.Facts.ImageSources) {
				return candidatePlan{}, false
			}
			targets = intersectTargetPlans(targets, p.targets[p.vision.Model])
			if len(targets) == 0 {
				return candidatePlan{}, false
			}
			visionMode = VisionComposite
			visionCallCost = estimateCallCost(
				profile.VisionImagePromptReserveTokens,
				p.vision.MaxTokens,
				visionModel,
			)
			visionCost = multiplyCost(visionCallCost, request.Facts.ImageCount)
		default:
			return candidatePlan{}, false
		}
	}

	answerInputTokens := request.Facts.EstimatedInputTokens
	if visionMode == VisionComposite {
		var fits bool
		answerInputTokens, fits = compositeAnswerInputTokens(request.Facts, p.vision)
		if !fits {
			return candidatePlan{}, false
		}
	}
	if answerInputTokens > model.ContextWindow-outputTokens {
		return candidatePlan{}, false
	}
	answerCost := estimateCallCost(
		answerInputTokens,
		outputTokens,
		model,
	)
	auxiliaryCost := visionCost
	estimated := addCost(answerCost, auxiliaryCost)
	return candidatePlan{
		model: modelID, targets: cloneTargetPlans(targets), visionMode: visionMode,
		estimatedCost:  estimated,
		answerCallCost: answerCost, visionCallCost: visionCallCost,
	}, true
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
	auto.Strategy = cloneStrategy(auto.Strategy)
	return auto
}

func cloneStrategy(strategy profile.RoutingStrategyRuntime) profile.RoutingStrategyRuntime {
	cloned := strategy
	cloned.TaskRoutes = make(map[string]string, len(strategy.TaskRoutes))
	for taskType, route := range strategy.TaskRoutes {
		cloned.TaskRoutes[taskType] = route
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

func (p ModelAttemptPlan) Model() string                 { return p.model }
func (p ModelAttemptPlan) Targets() []TargetPlan         { return cloneTargetPlans(p.targets) }
func (p ModelAttemptPlan) VisionMode() VisionMode        { return p.visionMode }
func (p ModelAttemptPlan) QualityScoreBPS() int          { return p.qualityScoreBPS }
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
