package admin

import (
	"context"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
)

type ProfileService struct {
	store       *profile.Store
	coordinator *gateway.Coordinator
}

func NewProfileService(
	store *profile.Store,
	coordinator *gateway.Coordinator,
) *ProfileService {
	return &ProfileService{store: store, coordinator: coordinator}
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
	if _, err := record.Resolve(); err != nil {
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
	if _, err := record.Resolve(); err != nil {
		return profile.Record{}, err
	}
	return s.coordinator.Copy(ctx, id, slug, displayName)
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
