package admin

import (
	"context"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/aggregate"
)

type AggregateService struct {
	store    *aggregate.Store
	registry *aggregate.Registry
}

func NewAggregateService(store *aggregate.Store, registry *aggregate.Registry) *AggregateService {
	return &AggregateService{store: store, registry: registry}
}

func (s *AggregateService) Reload(ctx context.Context) error {
	snapshot, err := s.store.LoadSnapshot(ctx)
	if err != nil {
		return err
	}
	if err := s.registry.Load(snapshot); err != nil {
		return fmt.Errorf("publish aggregate gateway snapshot: %w", err)
	}
	return nil
}

func (s *AggregateService) ListProviders(ctx context.Context) ([]aggregate.ProviderAccount, error) {
	return s.store.ListProviders(ctx)
}

func (s *AggregateService) SaveProvider(ctx context.Context, input aggregate.ProviderAccountInput) (aggregate.ProviderAccount, error) {
	record, err := s.store.SaveProvider(ctx, input)
	if err != nil {
		return aggregate.ProviderAccount{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return record, err
	}
	return record, nil
}

func (s *AggregateService) ListGateways(ctx context.Context) ([]aggregate.Gateway, error) {
	return s.store.ListGateways(ctx)
}

func (s *AggregateService) GetGateway(ctx context.Context, id int64) (aggregate.Gateway, error) {
	return s.store.GetGateway(ctx, id)
}

func (s *AggregateService) SaveGateway(ctx context.Context, input aggregate.GatewayInput) (aggregate.Gateway, error) {
	record, err := s.store.SaveGateway(ctx, input)
	if err != nil {
		return aggregate.Gateway{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return record, err
	}
	return record, nil
}

func (s *AggregateService) CreateKey(ctx context.Context, input aggregate.IssuedKeyInput) (aggregate.IssuedKey, error) {
	key, err := s.store.CreateKey(ctx, input)
	if err != nil {
		return aggregate.IssuedKey{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return key, err
	}
	return key, nil
}

func (s *AggregateService) ListKeys(ctx context.Context, gatewayID int64) ([]aggregate.IssuedKey, error) {
	return s.store.ListKeys(ctx, gatewayID)
}
