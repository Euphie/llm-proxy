package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/aggregate"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
)

type ProfileService struct {
	store       *profile.Store
	coordinator *gateway.Coordinator
	aggregate   *AggregateService
}

func NewProfileService(
	store *profile.Store,
	coordinator *gateway.Coordinator,
	aggregateService *AggregateService,
) *ProfileService {
	return &ProfileService{store: store, coordinator: coordinator, aggregate: aggregateService}
}

func (s *ProfileService) List(ctx context.Context) ([]profile.Record, int64, error) {
	return s.store.LoadSnapshot(ctx)
}

func (s *ProfileService) Get(ctx context.Context, id int64) (profile.Record, error) {
	return s.store.Get(ctx, id)
}

func (s *ProfileService) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	record := profile.Record{
		ID:          input.ID,
		Slug:        input.Slug,
		DisplayName: input.DisplayName,
		Enabled:     input.Enabled,
		Config:      input.Config,
	}
	if err := s.validate(ctx, record); err != nil {
		return profile.Record{}, err
	}
	return s.coordinator.Save(ctx, input, makeDefault)
}

func (s *ProfileService) Copy(
	ctx context.Context,
	id int64,
	slug, displayName string,
) (profile.Record, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return profile.Record{}, err
	}
	record.ID = 0
	record.Slug = slug
	record.DisplayName = displayName
	if err := s.validate(ctx, record); err != nil {
		return profile.Record{}, err
	}
	return s.coordinator.Copy(ctx, id, slug, displayName)
}

func (s *ProfileService) SetDefault(ctx context.Context, id int64) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.validate(ctx, record); err != nil {
		return err
	}
	return s.coordinator.SetDefault(ctx, id)
}

func (s *ProfileService) Delete(
	ctx context.Context,
	id, replacementDefaultID int64,
) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.validate(ctx, record); err != nil {
		return err
	}
	if replacementDefaultID != 0 {
		replacement, err := s.store.Get(ctx, replacementDefaultID)
		if err != nil {
			return err
		}
		if err := s.validate(ctx, replacement); err != nil {
			return err
		}
	}
	return s.coordinator.Delete(ctx, id, replacementDefaultID)
}

func (s *ProfileService) validate(ctx context.Context, record profile.Record) error {
	runtime, err := record.Resolve()
	if err != nil {
		return err
	}
	if runtime.UpstreamType != profile.UpstreamTypeAggregateGateway {
		return nil
	}
	if s.aggregate == nil {
		return fmt.Errorf("%w: aggregate gateway lookup unavailable", profile.ErrInvalidConfig)
	}
	gatewayRecord, err := s.aggregate.GetGateway(ctx, runtime.UpstreamGatewayID)
	if err != nil {
		if errors.Is(err, aggregate.ErrNotFound) {
			return fmt.Errorf("%w: aggregate gateway %d not found", aggregate.ErrNotFound, runtime.UpstreamGatewayID)
		}
		return err
	}
	if gatewayRecord.Protocol != runtime.Protocol {
		return fmt.Errorf("%w: aggregate gateway protocol mismatch", profile.ErrInvalidConfig)
	}
	if runtime.Vision.Enabled && !gatewayHasModelRoute(gatewayRecord, runtime.Vision.Model) {
		return fmt.Errorf(
			"%w: aggregate gateway does not route vision model %q",
			profile.ErrInvalidConfig,
			runtime.Vision.Model,
		)
	}
	return nil
}

func gatewayHasModelRoute(gatewayRecord aggregate.Gateway, model string) bool {
	for _, route := range gatewayRecord.Routes {
		if route.Enabled && route.PublicModel == model {
			return true
		}
	}
	return false
}
