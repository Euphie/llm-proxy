package routing

import (
	"errors"
	"fmt"
	"math"
	"sort"

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

type VisionMode string

const (
	VisionNone      VisionMode = "none"
	VisionNative    VisionMode = "native"
	VisionComposite VisionMode = "composite"
)

type Planner struct {
	models   profile.ModelCatalog
	vision   profile.VisionRuntime
	auto     profile.AutoRoutingRuntime
	strategy profile.RoutingStrategyRuntime
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
	visionMode             VisionMode
	answerCallCostMicroUSD int64
	visionCallCostMicroUSD int64
}

type ModelAttemptSnapshot struct {
	Model                  string
	VisionMode             VisionMode
	AnswerCallCostMicroUSD int64
	VisionCallCostMicroUSD int64
}

func NewPlanner(runtime profile.Runtime) (*Planner, error) {
	if !runtime.AutoRouting.Enabled {
		return nil, ErrRoutingDisabled
	}
	return &Planner{
		models:   cloneModels(runtime.Models),
		vision:   runtime.Vision,
		auto:     cloneAutoRouting(runtime.AutoRouting),
		strategy: cloneStrategy(runtime.AutoRouting.Strategy),
	}, nil
}

func (p *Planner) Plan(request Request, classification Classification) (ExecutionPlan, error) {
	routeID := p.strategy.DefaultRoute
	if mapped, ok := p.strategy.TaskRoutes[classification.TaskType]; ok {
		routeID = mapped
	}
	route := p.strategy.Routes[routeID]

	if classification.Risk == RiskHigh {
		return p.planStrongBaseline(request, routeID, "high risk", classification.Source)
	}

	candidates := append([]profile.RouteCandidateRuntime(nil), route.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Model < candidates[j].Model
	})

	ranked := make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.QualityScoreBPS < route.MinQualityBPS ||
			candidate.SevereErrorRateBPS > route.MaxSevereErrorRateBPS {
			continue
		}
		planned, ok := p.evaluateCandidate(request, candidate.Model, classification.Source)
		if !ok {
			continue
		}
		ranked = append(ranked, rankedCandidate{plan: planned, metrics: candidate})
	}
	if len(ranked) == 0 {
		return p.planStrongBaseline(
			request,
			routeID,
			"no route candidate passed all gates",
			classification.Source,
		)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return betterCandidate(ranked[i].plan, ranked[j].plan, ranked[i].metrics, ranked[j].metrics)
	})
	attempts := p.plannedAttempts(request, classification.Source, ranked)
	return p.executionPlan(
		routeID,
		attempts,
		attempts[0].model == p.auto.StrongBaselineModel,
		"lowest complete cost",
	), nil
}

func (p *Planner) planStrongBaseline(
	request Request,
	routeID string,
	reason string,
	classificationSource ClassificationSource,
) (ExecutionPlan, error) {
	candidate, ok := p.evaluateCandidate(request, p.auto.StrongBaselineModel, classificationSource)
	if !ok {
		return ExecutionPlan{}, fmt.Errorf("%w: strong baseline %q", ErrNoCapableModel, p.auto.StrongBaselineModel)
	}
	return p.executionPlan(routeID, []candidatePlan{candidate}, true, reason), nil
}

func (p *Planner) executionPlan(
	routeID string,
	attempts []candidatePlan,
	baseline bool,
	reason string,
) ExecutionPlan {
	candidate := attempts[0]
	maxAnswerCost := int64(0)
	maxAuxiliaryCost := int64(0)
	modelAttempts := make([]ModelAttemptPlan, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.answerCallCost > maxAnswerCost {
			maxAnswerCost = attempt.answerCallCost
		}
		auxiliaryCost := attempt.estimatedCost - attempt.answerCallCost
		if auxiliaryCost > maxAuxiliaryCost {
			maxAuxiliaryCost = auxiliaryCost
		}
		modelAttempts = append(modelAttempts, modelAttemptPlan(attempt))
	}
	worstCaseCost := addCost(
		multiplyCost(maxAnswerCost, p.strategy.Budget.MaxAnswerAttempts),
		maxAuxiliaryCost,
	)
	return ExecutionPlan{
		strategyName:           p.strategy.Name,
		routeID:                routeID,
		model:                  candidate.model,
		visionMode:             candidate.visionMode,
		usesStrongBaseline:     baseline,
		estimatedCostMicroUSD:  candidate.estimatedCost,
		worstCaseCostMicroUSD:  worstCaseCost,
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
	classificationSource ClassificationSource,
	ranked []rankedCandidate,
) []candidatePlan {
	capacity := min(
		len(ranked)+1,
		min(p.strategy.Budget.MaxModelSwitches+1, p.strategy.Budget.MaxAnswerAttempts),
	)
	attempts := []candidatePlan{ranked[0].plan}
	if capacity <= 1 {
		return attempts
	}

	baseline, baselineOK := p.evaluateCandidate(
		request,
		p.auto.StrongBaselineModel,
		classificationSource,
	)
	baselineAdded := baselineOK && baseline.model != attempts[0].model &&
		p.canAddAttempt(attempts, baseline)
	if baselineAdded {
		attempts = append(attempts, baseline)
	}
	for _, candidate := range ranked[1:] {
		if len(attempts) >= capacity {
			break
		}
		if candidate.plan.model == p.auto.StrongBaselineModel ||
			!p.canAddAttempt(attempts, candidate.plan) {
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

func (p *Planner) canAddAttempt(attempts []candidatePlan, candidate candidatePlan) bool {
	maxAnswerCost := candidate.answerCallCost
	maxAuxiliaryCost := candidate.estimatedCost - candidate.answerCallCost
	for _, attempt := range attempts {
		maxAnswerCost = max(maxAnswerCost, attempt.answerCallCost)
		maxAuxiliaryCost = max(maxAuxiliaryCost, attempt.estimatedCost-attempt.answerCallCost)
	}
	worst := addCost(
		multiplyCost(maxAnswerCost, p.strategy.Budget.MaxAnswerAttempts),
		maxAuxiliaryCost,
	)
	return worst <= p.strategy.Budget.MaxWorstCaseCostMicroUSD
}

func modelAttemptPlan(candidate candidatePlan) ModelAttemptPlan {
	return ModelAttemptPlan{
		model:                  candidate.model,
		visionMode:             candidate.visionMode,
		answerCallCostMicroUSD: candidate.answerCallCost,
		visionCallCostMicroUSD: candidate.visionCallCost,
	}
}

type candidatePlan struct {
	model          string
	visionMode     VisionMode
	estimatedCost  int64
	worstCaseCost  int64
	answerCallCost int64
	visionCallCost int64
}

func (p *Planner) evaluateCandidate(
	request Request,
	modelID string,
	classificationSource ClassificationSource,
) (candidatePlan, bool) {
	model, ok := p.models[modelID]
	if !ok || !model.HasContextWindow || !model.HasMaxOutputTokens ||
		!model.HasInputPrice || !model.HasOutputPrice {
		return candidatePlan{}, false
	}
	outputTokens := request.Facts.RequestedOutputTokens
	if outputTokens == 0 {
		outputTokens = min(4096, model.MaxOutputTokens)
	}
	if outputTokens <= 0 || outputTokens > model.MaxOutputTokens ||
		request.Facts.EstimatedInputTokens > model.ContextWindow-outputTokens {
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
			visionMode = VisionComposite
			visionCallCost = estimateCallCost(4096, p.vision.MaxTokens, visionModel)
			visionCost = multiplyCost(visionCallCost, request.Facts.ImageCount)
		default:
			return candidatePlan{}, false
		}
	}

	answerCost := estimateCallCost(
		request.Facts.EstimatedInputTokens,
		outputTokens,
		model,
	)
	auxiliaryCost := visionCost
	if classificationSource == ClassificationSourceAnalyzer ||
		classificationSource == ClassificationSourceFallback {
		analyzer := p.models[p.auto.TaskAnalyzerModel]
		auxiliaryCost = addCost(auxiliaryCost, estimateCallCost(
			request.Facts.EstimatedInputTokens,
			256,
			analyzer,
		))
	}
	estimated := addCost(answerCost, auxiliaryCost)
	worst := addCost(multiplyCost(answerCost, p.strategy.Budget.MaxAnswerAttempts), auxiliaryCost)
	if worst > p.strategy.Budget.MaxWorstCaseCostMicroUSD {
		return candidatePlan{}, false
	}
	return candidatePlan{
		model: modelID, visionMode: visionMode,
		estimatedCost: estimated, worstCaseCost: worst,
		answerCallCost: answerCost, visionCallCost: visionCallCost,
	}, true
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

func (p ExecutionPlan) ModelAttempts() []ModelAttemptPlan {
	return append([]ModelAttemptPlan(nil), p.modelAttempts...)
}

func (p ModelAttemptPlan) Model() string                 { return p.model }
func (p ModelAttemptPlan) VisionMode() VisionMode        { return p.visionMode }
func (p ModelAttemptPlan) AnswerCallCostMicroUSD() int64 { return p.answerCallCostMicroUSD }
func (p ModelAttemptPlan) VisionCallCostMicroUSD() int64 { return p.visionCallCostMicroUSD }

func (p ModelAttemptPlan) Snapshot() ModelAttemptSnapshot {
	return ModelAttemptSnapshot{
		Model:                  p.model,
		VisionMode:             p.visionMode,
		AnswerCallCostMicroUSD: p.answerCallCostMicroUSD,
		VisionCallCostMicroUSD: p.visionCallCostMicroUSD,
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
