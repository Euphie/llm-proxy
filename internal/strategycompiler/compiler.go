package strategycompiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
	"github.com/Euphie/llm-proxy/internal/profile"
)

var standardizedDomains = []string{
	"general", "simple", "reasoning", "math", "coding", "tool_use", "vision",
}

var generatedDifficulties = []string{"easy", "medium"}

const (
	generatedMinQualityBPS     = 8_000
	generatedMinStabilityBPS   = 8_000
	generatedMaxSevereErrorBPS = 500
	provisionalStabilityBPS    = 8_000
	provisionalSevereErrorBPS  = 500
)

type routeSegment struct {
	domain     string
	difficulty string
}

type Compiler struct {
	models      ModelCatalogProvider
	evaluations EvaluationCatalogProvider
	local       LocalEvidenceProvider
	now         func() time.Time
}

func New(
	models ModelCatalogProvider,
	evaluations EvaluationCatalogProvider,
	local LocalEvidenceProvider,
	now func() time.Time,
) *Compiler {
	if now == nil {
		now = time.Now
	}
	return &Compiler{models: models, evaluations: evaluations, local: local, now: now}
}

func (compiler *Compiler) Compile(
	ctx context.Context,
	record profile.Record,
	intent Intent,
) (Result, error) {
	intent, err := validateIntent(intent)
	if err != nil {
		return Result{}, err
	}
	if compiler.models == nil || compiler.evaluations == nil {
		return Result{}, errorsNew("model and evaluation catalogs are required")
	}
	models := compiler.models.Current()
	evaluations := compiler.evaluations.Current()
	record.Config.Models = append([]profile.ModelCapabilityConfig(nil), record.Config.Models...)
	for index := range record.Config.Models {
		model := &record.Config.Models[index]
		if model.CanonicalModelID != "" {
			continue
		}
		if canonicalID, found := evalcatalog.ResolveCanonicalModel(models, model.ID); found {
			model.CanonicalModelID = canonicalID
		}
	}
	roles, roleExplanations, err := recommendRoles(record, intent, evaluations, compiler.now())
	if err != nil {
		return Result{}, err
	}
	weights := objectiveWeights(intent.Objective)
	segmentCandidates := make(map[routeSegment][]scoredCandidate, len(standardizedDomains)*len(generatedDifficulties))
	explanations := append([]Explanation(nil), roleExplanations...)
	confidence := Confidence{}
	for _, domain := range standardizedDomains {
		for _, difficulty := range generatedDifficulties {
			candidates, candidateExplanations, counts, compileErr := compiler.compileDomain(
				ctx, record, evaluations, roles, domain, difficulty, weights,
			)
			if compileErr != nil {
				return Result{}, compileErr
			}
			segmentCandidates[routeSegment{domain: domain, difficulty: difficulty}] = candidates
			explanations = append(explanations, candidateExplanations...)
			confidence.LocalCandidates += counts.LocalCandidates
			confidence.ExternalCandidates += counts.ExternalCandidates
			confidence.ProvisionalCandidates += counts.ProvisionalCandidates
		}
	}
	routes, taskRoutes := buildRoutes(segmentCandidates, weights, roles.StrongBaselineModel)
	minNetSavingsBPS := generatedNetSavingsGuard(routes, roles.StrongBaselineModel, record.Config.Models)
	if minNetSavingsBPS == 0 {
		explanations = append(explanations, Explanation{
			Code:    "net_savings_guard_disabled",
			Message: "当前价格配置下没有候选能稳定满足 10% 净节省门槛，已关闭该门槛，避免普通 Route 永远不可达。",
		})
	}
	budget := record.Config.AutoRouting.Strategy.Budget
	if !record.Config.AutoRouting.Enabled || budget.Deadline == "" {
		budget = defaultBudget()
	}
	if intent.MaxCostPerRequestMicroUSD != nil {
		budget.MaxWorstCaseCostMicroUSD = *intent.MaxCostPerRequestMicroUSD
	}
	explanations = append(explanations, Explanation{
		Code:    "high_risk_baseline_guard",
		Message: fmt.Sprintf("高风险请求继续由运行时强制回退到基线模型 %s。", roles.StrongBaselineModel),
		Model:   roles.StrongBaselineModel,
	})
	confidence.Level = confidenceLevel(confidence)
	latencyTargetMS := int64(0)
	if intent.LatencyTargetMS != nil {
		latencyTargetMS = int64(*intent.LatencyTargetMS)
	}
	return Result{
		Config: profile.RoutingStrategyConfig{
			Name:  compiler.now().UTC().Format("20060102") + "-001",
			Alias: objectiveAlias(intent.Objective), DefaultRoute: "general",
			Roles: &profile.RoutingStrategyRolesConfig{
				Participants:        append([]string(nil), roles.Participants...),
				StrongBaselineModel: roles.StrongBaselineModel,
				TaskAnalyzerModel:   roles.TaskAnalyzerModel,
				ReviewerModel:       roles.ReviewerModel,
			},
			MinNetSavingsBPS: minNetSavingsBPS, LatencyTargetMS: latencyTargetMS,
			TaskRoutes: taskRoutes, Routes: routes, Budget: budget,
		},
		Roles: roles, Explanations: explanations,
		SourceDigest: sourceDigest(models.Source.Revision, evaluations.Revision),
		Confidence:   confidence,
	}, nil
}

func validateIntent(intent Intent) (Intent, error) {
	if intent.Objective == "" {
		intent.Objective = ObjectiveBalanced
	}
	switch intent.Objective {
	case ObjectiveBalanced, ObjectiveQuality, ObjectiveCost, ObjectiveLatency:
	default:
		return Intent{}, fmt.Errorf("%w: unsupported objective %q", ErrInvalidIntent, intent.Objective)
	}
	if intent.MaxCostPerRequestMicroUSD != nil && *intent.MaxCostPerRequestMicroUSD < 0 {
		return Intent{}, fmt.Errorf("%w: max cost cannot be negative", ErrInvalidIntent)
	}
	if intent.LatencyTargetMS != nil && *intent.LatencyTargetMS <= 0 {
		return Intent{}, fmt.Errorf("%w: latency target must be positive", ErrInvalidIntent)
	}
	if intent.DailyEvalBudgetMicroUSD != nil && *intent.DailyEvalBudgetMicroUSD < 0 {
		return Intent{}, fmt.Errorf("%w: daily evaluation budget cannot be negative", ErrInvalidIntent)
	}
	return intent, nil
}

func recommendRoles(
	record profile.Record,
	intent Intent,
	evaluations evalcatalog.Catalog,
	now time.Time,
) (RoleRecommendation, []Explanation, error) {
	configuredModels := make(map[string]profile.ModelCapabilityConfig, len(record.Config.Models))
	for _, model := range record.Config.Models {
		configuredModels[model.ID] = model
	}
	participants := append([]string(nil), intent.Participants...)
	if len(participants) == 0 {
		for _, model := range record.Config.Models {
			participants = append(participants, model.ID)
		}
	}
	if len(participants) < 2 {
		return RoleRecommendation{}, nil, ErrInsufficientParticipants
	}
	seen := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		if _, found := configuredModels[participant]; !found {
			return RoleRecommendation{}, nil, fmt.Errorf("%w: unknown participant %q", ErrInvalidIntent, participant)
		}
		if _, duplicate := seen[participant]; duplicate {
			return RoleRecommendation{}, nil, fmt.Errorf("%w: duplicate participant %q", ErrInvalidIntent, participant)
		}
		seen[participant] = struct{}{}
	}
	baseline, evidenceBacked := evidenceBackedBaseline(participants, configuredModels, evaluations, now)
	if !evidenceBacked {
		baseline = capabilityBackedFallback(participants, configuredModels)
	}
	analyzer := cheapestToolModel(participants, configuredModels)
	if analyzer == "" {
		analyzer = baseline
	}
	roles := RoleRecommendation{
		Apply: true, Participants: participants, StrongBaselineModel: baseline,
		TaskAnalyzerModel: analyzer, ReviewerModel: baseline,
	}
	if intent.DailyEvalBudgetMicroUSD != nil {
		roles.DailyEvalBudgetMicroUSD = *intent.DailyEvalBudgetMicroUSD
	}
	explanation := Explanation{
		Code:    "role_recommendation_evidence",
		Message: "已根据精确模型身份的可比较公开评测推荐强基线；初始化角色仍需人工确认后保存。",
		Model:   baseline,
	}
	if !evidenceBacked {
		explanation.Code = "role_recommendation_provisional"
		explanation.Message = "缺少可比较的质量证据，暂从能力条件相同的模型中选择价格较高者作为保守兜底；这不是质量结论，必须用本地评测校准。"
	}
	return roles, []Explanation{explanation}, nil
}

type baselineEvidenceScore struct {
	model       string
	coverage    int
	comparisons int
	lowerTotal  int64
	minimum     int
}

func evidenceBackedBaseline(
	participants []string,
	models map[string]profile.ModelCapabilityConfig,
	evaluations evalcatalog.Catalog,
	now time.Time,
) (string, bool) {
	scores := make([]baselineEvidenceScore, 0, len(participants))
	for _, participant := range participants {
		canonical := models[participant].CanonicalModelID
		if canonical == "" {
			continue
		}
		score := baselineEvidenceScore{model: participant, minimum: 10_000}
		coveredDomains := make(map[string]struct{}, len(standardizedDomains))
		for _, comparison := range participants {
			if comparison == participant {
				continue
			}
			otherCanonical := models[comparison].CanonicalModelID
			if otherCanonical == "" || otherCanonical == canonical {
				continue
			}
			for _, domain := range standardizedDomains {
				prior, found := evalcatalog.EstimatePrior(evaluations, evalcatalog.PriorRequest{
					CandidateID: canonical, BaselineID: otherCanonical, Domain: domain,
					ExactDomainOnly: true, Now: now,
				})
				if !found || prior.RankingOnly {
					continue
				}
				score.comparisons++
				score.lowerTotal += int64(prior.LowerBPS)
				score.minimum = min(score.minimum, prior.LowerBPS)
				coveredDomains[domain] = struct{}{}
			}
		}
		if score.comparisons > 0 {
			score.coverage = len(coveredDomains)
			scores = append(scores, score)
		}
	}
	if len(scores) == 0 {
		return "", false
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].coverage != scores[j].coverage {
			return scores[i].coverage > scores[j].coverage
		}
		leftAverage := scores[i].lowerTotal / int64(scores[i].comparisons)
		rightAverage := scores[j].lowerTotal / int64(scores[j].comparisons)
		if leftAverage != rightAverage {
			return leftAverage > rightAverage
		}
		if scores[i].minimum != scores[j].minimum {
			return scores[i].minimum > scores[j].minimum
		}
		if scores[i].comparisons != scores[j].comparisons {
			return scores[i].comparisons > scores[j].comparisons
		}
		return scores[i].model < scores[j].model
	})
	return scores[0].model, true
}

type scoredCandidate struct {
	config    profile.RouteCandidateConfig
	score     int64
	rankBonus int
}

func (compiler *Compiler) compileDomain(
	ctx context.Context,
	record profile.Record,
	evaluations evalcatalog.Catalog,
	roles RoleRecommendation,
	domain string,
	difficulty string,
	weights profile.RoutingWeightsConfig,
) ([]scoredCandidate, []Explanation, Confidence, error) {
	configured := make(map[string]profile.ModelCapabilityConfig, len(record.Config.Models))
	for _, model := range record.Config.Models {
		configured[model.ID] = model
	}
	prices := make(map[string]int64, len(roles.Participants))
	maxPrice := int64(0)
	for _, model := range roles.Participants {
		prices[model] = representativePrice(configured[model])
		maxPrice = max(maxPrice, prices[model])
	}
	baselineConfig, baselineMatched := configured[roles.StrongBaselineModel]
	canonicalBaseline := baselineConfig.CanonicalModelID
	baselineMatched = baselineMatched && canonicalBaseline != ""
	candidates := make([]scoredCandidate, 0, len(roles.Participants))
	explanations := make([]Explanation, 0, len(roles.Participants)*2)
	counts := Confidence{}
	for _, model := range roles.Participants {
		if model == roles.StrongBaselineModel {
			continue
		}
		candidate := profile.RouteCandidateConfig{
			Model: model, QualityScoreBPS: 8_000, StabilityScoreBPS: provisionalStabilityBPS,
			SevereErrorRateBPS: provisionalSevereErrorBPS,
		}
		rankBonus := 0
		used := "provisional"
		if compiler.local != nil {
			local, found, err := compiler.local.CandidateEstimate(ctx, EvidenceKey{
				ProfileID: record.ID, Strategy: record.Config.AutoRouting.Strategy.Name,
				CandidateModel: model, ReferenceModel: roles.StrongBaselineModel,
				Domain: domain, Difficulty: difficulty,
			})
			if err != nil {
				return nil, nil, Confidence{}, err
			}
			if found && local.Reliable {
				candidate.QualityScoreBPS = clampBPS(local.QualityLowerBPS)
				candidate.StabilityScoreBPS = clampBPS(local.StabilityLowerBPS)
				candidate.SevereErrorRateBPS = clampBPS(local.SevereErrorUpperBPS)
				candidate.ExpectedLatencyMS = max(local.ExpectedLatencyMS, 0)
				used = "local"
				counts.LocalCandidates++
				explanations = append(explanations, Explanation{
					Code: "local_evidence", Domain: domain, Model: model,
					Message: fmt.Sprintf("已使用达到可靠门槛的本地在线轨迹和异步评测证据（难度 %s）。", difficulty),
				})
			}
		}
		if used == "provisional" {
			candidateConfig, candidateMatched := configured[model]
			canonicalCandidate := candidateConfig.CanonicalModelID
			candidateMatched = candidateMatched && canonicalCandidate != "" && canonicalCandidate != canonicalBaseline
			if baselineMatched && candidateMatched {
				if prior, found := evalcatalog.EstimatePrior(evaluations, evalcatalog.PriorRequest{
					CandidateID: canonicalCandidate, BaselineID: canonicalBaseline,
					Domain: domain, Now: compiler.now(),
				}); found {
					if prior.RankingOnly {
						rankBonus = prior.RankAdvantage * 100
						explanations = append(explanations, Explanation{
							Code: "ranking_only", Domain: domain, Model: model,
							Message: "公开数据只提供排名，可参与候选排序但不用于建立质量硬门槛。",
						})
					} else {
						candidate.QualityScoreBPS = clampBPS(prior.LowerBPS)
						used = "external"
						counts.ExternalCandidates++
						explanations = append(explanations, Explanation{
							Code: "external_prior", Domain: domain, Model: model,
							Message: fmt.Sprintf("采用公开评测的保守下界作为冷启动质量先验（等效样本 %.1f）。", prior.EffectiveSamples),
						})
					}
				}
			}
			if used == "provisional" {
				counts.ProvisionalCandidates++
				explanations = append(explanations, Explanation{
					Code: "insufficient_data", Domain: domain, Model: model,
					Message: "没有可精确匹配的公开评测或可靠本地证据，使用系统临时质量值。",
				})
			}
			if used != "local" {
				explanations = append(explanations, Explanation{
					Code: "system_provisional", Domain: domain, Model: model,
					Message: "稳定性、严重错误率和延迟尚无可靠本地证据，先使用普通任务冷启动值，后续由本地证据校准。",
				})
			}
		}
		candidate.ProductionEligible = used == "local" && candidateMeetsRouteGates(
			candidate,
			profile.RouteConfig{
				MinQualityBPS: generatedMinQualityBPS, MinStabilityBPS: generatedMinStabilityBPS,
				MaxSevereErrorRateBPS: generatedMaxSevereErrorBPS,
			},
		)
		costBPS := 0
		if maxPrice > 0 {
			costBPS = int((maxPrice - min(prices[model], maxPrice)) * 10_000 / maxPrice)
		}
		performanceBPS := 0
		if candidate.ExpectedLatencyMS > 0 {
			performanceBPS = int(10_000_000 / max(candidate.ExpectedLatencyMS, 1))
			performanceBPS = min(performanceBPS, 10_000)
		}
		score := int64(candidate.QualityScoreBPS)*int64(weights.QualityBPS) +
			int64(candidate.StabilityScoreBPS)*int64(weights.StabilityBPS) +
			int64(costBPS)*int64(weights.CostBPS) +
			int64(performanceBPS)*int64(weights.PerformanceBPS) + int64(rankBonus)*10_000
		candidates = append(candidates, scoredCandidate{config: candidate, score: score, rankBonus: rankBonus})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].config.Model < candidates[j].config.Model
	})
	return candidates, explanations, counts, nil
}

func buildRoutes(
	bySegment map[routeSegment][]scoredCandidate,
	weights profile.RoutingWeightsConfig,
	baseline string,
) ([]profile.RouteConfig, []profile.TaskRouteConfig) {
	routes := []profile.RouteConfig{baselineRoute("strong", baseline, weights)}
	segmentRoutes := make(map[routeSegment]string, len(bySegment))
	sharedRoutes := make(map[string]string)
	for _, domain := range standardizedDomains {
		for _, difficulty := range generatedDifficulties {
			segment := routeSegment{domain: domain, difficulty: difficulty}
			candidates := bySegment[segment]
			key := candidateSetKey(candidates)
			if routeID, found := sharedRoutes[key]; found {
				segmentRoutes[segment] = routeID
				continue
			}
			routeID := domain + "-" + difficulty
			if domain == "general" && difficulty == "easy" {
				routeID = "general"
			}
			routes = append(routes, routeConfig(routeID, candidates, weights))
			sharedRoutes[key] = routeID
			segmentRoutes[segment] = routeID
		}
	}
	taskRoutes := make([]profile.TaskRouteConfig, 0, len(standardizedDomains)*3)
	for _, domain := range standardizedDomains {
		for _, difficulty := range generatedDifficulties {
			segment := routeSegment{domain: domain, difficulty: difficulty}
			route := segmentRoutes[segment]
			if route == "" {
				route = "general"
			}
			taskRoutes = append(taskRoutes, profile.TaskRouteConfig{
				TaskType: domain, Difficulty: difficulty, Route: route,
			})
		}
		taskRoutes = append(taskRoutes, profile.TaskRouteConfig{
			TaskType: domain, Difficulty: "hard", Route: "strong",
		})
	}
	return routes, taskRoutes
}

func candidateSetKey(candidates []scoredCandidate) string {
	var key strings.Builder
	for _, candidate := range candidates {
		fmt.Fprintf(
			&key, "%s:%t:%d:%d:%d:%d;",
			candidate.config.Model,
			candidate.config.ProductionEligible,
			candidate.config.QualityScoreBPS,
			candidate.config.StabilityScoreBPS,
			candidate.config.SevereErrorRateBPS,
			candidate.config.ExpectedLatencyMS,
		)
	}
	return key.String()
}

func routeConfig(
	id string,
	candidates []scoredCandidate,
	weights profile.RoutingWeightsConfig,
) profile.RouteConfig {
	configured := make([]profile.RouteCandidateConfig, 0, len(candidates))
	for _, candidate := range candidates {
		configured = append(configured, candidate.config)
	}
	return profile.RouteConfig{
		ID: id, MinQualityBPS: generatedMinQualityBPS, MinStabilityBPS: generatedMinStabilityBPS,
		MaxSevereErrorRateBPS: generatedMaxSevereErrorBPS, Weights: weights, Candidates: configured,
	}
}

func baselineRoute(id, baseline string, weights profile.RoutingWeightsConfig) profile.RouteConfig {
	return routeConfig(id, []scoredCandidate{{config: profile.RouteCandidateConfig{
		Model: baseline, ProductionEligible: true, QualityScoreBPS: generatedMinQualityBPS,
		StabilityScoreBPS:  generatedMinStabilityBPS,
		SevereErrorRateBPS: generatedMaxSevereErrorBPS,
	}}}, weights)
}

func objectiveWeights(objective Objective) profile.RoutingWeightsConfig {
	switch objective {
	case ObjectiveQuality:
		return profile.RoutingWeightsConfig{QualityBPS: 5_500, StabilityBPS: 2_500, CostBPS: 1_000, PerformanceBPS: 1_000}
	case ObjectiveCost:
		return profile.RoutingWeightsConfig{QualityBPS: 3_000, StabilityBPS: 2_000, CostBPS: 4_000, PerformanceBPS: 1_000}
	case ObjectiveLatency:
		return profile.RoutingWeightsConfig{QualityBPS: 3_000, StabilityBPS: 2_000, CostBPS: 1_000, PerformanceBPS: 4_000}
	default:
		return profile.RoutingWeightsConfig{QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000}
	}
}

func objectiveAlias(objective Objective) string {
	switch objective {
	case ObjectiveQuality:
		return "智能生成 · 质量优先"
	case ObjectiveCost:
		return "智能生成 · 成本优先"
	case ObjectiveLatency:
		return "智能生成 · 延迟优先"
	default:
		return "智能生成 · 均衡"
	}
}

func defaultBudget() profile.AttemptBudgetConfig {
	return profile.AttemptBudgetConfig{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
		MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
		MaxWorstCaseCostMicroUSD: 500_000,
	}
}

func representativePrice(model profile.ModelCapabilityConfig) int64 {
	var price int64
	if model.InputPriceMicroUSDPerMillion != nil {
		price += *model.InputPriceMicroUSDPerMillion
	}
	if model.OutputPriceMicroUSDPerMillion != nil {
		price += *model.OutputPriceMicroUSDPerMillion
	}
	return max(price, 0)
}

func generatedNetSavingsGuard(
	routes []profile.RouteConfig,
	baseline string,
	models []profile.ModelCapabilityConfig,
) int {
	configured := make(map[string]profile.ModelCapabilityConfig, len(models))
	for _, model := range models {
		configured[model.ID] = model
	}
	baselinePrice := representativePrice(configured[baseline])
	if baselinePrice <= 0 {
		return 0
	}
	const guard = 1_000
	for _, route := range routes {
		if route.ID == "strong" {
			continue
		}
		reachable := false
		for _, candidate := range route.Candidates {
			candidatePrice := representativePrice(configured[candidate.Model])
			if candidatePrice*10_000 <= baselinePrice*(10_000-guard) {
				reachable = true
				break
			}
		}
		if !reachable {
			return 0
		}
	}
	return guard
}

func capabilityBackedFallback(participants []string, models map[string]profile.ModelCapabilityConfig) string {
	best := ""
	for _, participant := range participants {
		if best == "" || strongerFallbackModel(models[participant], models[best]) ||
			!strongerFallbackModel(models[best], models[participant]) && participant < best {
			best = participant
		}
	}
	return best
}

func strongerFallbackModel(left, right profile.ModelCapabilityConfig) bool {
	leftFeatures := supportedFeatureCount(left)
	rightFeatures := supportedFeatureCount(right)
	if leftFeatures != rightFeatures {
		return leftFeatures > rightFeatures
	}
	leftPrice, rightPrice := representativePrice(left), representativePrice(right)
	if leftPrice != rightPrice {
		return leftPrice > rightPrice
	}
	leftContext, rightContext := optionalInt(left.ContextWindow), optionalInt(right.ContextWindow)
	if leftContext != rightContext {
		return leftContext > rightContext
	}
	return optionalInt(left.MaxOutputTokens) > optionalInt(right.MaxOutputTokens)
}

func supportedFeatureCount(model profile.ModelCapabilityConfig) int {
	count := 0
	for _, supported := range []*bool{
		model.SupportsVision, model.SupportsTools, model.SupportsStructuredOutput,
	} {
		if supported != nil && *supported {
			count++
		}
	}
	return count
}

func optionalInt(value *int) int {
	if value == nil {
		return 0
	}
	return max(*value, 0)
}

func cheapestToolModel(participants []string, models map[string]profile.ModelCapabilityConfig) string {
	best := ""
	bestPrice := int64(^uint64(0) >> 1)
	for _, participant := range participants {
		model := models[participant]
		if model.SupportsTools == nil || !*model.SupportsTools {
			continue
		}
		price := representativePrice(model)
		if price < bestPrice || (price == bestPrice && (best == "" || participant < best)) {
			best, bestPrice = participant, price
		}
	}
	return best
}

func confidenceLevel(confidence Confidence) string {
	if confidence.LocalCandidates > 0 && confidence.ProvisionalCandidates == 0 {
		return "local"
	}
	if confidence.LocalCandidates > 0 {
		return "mixed"
	}
	if confidence.ExternalCandidates > 0 && confidence.ProvisionalCandidates == 0 {
		return "external"
	}
	if confidence.ExternalCandidates > 0 {
		return "external_with_gaps"
	}
	return "provisional"
}

func sourceDigest(modelRevision, evaluationRevision string) string {
	hash := sha256.New()
	hash.Write([]byte(strings.Join([]string{GeneratorVersion, modelRevision, evaluationRevision}, "\x00")))
	return hex.EncodeToString(hash.Sum(nil))
}

func clampBPS(value int) int { return min(max(value, 0), 10_000) }

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func errorsNew(message string) error { return fmt.Errorf("%w: %s", ErrInvalidIntent, message) }
