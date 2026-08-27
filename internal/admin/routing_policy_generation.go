package admin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

type RoutingPolicyPreview struct {
	Policy       profile.RoutingPolicyConfig    `json:"policy"`
	Explanations []strategycompiler.Explanation `json:"explanations"`
	SourceDigest string                         `json:"source_digest"`
	Confidence   strategycompiler.Confidence    `json:"confidence"`
}

func (service *RoutingPolicyService) Generate(
	ctx context.Context,
	profileID int64,
	intent strategycompiler.Intent,
) (RoutingPolicyPreview, error) {
	if service.compiler == nil {
		return RoutingPolicyPreview{}, errors.New("routing Policy generation is unavailable")
	}
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return RoutingPolicyPreview{}, err
	}
	record := aggregate.Profile
	record.Config.Models = nil
	for _, model := range aggregate.Models {
		if model.Status != modeldirectory.StatusAvailable {
			continue
		}
		var capability profile.ModelCapabilityConfig
		if err := json.Unmarshal(model.CapabilityJSON, &capability); err != nil {
			return RoutingPolicyPreview{}, err
		}
		record.Config.Models = append(record.Config.Models, capability)
	}
	if aggregate.Active != nil {
		record.Config.AutoRouting = aggregate.Active.Policy.AutoConfig()
	}
	recommendation, err := service.compiler.Compile(ctx, record, intent)
	if err != nil {
		return RoutingPolicyPreview{}, err
	}
	base := profile.AutoRoutingConfig{Enabled: true}
	if aggregate.Active != nil {
		base = aggregate.Active.Policy.AutoConfig()
	}
	policy := profile.PolicyFromLegacy(base, recommendation.Config)
	policy.Roles = recommendation.Config.Roles
	if _, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, &policy); err != nil {
		return RoutingPolicyPreview{}, err
	}
	return RoutingPolicyPreview{
		Policy: policy, Explanations: recommendation.Explanations,
		SourceDigest: recommendation.SourceDigest, Confidence: recommendation.Confidence,
	}, nil
}
