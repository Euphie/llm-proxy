package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
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
	Snapshot         strategy.Snapshot            `json:"snapshot"`
	Strategies       []strategy.Version           `json:"strategies"`
	QualityEstimates []evaluation.QualityEstimate `json:"quality_estimates,omitempty"`
	EvaluationBudget evaluation.BudgetSnapshot    `json:"evaluation_budget"`
}

type StrategyService struct {
	profiles    *profile.Store
	strategies  *strategy.Store
	coordinator *gateway.Coordinator
	now         func() time.Time
	evidence    *evaluation.Store
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
			candidate.QualityScoreBPS = estimate.QualityLowerBPS
			candidate.SevereErrorRateBPS = estimate.SevereErrorUpperBPS
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
		versions, listErr := s.strategies.List(ctx, record.ID)
		if listErr != nil {
			return evaluation.PerformancePage{}, listErr
		}
		byName := make(map[string]profile.RoutingStrategyConfig, len(versions))
		for _, version := range versions {
			byName[version.Config.Name] = version.Config
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
	return s.strategies.UpdateDraft(ctx, record, strategyID, config)
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

func (s *StrategyService) StartCanary(
	ctx context.Context,
	profileID int64,
	strategyID int64,
	canaryBPS int,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	publication, err := s.strategies.PrepareStartCanary(
		ctx, profileID, strategyID, canaryBPS, expectedRevision,
	)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
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
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	publication, err := s.strategies.PreparePromote(ctx, profileID, expectedRevision)
	if err != nil {
		return strategy.Snapshot{}, err
	}
	return s.coordinator.PublishStrategy(ctx, publication)
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
