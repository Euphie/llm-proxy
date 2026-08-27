package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/strategy"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

type performanceGroupKey struct {
	ProfileID      int64
	Strategy       string
	Route          string
	TaskType       string
	Difficulty     string
	Risk           string
	VisionMode     string
	CandidateModel string
	ReferenceModel string
}

type StrategyOverview struct {
	Snapshot           strategy.Snapshot              `json:"snapshot"`
	Strategies         []strategy.Version             `json:"strategies"`
	Generations        []strategy.Generation          `json:"generations,omitempty"`
	QualityEstimates   []evaluation.QualityEstimate   `json:"quality_estimates,omitempty"`
	StabilityEstimates []evaluation.StabilityEstimate `json:"stability_estimates,omitempty"`
	EvaluationBudget   evaluation.BudgetSnapshot      `json:"evaluation_budget"`
}

type StrategyService struct {
	profiles    *profile.Store
	strategies  *strategy.Store
	coordinator *gateway.Coordinator
	now         func() time.Time
	evidence    *evaluation.Store
	compiler    *strategycompiler.Compiler
	generations *strategy.GenerationStore
	runtimes    *runtimeconfig.Store

	canaryEvidenceMu       sync.Mutex
	canaryEvidenceReliable map[[2]int64]bool
}

func (s *StrategyService) EnableRuntimePolicies(store *runtimeconfig.Store) {
	s.runtimes = store
}

type GeneratedStrategy struct {
	Version        *strategy.Version       `json:"version,omitempty"`
	Generation     *strategy.Generation    `json:"generation,omitempty"`
	Recommendation strategycompiler.Result `json:"recommendation"`
}

func NewStrategyService(
	profiles *profile.Store,
	strategies *strategy.Store,
	coordinator *gateway.Coordinator,
	now func() time.Time,
	evidence ...*evaluation.Store,
) *StrategyService {
	if now == nil {
		now = time.Now
	}
	service := &StrategyService{
		profiles: profiles, strategies: strategies, coordinator: coordinator, now: now,
	}
	if len(evidence) > 0 {
		service.evidence = evidence[0]
	}
	return service
}

func (s *StrategyService) EnableGeneration(
	compiler *strategycompiler.Compiler,
	generations *strategy.GenerationStore,
) {
	s.compiler = compiler
	s.generations = generations
}

func (s *StrategyService) GenerateStrategy(
	ctx context.Context,
	profileID int64,
	intent strategycompiler.Intent,
) (GeneratedStrategy, error) {
	if s.compiler == nil || s.generations == nil {
		return GeneratedStrategy{}, errStrategyGenerationUnavailable
	}
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return GeneratedStrategy{}, err
	}
	recommendation, err := s.compiler.Compile(ctx, record, intent)
	if err != nil {
		return GeneratedStrategy{}, err
	}
	if !record.Config.AutoRouting.Enabled {
		return GeneratedStrategy{Recommendation: recommendation}, nil
	}
	if _, err := s.strategies.Bootstrap(ctx, record); err != nil {
		return GeneratedStrategy{}, err
	}
	name, err := s.strategies.NextName(ctx, profileID, s.now())
	if err != nil {
		return GeneratedStrategy{}, err
	}
	recommendation.Config.Name = name
	draft, generation, err := s.generations.CreateGeneratedDraft(ctx, record, strategy.GenerationInput{
		ProfileID:        profileID,
		GeneratorVersion: strategycompiler.GeneratorVersion, Intent: intent,
		SourceDigest: recommendation.SourceDigest, GeneratedConfig: recommendation.Config,
		Explanations: recommendation.Explanations,
	})
	if err != nil {
		return GeneratedStrategy{}, err
	}
	return GeneratedStrategy{
		Version: &draft, Generation: &generation, Recommendation: recommendation,
	}, nil
}

func (s *StrategyService) RestoreGeneratedRecommendation(
	ctx context.Context,
	profileID, strategyID int64,
	path string,
) (GeneratedStrategy, error) {
	if s.generations == nil {
		return GeneratedStrategy{}, errStrategyGenerationUnavailable
	}
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return GeneratedStrategy{}, err
	}
	version, metadata, err := s.generations.RestoreGeneratedDraft(ctx, record, strategyID, path)
	if err != nil {
		return GeneratedStrategy{}, err
	}
	return GeneratedStrategy{
		Version:    &version,
		Generation: &metadata,
		Recommendation: strategycompiler.Result{
			Config: metadata.GeneratedConfig, SourceDigest: metadata.SourceDigest,
			Explanations: metadata.Explanations,
		},
	}, nil
}

func (s *StrategyService) GenerateCandidate(
	ctx context.Context,
	profileID int64,
) (strategy.Version, error) {
	if s.evidence == nil {
		return strategy.Version{}, evaluation.ErrInsufficientEvidence
	}
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return strategy.Version{}, err
	}
	snapshot, err := s.strategies.Bootstrap(ctx, record)
	if err != nil {
		return strategy.Version{}, err
	}
	rows, err := s.evidence.ListEvidence(ctx, profileID)
	if err != nil {
		return strategy.Version{}, err
	}
	config := cloneStrategyConfig(snapshot.Active.Config)
	updated := 0
	for routeIndex := range config.Routes {
		route := &config.Routes[routeIndex]
		for candidateIndex := range route.Candidates {
			candidate := &route.Candidates[candidateIndex]
			if candidate.Model == record.Config.AutoRouting.StrongBaselineModel {
				continue
			}
			estimate, ok := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
				Now: s.now(), Strategy: snapshot.Active.Config.Name, Route: route.ID,
				CandidateModel:           candidate.Model,
				ReferenceModel:           record.Config.AutoRouting.StrongBaselineModel,
				ConfiguredQualityBPS:     candidate.QualityScoreBPS,
				ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
			})
			if !ok || !estimate.Reliable {
				continue
			}
			online, err := s.evidence.OnlineStability(
				ctx, profileID, snapshot.Active.Config.Name, route.ID, candidate.Model,
			)
			if err != nil {
				return strategy.Version{}, err
			}
			stability, ok := evaluation.EstimateStability(rows, online, evaluation.StabilityEstimateRequest{
				Now: s.now(), Strategy: snapshot.Active.Config.Name, Route: route.ID,
				CandidateModel:           candidate.Model,
				ReferenceModel:           record.Config.AutoRouting.StrongBaselineModel,
				ConfiguredStabilityBPS:   candidate.StabilityScoreBPS,
				ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
			})
			if !ok || !stability.Reliable {
				continue
			}
			candidate.QualityScoreBPS = estimate.QualityLowerBPS
			candidate.StabilityScoreBPS = stability.LowerBPS
			candidate.SevereErrorRateBPS = estimate.SevereErrorUpperBPS
			candidate.ExpectedLatencyMS = stability.ExpectedLatencyMS
			updated++
		}
	}
	if updated == 0 {
		return strategy.Version{}, evaluation.ErrInsufficientEvidence
	}
	config.Name, err = s.strategies.NextName(ctx, profileID, s.now())
	if err != nil {
		return strategy.Version{}, err
	}
	config.Alias = fmt.Sprintf("学习候选 %s", s.now().UTC().Format("2006-01-02"))
	return s.strategies.CreateDraft(ctx, record, config)
}

func cloneStrategyConfig(config profile.RoutingStrategyConfig) profile.RoutingStrategyConfig {
	cloned := config
	if config.Roles != nil {
		roles := *config.Roles
		roles.Participants = append([]string(nil), config.Roles.Participants...)
		cloned.Roles = &roles
	}
	cloned.TaskRoutes = append([]profile.TaskRouteConfig(nil), config.TaskRoutes...)
	cloned.Routes = make([]profile.RouteConfig, len(config.Routes))
	for index, route := range config.Routes {
		route.Candidates = append([]profile.RouteCandidateConfig(nil), route.Candidates...)
		cloned.Routes[index] = route
	}
	return cloned
}

func (s *StrategyService) Overview(ctx context.Context, profileID int64) (StrategyOverview, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return StrategyOverview{}, err
	}
	var snapshot strategy.Snapshot
	if record.Config.AutoRouting.Enabled {
		snapshot, err = s.strategies.Bootstrap(ctx, record)
	} else {
		snapshot, err = s.strategies.Snapshot(ctx, profileID)
		if errors.Is(err, strategy.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return StrategyOverview{}, err
	}
	versions, err := s.strategies.List(ctx, profileID)
	if err != nil {
		return StrategyOverview{}, err
	}
	overview := StrategyOverview{Snapshot: snapshot, Strategies: versions}
	if s.generations != nil {
		overview.Generations, err = s.generations.List(ctx, profileID)
		if err != nil {
			return StrategyOverview{}, err
		}
	}
	if s.evidence == nil || snapshot.Active.ID == 0 {
		return overview, nil
	}
	rows, err := s.evidence.ListEvidence(ctx, profileID)
	if err != nil {
		return StrategyOverview{}, err
	}
	overview.EvaluationBudget, err = s.evidence.Budget(ctx, profileID, s.now())
	if err != nil {
		return StrategyOverview{}, err
	}
	for _, route := range snapshot.Active.Config.Routes {
		for _, candidate := range route.Candidates {
			if candidate.Model == record.Config.AutoRouting.StrongBaselineModel {
				continue
			}
			estimate, ok := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
				Now: s.now(), Strategy: snapshot.Active.Config.Name, Route: route.ID,
				CandidateModel:           candidate.Model,
				ReferenceModel:           record.Config.AutoRouting.StrongBaselineModel,
				ConfiguredQualityBPS:     candidate.QualityScoreBPS,
				ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
			})
			if ok {
				overview.QualityEstimates = append(overview.QualityEstimates, estimate)
			}
			online, err := s.evidence.OnlineStability(
				ctx, profileID, snapshot.Active.Config.Name, route.ID, candidate.Model,
			)
			if err != nil {
				return StrategyOverview{}, err
			}
			stability, ok := evaluation.EstimateStability(rows, online, evaluation.StabilityEstimateRequest{
				Now: s.now(), Strategy: snapshot.Active.Config.Name, Route: route.ID,
				CandidateModel:           candidate.Model,
				ReferenceModel:           record.Config.AutoRouting.StrongBaselineModel,
				ConfiguredStabilityBPS:   candidate.StabilityScoreBPS,
				ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
			})
			if ok {
				overview.StabilityEstimates = append(overview.StabilityEstimates, stability)
			}
		}
	}
	return overview, nil
}

func (s *StrategyService) ModelPerformance(
	ctx context.Context,
	filter evaluation.PerformanceFilter,
) (evaluation.PerformancePage, error) {
	page := max(filter.Page, 1)
	page = min(page, maxStatisticsPage)
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}
	result := evaluation.PerformancePage{
		Items: make([]evaluation.PerformanceItem, 0), Page: page, PageSize: pageSize,
	}
	if s.evidence == nil {
		return result, nil
	}
	records, _, err := s.profiles.LoadSnapshot(ctx)
	if err != nil {
		return evaluation.PerformancePage{}, err
	}
	groups := make(map[performanceGroupKey][]evaluation.EvidenceAggregate)
	recordByID := make(map[int64]profile.Record, len(records))
	strategyByProfile := make(map[int64]map[string]profile.RoutingStrategyConfig, len(records))
	for _, record := range records {
		if filter.ProfileID != nil && record.ID != *filter.ProfileID {
			continue
		}
		recordByID[record.ID] = record
		byName := make(map[string]profile.RoutingStrategyConfig)
		if record.Config.Version == 2 && s.runtimes != nil {
			versions, listErr := s.runtimes.History(ctx, record.ID, 1000)
			if listErr != nil {
				return evaluation.PerformancePage{}, listErr
			}
			for _, version := range versions {
				byName[version.Policy.Name] = version.Policy.RoutingStrategyConfig
			}
		} else {
			versions, listErr := s.strategies.List(ctx, record.ID)
			if listErr != nil {
				return evaluation.PerformancePage{}, listErr
			}
			for _, version := range versions {
				byName[version.Config.Name] = version.Config
			}
		}
		strategyByProfile[record.ID] = byName
		rows, listErr := s.evidence.ListEvidence(ctx, record.ID)
		if listErr != nil {
			return evaluation.PerformancePage{}, listErr
		}
		for _, row := range rows {
			if !matchesPerformanceFilter(row, filter) {
				continue
			}
			key := performanceGroupKey{
				ProfileID: record.ID, Strategy: row.Strategy, Route: row.Route,
				TaskType: row.TaskType, Difficulty: row.Difficulty, Risk: row.Risk,
				VisionMode: row.VisionMode, CandidateModel: row.CandidateModel,
				ReferenceModel: row.ReferenceModel,
			}
			groups[key] = append(groups[key], row)
		}
	}
	items := make([]evaluation.PerformanceItem, 0, len(groups))
	for key, rows := range groups {
		record := recordByID[key.ProfileID]
		config, strategyKnown := strategyByProfile[key.ProfileID][key.Strategy]
		quality, severe, priorKnown := configuredPerformancePrior(
			config, key.Route, key.CandidateModel,
		)
		priorKnown = priorKnown && strategyKnown
		estimate, ok := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
			Now: s.now(), Strategy: key.Strategy, Route: key.Route,
			CandidateModel: key.CandidateModel, ReferenceModel: key.ReferenceModel,
			ConfiguredQualityBPS: quality, ConfiguredSevereErrorBPS: severe,
		})
		if !ok {
			continue
		}
		item := evaluation.PerformanceItem{
			ProfileID: key.ProfileID, ProfileSlug: record.Slug, ProfileName: record.DisplayName,
			Strategy: key.Strategy, Route: key.Route, TaskType: key.TaskType,
			Difficulty: key.Difficulty, Risk: key.Risk, VisionMode: key.VisionMode,
			CandidateModel: key.CandidateModel, ReferenceModel: key.ReferenceModel,
			PriorKnown: priorKnown, Estimate: estimate,
		}
		items = append(items, item)
		result.Summary.Groups++
		if estimate.Reliable {
			result.Summary.ReliableGroups++
		}
		result.Summary.RawSamples += estimate.RawSamples
		result.Summary.CandidateCostMicroUSD += estimate.CandidateCostMicroUSD
		result.Summary.ReferenceCostMicroUSD += estimate.ReferenceCostMicroUSD
		result.Summary.ReviewerCostMicroUSD += estimate.ReviewerCostMicroUSD
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Estimate.Reliable != items[right].Estimate.Reliable {
			return items[left].Estimate.Reliable
		}
		if items[left].ProfileSlug != items[right].ProfileSlug {
			return items[left].ProfileSlug < items[right].ProfileSlug
		}
		if items[left].CandidateModel != items[right].CandidateModel {
			return items[left].CandidateModel < items[right].CandidateModel
		}
		if items[left].TaskType != items[right].TaskType {
			return items[left].TaskType < items[right].TaskType
		}
		return items[left].Difficulty < items[right].Difficulty
	})
	result.Total = len(items)
	result.TotalPages = (result.Total + pageSize - 1) / pageSize
	start := result.Total
	if page <= result.TotalPages {
		start = (page - 1) * pageSize
	}
	end := min(start+pageSize, result.Total)
	result.Items = append(result.Items, items[start:end]...)
	return result, nil
}

func matchesPerformanceFilter(
	row evaluation.EvidenceAggregate,
	filter evaluation.PerformanceFilter,
) bool {
	if filter.Model != "" && !strings.Contains(
		strings.ToLower(row.CandidateModel), strings.ToLower(filter.Model),
	) {
		return false
	}
	if filter.TaskType != "" && row.TaskType != filter.TaskType ||
		filter.Difficulty != "" && row.Difficulty != filter.Difficulty ||
		filter.Risk != "" && row.Risk != filter.Risk ||
		filter.VisionMode != "" && row.VisionMode != filter.VisionMode {
		return false
	}
	day, err := time.Parse("2006-01-02", row.Day)
	if err != nil {
		return false
	}
	if !filter.From.IsZero() && day.Before(dateOnly(filter.From)) ||
		!filter.To.IsZero() && day.After(dateOnly(filter.To)) {
		return false
	}
	return true
}

func dateOnly(value time.Time) time.Time {
	parsed, _ := time.Parse("2006-01-02", value.UTC().Format("2006-01-02"))
	return parsed
}

func configuredPerformancePrior(
	config profile.RoutingStrategyConfig,
	routeID string,
	model string,
) (int, int, bool) {
	for _, route := range config.Routes {
		if route.ID != routeID {
			continue
		}
		for _, candidate := range route.Candidates {
			if candidate.Model == model {
				return candidate.QualityScoreBPS, candidate.SevereErrorRateBPS, true
			}
		}
	}
	return 5000, 5000, false
}

func (s *StrategyService) CreateDraft(
	ctx context.Context,
	profileID int64,
	config profile.RoutingStrategyConfig,
) (strategy.Version, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return strategy.Version{}, err
	}
	if _, err := s.strategies.Bootstrap(ctx, record); err != nil {
		return strategy.Version{}, err
	}
	config.Name, err = s.strategies.NextName(ctx, profileID, s.now())
	if err != nil {
		return strategy.Version{}, err
	}
	return s.strategies.CreateDraft(ctx, record, config)
}

func (s *StrategyService) UpdateDraft(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	config profile.RoutingStrategyConfig,
) (strategy.Version, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return strategy.Version{}, err
	}
	if s.generations == nil {
		return s.strategies.UpdateDraft(ctx, record, strategyID, config)
	}
	if _, err := s.generations.Get(ctx, profileID, strategyID); errors.Is(err, strategy.ErrNotFound) {
		return s.strategies.UpdateDraft(ctx, record, strategyID, config)
	} else if err != nil {
		return strategy.Version{}, err
	}
	if config.MinNetSavingsBPS <= 0 {
		return strategy.Version{}, fmt.Errorf(
			"%w: generated strategy minimum net savings must remain positive",
			profile.ErrInvalidConfig,
		)
	}
	version, _, err := s.generations.UpdateGeneratedDraft(ctx, record, strategyID, config)
	if err != nil {
		return strategy.Version{}, err
	}
	return version, nil
}

func (s *StrategyService) Advance(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	from strategy.State,
	to strategy.State,
) (strategy.Version, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Version{}, err
	}
	return s.strategies.Advance(ctx, profileID, strategyID, from, to)
}

func (s *StrategyService) Archive(
	ctx context.Context,
	profileID int64,
	strategyID int64,
) (strategy.Version, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Version{}, err
	}
	return s.strategies.Archive(ctx, profileID, strategyID)
}

func (s *StrategyService) StartCanary(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	canaryBPS int,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	if s.generations != nil {
		generation, generationErr := s.generations.Get(ctx, profileID, strategyID)
		switch {
		case generationErr == nil:
			versions, listErr := s.strategies.List(ctx, profileID)
			if listErr != nil {
				return strategy.Snapshot{}, listErr
			}
			var candidate *strategy.Version
			for index := range versions {
				if versions[index].ID == strategyID {
					candidate = &versions[index]
					break
				}
			}
			if candidate == nil {
				return strategy.Snapshot{}, strategy.ErrNotFound
			}
			if safetyErr := validateGeneratedCanarySafety(
				generation.GeneratedConfig,
				candidate.Config,
				record.Config.AutoRouting.StrongBaselineModel,
			); safetyErr != nil {
				return strategy.Snapshot{}, safetyErr
			}
		case errors.Is(generationErr, strategy.ErrNotFound):
		default:
			return strategy.Snapshot{}, generationErr
		}
	}
	publication, err := s.strategies.PrepareStartCanary(
		ctx, profileID, strategyID, canaryBPS, expectedRevision,
	)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
}

func validateGeneratedCanarySafety(
	generated profile.RoutingStrategyConfig,
	edited profile.RoutingStrategyConfig,
	baseline string,
) error {
	generatedRoutes := make(map[string]profile.RouteConfig, len(generated.Routes))
	for _, route := range generated.Routes {
		generatedRoutes[route.ID] = route
	}
	for _, route := range edited.Routes {
		for _, candidate := range route.Candidates {
			if candidate.Model == baseline || !candidatePassesRoute(candidate, route, edited.LatencyTargetMS) {
				continue
			}
			originalRoute, found := generatedRoutes[route.ID]
			if !found {
				return promotionEvidenceError(route.ID, candidate.Model, "manual edit introduced a production-eligible route")
			}
			originalFound := false
			for _, original := range originalRoute.Candidates {
				if original.Model == candidate.Model {
					originalFound = candidatePassesRoute(original, originalRoute, generated.LatencyTargetMS)
					break
				}
			}
			if !originalFound {
				return promotionEvidenceError(route.ID, candidate.Model, "manual edit cannot promote provisional public evidence into canary traffic")
			}
		}
	}
	return nil
}

func candidatePassesRoute(
	candidate profile.RouteCandidateConfig,
	route profile.RouteConfig,
	latencyTargetMS int64,
) bool {
	return candidate.QualityScoreBPS >= route.MinQualityBPS &&
		candidate.StabilityScoreBPS >= route.MinStabilityBPS &&
		candidate.SevereErrorRateBPS <= route.MaxSevereErrorRateBPS &&
		(latencyTargetMS <= 0 || candidate.ExpectedLatencyMS > 0 && candidate.ExpectedLatencyMS <= latencyTargetMS)
}

func (s *StrategyService) CancelCanary(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	publication, err := s.strategies.PrepareCancelCanary(ctx, profileID, expectedRevision)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
}

func (s *StrategyService) Promote(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	snapshot, err := s.strategies.Snapshot(ctx, profileID)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	if snapshot.Revision != expectedRevision {
		return strategy.Snapshot{}, strategy.ErrConflict
	}
	if snapshot.Canary == nil {
		return strategy.Snapshot{}, strategy.ErrInvalidTransition
	}
	if err := s.validatePromotionEvidence(
		ctx, record, snapshot.Canary.ID, snapshot.Canary.Config,
	); err != nil {
		return strategy.Snapshot{}, err
	}
	publication, err := s.strategies.PreparePromote(ctx, profileID, expectedRevision)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
}

type promotionSegment struct {
	taskType   string
	difficulty string
}

func (s *StrategyService) validatePromotionEvidence(
	ctx context.Context,
	record profile.Record,
	strategyID int64,
	config profile.RoutingStrategyConfig,
) error {
	required, err := s.promotionEvidenceRequired(ctx, record.ID, strategyID, config)
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	if s.evidence == nil {
		return evaluation.ErrInsufficientEvidence
	}
	rows, err := s.evidence.ListEvidence(ctx, record.ID)
	if err != nil {
		return err
	}
	segmentsByRoute := make(map[string][]promotionSegment)
	for _, taskRoute := range config.TaskRoutes {
		segmentsByRoute[taskRoute.Route] = append(segmentsByRoute[taskRoute.Route], promotionSegment{
			taskType: taskRoute.TaskType, difficulty: taskRoute.Difficulty,
		})
	}
	for _, route := range config.Routes {
		segments := segmentsByRoute[route.ID]
		if len(segments) == 0 {
			segments = []promotionSegment{{}}
		}
		for _, candidate := range route.Candidates {
			if candidate.Model == record.Config.AutoRouting.StrongBaselineModel {
				continue
			}
			if candidate.QualityScoreBPS < route.MinQualityBPS ||
				candidate.StabilityScoreBPS < route.MinStabilityBPS ||
				candidate.SevereErrorRateBPS > route.MaxSevereErrorRateBPS ||
				config.LatencyTargetMS > 0 &&
					(candidate.ExpectedLatencyMS <= 0 || candidate.ExpectedLatencyMS > config.LatencyTargetMS) {
				return promotionEvidenceError(route.ID, candidate.Model, "configured guardrail is not satisfied")
			}
			for _, segment := range segments {
				quality, ok := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
					Now: s.now(), Strategy: config.Name, Route: route.ID,
					CandidateModel: candidate.Model,
					ReferenceModel: record.Config.AutoRouting.StrongBaselineModel,
					TaskType:       segment.taskType, Difficulty: segment.difficulty,
					ConfiguredQualityBPS:     candidate.QualityScoreBPS,
					ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
				})
				if !ok || !quality.Reliable || quality.QualityLowerBPS < route.MinQualityBPS ||
					quality.SevereErrorUpperBPS > route.MaxSevereErrorRateBPS {
					return promotionEvidenceError(route.ID, candidate.Model, "quality evidence is insufficient or unsafe")
				}
				online, err := s.evidence.OnlineStabilityFor(
					ctx, record.ID, config.Name, route.ID, candidate.Model,
					segment.taskType, segment.difficulty,
				)
				if err != nil {
					return err
				}
				stability, ok := evaluation.EstimateStability(rows, online, evaluation.StabilityEstimateRequest{
					Now: s.now(), Strategy: config.Name, Route: route.ID,
					CandidateModel: candidate.Model,
					ReferenceModel: record.Config.AutoRouting.StrongBaselineModel,
					TaskType:       segment.taskType, Difficulty: segment.difficulty,
					ConfiguredStabilityBPS:   candidate.StabilityScoreBPS,
					ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
				})
				if !ok || !stability.Reliable || stability.LowerBPS < route.MinStabilityBPS ||
					config.LatencyTargetMS > 0 &&
						(stability.ExpectedLatencyMS <= 0 || stability.ExpectedLatencyMS > config.LatencyTargetMS) {
					return promotionEvidenceError(route.ID, candidate.Model, "stability evidence is insufficient or unsafe")
				}
			}
		}
	}
	return nil
}

func (s *StrategyService) CanaryEvidenceUnsafe(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	config profile.RoutingStrategyConfig,
) (bool, error) {
	record, err := s.profile(ctx, profileID)
	if err != nil {
		return true, err
	}
	required, err := s.promotionEvidenceRequired(ctx, profileID, strategyID, config)
	if err != nil {
		return true, err
	}
	if !required {
		return false, nil
	}
	if s.evidence == nil {
		return true, evaluation.ErrInsufficientEvidence
	}
	rows, err := s.evidence.ListEvidence(ctx, profileID)
	if err != nil {
		return true, err
	}
	segmentsByRoute := promotionSegmentsByRoute(config)
	eligibleSegments := 0
	allReliable := true
	allMature := true
	for _, route := range config.Routes {
		segments := segmentsByRoute[route.ID]
		if len(segments) == 0 {
			segments = []promotionSegment{{}}
		}
		for _, candidate := range route.Candidates {
			if candidate.Model == record.Config.AutoRouting.StrongBaselineModel ||
				!candidatePassesRoute(candidate, route, config.LatencyTargetMS) {
				continue
			}
			for _, segment := range segments {
				eligibleSegments++
				segmentMature := false
				quality, qualityOK := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
					Now: s.now(), Strategy: config.Name, Route: route.ID,
					CandidateModel: candidate.Model,
					ReferenceModel: record.Config.AutoRouting.StrongBaselineModel,
					TaskType:       segment.taskType, Difficulty: segment.difficulty,
					ConfiguredQualityBPS:     candidate.QualityScoreBPS,
					ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
				})
				if qualityOK && quality.Reliable &&
					(quality.QualityLowerBPS < route.MinQualityBPS ||
						quality.SevereErrorUpperBPS > route.MaxSevereErrorRateBPS) {
					return true, nil
				}
				if qualityOK && quality.RawSamples >= evaluation.MinimumReliableSampleCount {
					segmentMature = true
				}
				online, onlineErr := s.evidence.OnlineStabilityFor(
					ctx, profileID, config.Name, route.ID, candidate.Model,
					segment.taskType, segment.difficulty,
				)
				if onlineErr != nil {
					return true, onlineErr
				}
				if online.Samples < evaluation.MinimumReliableSampleCount {
					segmentMature = false
				}
				stability, stabilityOK := evaluation.EstimateStability(rows, online, evaluation.StabilityEstimateRequest{
					Now: s.now(), Strategy: config.Name, Route: route.ID,
					CandidateModel: candidate.Model,
					ReferenceModel: record.Config.AutoRouting.StrongBaselineModel,
					TaskType:       segment.taskType, Difficulty: segment.difficulty,
					ConfiguredStabilityBPS:   candidate.StabilityScoreBPS,
					ConfiguredSevereErrorBPS: candidate.SevereErrorRateBPS,
				})
				if stabilityOK && stability.Reliable &&
					(stability.LowerBPS < route.MinStabilityBPS ||
						config.LatencyTargetMS > 0 &&
							(stability.ExpectedLatencyMS <= 0 || stability.ExpectedLatencyMS > config.LatencyTargetMS)) {
					return true, nil
				}
				if !qualityOK || !quality.Reliable || !stabilityOK || !stability.Reliable {
					allReliable = false
				}
				if !segmentMature {
					allMature = false
				}
			}
		}
	}
	if eligibleSegments == 0 {
		return false, nil
	}
	key := [2]int64{profileID, strategyID}
	s.canaryEvidenceMu.Lock()
	defer s.canaryEvidenceMu.Unlock()
	if s.canaryEvidenceReliable == nil {
		s.canaryEvidenceReliable = make(map[[2]int64]bool)
	}
	wasReliable := s.canaryEvidenceReliable[key]
	if allReliable {
		s.canaryEvidenceReliable[key] = true
		return false, nil
	}
	return wasReliable || allMature, nil
}

func promotionSegmentsByRoute(config profile.RoutingStrategyConfig) map[string][]promotionSegment {
	segmentsByRoute := make(map[string][]promotionSegment)
	for _, taskRoute := range config.TaskRoutes {
		segmentsByRoute[taskRoute.Route] = append(segmentsByRoute[taskRoute.Route], promotionSegment{
			taskType: taskRoute.TaskType, difficulty: taskRoute.Difficulty,
		})
	}
	return segmentsByRoute
}

func (s *StrategyService) promotionEvidenceRequired(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	config profile.RoutingStrategyConfig,
) (bool, error) {
	if config.MinNetSavingsBPS > 0 {
		return true, nil
	}
	if s.generations == nil {
		return false, nil
	}
	if _, err := s.generations.Get(ctx, profileID, strategyID); err == nil {
		return true, nil
	} else if errors.Is(err, strategy.ErrNotFound) {
		return false, nil
	} else {
		return false, err
	}
}

func promotionEvidenceError(route, model, reason string) error {
	return errors.Join(
		evaluation.ErrInsufficientEvidence,
		fmt.Errorf("route %s candidate %s: %s", route, model, reason),
	)
}

func (s *StrategyService) Rollback(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	publication, err := s.strategies.PrepareRollback(ctx, profileID, expectedRevision)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
}

func (s *StrategyService) profile(ctx context.Context, profileID int64) (profile.Record, error) {
	record, err := s.profiles.Get(ctx, profileID)
	if err != nil {
		return profile.Record{}, err
	}
	if record.Config.AutoRouting.Enabled {
		resolved, _, err := s.strategies.ResolveRecord(ctx, record)
		return resolved, err
	}
	if _, err := record.Resolve(); err != nil {
		return profile.Record{}, err
	}
	return record, nil
}
