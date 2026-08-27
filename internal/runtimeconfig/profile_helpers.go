package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
)

func profileRecordFromInput(input profile.SaveInput, now time.Time) profile.Record {
	return profile.Record{
		ID: input.ID, Slug: input.Slug, DisplayName: input.DisplayName,
		Enabled: input.Enabled, Config: input.Config, CreatedAt: now, UpdatedAt: now,
	}
}

func cloneModelsForProfile(source []modeldirectory.Record, profileID int64, now time.Time) []modeldirectory.Record {
	models := make([]modeldirectory.Record, len(source))
	for index, model := range source {
		models[index] = model
		models[index].ProfileID = profileID
		models[index].CapabilityJSON = append([]byte(nil), model.CapabilityJSON...)
		models[index].CreatedAt = now
		models[index].UpdatedAt = now
		if model.Status == "" {
			models[index].Status = modeldirectory.StatusAvailable
		}
		if model.RetiredAt != nil {
			retired := now
			models[index].RetiredAt = &retired
		}
	}
	return models
}

func resolveAggregateRuntime(aggregate Aggregate, policy *profile.RoutingPolicyConfig) (profile.Runtime, error) {
	return profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, policy)
}

func encodeAggregateConfig(aggregate Aggregate) ([]byte, []byte, error) {
	profileJSON, err := json.Marshal(aggregate.Profile.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("encode Profile config: %w", err)
	}
	var policyJSON []byte
	if aggregate.Active != nil {
		policyJSON, err = json.Marshal(aggregate.Active.Policy)
		if err != nil {
			return nil, nil, fmt.Errorf("encode routing Policy: %w", err)
		}
	}
	return profileJSON, policyJSON, nil
}
