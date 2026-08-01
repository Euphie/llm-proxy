package admin

import (
	"context"
	"time"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

type StrategyOverview struct {
	Snapshot   strategy.Snapshot  `json:"snapshot"`
	Strategies []strategy.Version `json:"strategies"`
}

type StrategyService struct {
	profiles    *profile.Store
	strategies  *strategy.Store
	coordinator *gateway.Coordinator
	now         func() time.Time
}

func NewStrategyService(
	profiles *profile.Store,
	strategies *strategy.Store,
	coordinator *gateway.Coordinator,
	now func() time.Time,
) *StrategyService {
	if now == nil {
		now = time.Now
	}
	return &StrategyService{
		profiles: profiles, strategies: strategies, coordinator: coordinator, now: now,
	}
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
	return StrategyOverview{Snapshot: snapshot, Strategies: versions}, nil
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
	snapshot, err := s.strategies.StartCanary(ctx, profileID, strategyID, canaryBPS, expectedRevision)
	return s.publish(ctx, snapshot, err)
}

func (s *StrategyService) CancelCanary(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	snapshot, err := s.strategies.CancelCanary(ctx, profileID, expectedRevision)
	return s.publish(ctx, snapshot, err)
}

func (s *StrategyService) Promote(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	snapshot, err := s.strategies.Promote(ctx, profileID, expectedRevision)
	return s.publish(ctx, snapshot, err)
}

func (s *StrategyService) Rollback(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (strategy.Snapshot, error) {
	if _, err := s.profile(ctx, profileID); err != nil {
		return strategy.Snapshot{}, err
	}
	snapshot, err := s.strategies.Rollback(ctx, profileID, expectedRevision)
	return s.publish(ctx, snapshot, err)
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

func (s *StrategyService) publish(
	ctx context.Context,
	snapshot strategy.Snapshot,
	err error,
) (strategy.Snapshot, error) {
	if err != nil {
		return strategy.Snapshot{}, err
	}
	if err := s.coordinator.Reload(ctx); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
