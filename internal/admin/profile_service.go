package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

type ProfileService struct {
	store       *profile.Store
	coordinator *gateway.Coordinator
	strategies  *strategy.Store
	runtimes    *runtimeconfig.Store
	runtime     *gateway.RuntimeCoordinator
}

func (s *ProfileService) CreateRuntime(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	if s.runtimes == nil || s.runtime == nil {
		return s.Save(ctx, input, makeDefault)
	}
	models := make([]modeldirectory.Record, 0, len(input.Config.Models))
	for _, capability := range input.Config.Models {
		payload, err := json.Marshal(capability)
		if err != nil {
			return profile.Record{}, fmt.Errorf("encode model %q: %w", capability.ID, err)
		}
		models = append(models, modeldirectory.Record{
			ModelID: capability.ID, CapabilityJSON: payload, Status: modeldirectory.StatusAvailable,
		})
	}
	var policyConfig *profile.RoutingPolicyConfig
	if input.Config.AutoRouting.Enabled {
		strategyConfig := input.Config.AutoRouting.Strategy
		if strategyConfig.Roles == nil {
			strategyConfig.Roles = profile.RoutingStrategyRoles(input.Config.AutoRouting)
		}
		policy := profile.PolicyFromLegacy(input.Config.AutoRouting, strategyConfig)
		policyConfig = &policy
	}
	input.Config.Version = 2
	prepared, err := s.runtimes.PrepareProfileCreate(ctx, runtimeconfig.ProfileCreateInput{
		Profile: input, Models: models, Policy: policyConfig, MakeDefault: makeDefault,
		Reason: "Profile created.", Actor: "admin",
	})
	if err != nil {
		return profile.Record{}, err
	}
	prospective := prepared.Prospective()
	if _, err := s.runtime.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective, DefaultProfileID: prepared.DefaultProfileID(),
		Commit: prepared.Commit,
	}); err != nil {
		return profile.Record{}, err
	}
	return prospective.Profile, nil
}

func (s *ProfileService) EnableRuntimeConfiguration(
	store *runtimeconfig.Store,
	coordinator *gateway.RuntimeCoordinator,
) {
	s.runtimes = store
	s.runtime = coordinator
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
			if routingRolesEqual(input.Config.AutoRouting, snapshot.Active.Config.Roles) {
				input.Config.AutoRouting.Strategy = snapshot.Active.Config
			} else if err := s.validateStagedRoutingConfig(ctx, input.ID, input.Config.AutoRouting); err != nil {
				return profile.Record{}, err
			}
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

func (s *ProfileService) SaveRuntime(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
	expectedRevision int64,
) (profile.Record, error) {
	if s.runtimes == nil || s.runtime == nil {
		return profile.Record{}, errors.New("Profile runtime configuration is unavailable")
	}
	prepared, err := s.runtimes.PrepareProfile(ctx, runtimeconfig.ProfileMutationInput{
		ProfileID: input.ID, ExpectedRevision: expectedRevision, Profile: input,
		MakeDefault: makeDefault, Reason: "Profile configuration updated.", Actor: "admin",
	})
	if err != nil {
		return profile.Record{}, err
	}
	prospective := prepared.Prospective()
	if _, err := s.runtime.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective, DefaultProfileID: prepared.DefaultProfileID(),
		Commit: prepared.Commit,
	}); err != nil {
		return profile.Record{}, err
	}
	return prospective.Profile, nil
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
	if record.Config.Version == 2 && s.runtimes != nil && s.runtime != nil {
		source, err := s.runtimes.Load(ctx, id)
		if err != nil {
			return profile.Record{}, err
		}
		var policyConfig *profile.RoutingPolicyConfig
		if source.Active != nil {
			policy := source.Active.Policy
			policyConfig = &policy
		}
		prepared, err := s.runtimes.PrepareProfileCreate(ctx, runtimeconfig.ProfileCreateInput{
			Profile: profile.SaveInput{
				Slug: slug, DisplayName: displayName, Enabled: record.Enabled, Config: record.Config,
			},
			Models: source.Models, Policy: policyConfig, Reason: fmt.Sprintf("Copied from Profile %d.", id),
			Actor: "admin",
		})
		if err != nil {
			return profile.Record{}, err
		}
		prospective := prepared.Prospective()
		if _, err := s.runtime.Publish(ctx, gateway.RuntimePublication{
			Prospective: prospective, DefaultProfileID: prepared.DefaultProfileID(),
			Commit: prepared.Commit,
		}); err != nil {
			return profile.Record{}, err
		}
		return prospective.Profile, nil
	}
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
	if record.Config.Version == 2 || s.strategies == nil || !record.Config.AutoRouting.Enabled {
		return record, nil
	}
	resolved, snapshot, err := s.strategies.ResolveRecord(ctx, record)
	if err != nil {
		return profile.Record{}, err
	}
	if !routingRolesEqual(record.Config.AutoRouting, snapshot.Active.Config.Roles) {
		return record, nil
	}
	return resolved, nil
}

func (s *ProfileService) validateStagedRoutingConfig(
	ctx context.Context,
	profileID int64,
	auto profile.AutoRoutingConfig,
) error {
	if auto.Strategy.Roles == nil || !routingRolesEqual(auto, auto.Strategy.Roles) {
		return fmt.Errorf("%w: staged routing strategy roles do not match the selected models", profile.ErrInvalidConfig)
	}
	versions, err := s.strategies.List(ctx, profileID)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if version.ArchivedAt != nil || version.State == strategy.StateActive {
			continue
		}
		if reflect.DeepEqual(version.Config, auto.Strategy) {
			return nil
		}
	}
	return fmt.Errorf("%w: save a compatible routing strategy draft before changing model roles", profile.ErrInvalidConfig)
}

func routingRolesEqual(
	auto profile.AutoRoutingConfig,
	roles *profile.RoutingStrategyRolesConfig,
) bool {
	if roles == nil || auto.StrongBaselineModel != roles.StrongBaselineModel ||
		auto.TaskAnalyzerModel != roles.TaskAnalyzerModel ||
		auto.DynamicOptimization.ReviewerModel != roles.ReviewerModel ||
		len(auto.Participants) != len(roles.Participants) {
		return false
	}
	participants := make(map[string]int, len(auto.Participants))
	for _, model := range auto.Participants {
		participants[model]++
	}
	for _, model := range roles.Participants {
		participants[model]--
		if participants[model] < 0 {
			return false
		}
	}
	return true
}

func (s *ProfileService) SetDefault(ctx context.Context, id int64) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if record.Config.Version != 2 {
		if _, err := record.Resolve(); err != nil {
			return err
		}
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
	if record.Config.Version != 2 {
		if _, err := record.Resolve(); err != nil {
			return err
		}
	}
	if replacementDefaultID != 0 {
		replacement, err := s.store.Get(ctx, replacementDefaultID)
		if err != nil {
			return err
		}
		if replacement.Config.Version != 2 {
			if _, err := replacement.Resolve(); err != nil {
				return err
			}
		}
	}
	return s.coordinator.Delete(ctx, id, replacementDefaultID)
}
