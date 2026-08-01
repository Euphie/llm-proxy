package admin

import (
	"context"
	"errors"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

type ProfileService struct {
	store       *profile.Store
	coordinator *gateway.Coordinator
	strategies  *strategy.Store
}

func NewProfileService(
	store *profile.Store,
	coordinator *gateway.Coordinator,
	strategyStores ...*strategy.Store,
) *ProfileService {
	service := &ProfileService{store: store, coordinator: coordinator}
	if len(strategyStores) != 0 {
		service.strategies = strategyStores[0]
	}
	return service
}

func (s *ProfileService) List(ctx context.Context) ([]profile.Record, int64, error) {
	records, defaultID, err := s.store.LoadSnapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	for index := range records {
		records[index], err = s.resolveStrategy(ctx, records[index])
		if err != nil {
			return nil, 0, err
		}
	}
	return records, defaultID, nil
}

func (s *ProfileService) Get(ctx context.Context, id int64) (profile.Record, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return profile.Record{}, err
	}
	return s.resolveStrategy(ctx, record)
}

func (s *ProfileService) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	if input.ID > 0 && input.Config.AutoRouting.Enabled && s.strategies != nil {
		snapshot, err := s.strategies.Snapshot(ctx, input.ID)
		if err == nil {
			input.Config.AutoRouting.Strategy = snapshot.Active.Config
		} else if !errors.Is(err, strategy.ErrNotFound) {
			return profile.Record{}, err
		}
	}
	record := profile.Record{
		ID:          input.ID,
		Slug:        input.Slug,
		DisplayName: input.DisplayName,
		Enabled:     input.Enabled,
		Config:      input.Config,
	}
	if _, err := record.Resolve(); err != nil {
		return profile.Record{}, err
	}
	record, err := s.coordinator.Save(ctx, input, makeDefault)
	if err != nil {
		return record, err
	}
	return s.resolveStrategy(ctx, record)
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
	if _, err := record.Resolve(); err != nil {
		return profile.Record{}, err
	}
	if s.strategies == nil {
		return s.coordinator.Copy(ctx, id, slug, displayName)
	}
	return s.Save(ctx, profile.SaveInput{
		Slug:        slug,
		DisplayName: displayName,
		Enabled:     record.Enabled,
		Config:      record.Config,
	}, false)
}

func (s *ProfileService) resolveStrategy(
	ctx context.Context,
	record profile.Record,
) (profile.Record, error) {
	if s.strategies == nil || !record.Config.AutoRouting.Enabled {
		return record, nil
	}
	resolved, _, err := s.strategies.ResolveRecord(ctx, record)
	return resolved, err
}

func (s *ProfileService) SetDefault(ctx context.Context, id int64) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := record.Resolve(); err != nil {
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
	if _, err := record.Resolve(); err != nil {
		return err
	}
	if replacementDefaultID != 0 {
		replacement, err := s.store.Get(ctx, replacementDefaultID)
		if err != nil {
			return err
		}
		if _, err := replacement.Resolve(); err != nil {
			return err
		}
	}
	return s.coordinator.Delete(ctx, id, replacementDefaultID)
}
