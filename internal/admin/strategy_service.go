package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

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
	snapshot, err := s.strategies.Bootstrap(ctx, record)
	if err != nil {
		return StrategyOverview{}, err
	}
	versions, err := s.strategies.List(ctx, profileID)
	if err != nil {
		return StrategyOverview{}, err
	}
	overview := StrategyOverview{Snapshot: snapshot, Strategies: versions}
	if s.evidence == nil {
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
