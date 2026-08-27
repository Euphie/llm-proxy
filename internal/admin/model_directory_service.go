package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
)

type ModelDirectoryOverview struct {
	RuntimeState runtimeconfig.State          `json:"runtime_state"`
	Active       *runtimeconfig.PolicyVersion `json:"active,omitempty"`
	Models       []modeldirectory.Record      `json:"models"`
}

func (service *RoutingPolicyService) Models(
	ctx context.Context,
	profileID int64,
) (ModelDirectoryOverview, error) {
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return ModelDirectoryOverview{}, err
	}
	return modelDirectoryOverview(aggregate), nil
}

func (service *RoutingPolicyService) MutateModel(
	ctx context.Context,
	input runtimeconfig.ModelMutationInput,
) (ModelDirectoryOverview, error) {
	if service.coordinator == nil {
		return ModelDirectoryOverview{}, fmt.Errorf("routing Policy runtime coordinator is unavailable")
	}
	input.Actor = "admin"
	prepared, err := service.store.PrepareModel(ctx, input)
	if err != nil {
		return ModelDirectoryOverview{}, err
	}
	prospective := prepared.Prospective()
	var policy *profile.RoutingPolicyConfig
	if prospective.Active != nil {
		policy = &prospective.Active.Policy
	}
	if _, err := profile.ResolvePolicyRuntime(prospective.Profile, prospective.Models, policy); err != nil {
		if input.Kind == runtimeconfig.ModelRetire {
			return ModelDirectoryOverview{}, fmt.Errorf("%w: %v", runtimeconfig.ErrModelStillInUse, err)
		}
		return ModelDirectoryOverview{}, err
	}
	if _, err := service.coordinator.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective,
		Commit:      prepared.Commit,
	}); err != nil {
		return ModelDirectoryOverview{}, err
	}
	return service.Models(ctx, input.ProfileID)
}

func (service *RoutingPolicyService) EmergencyOffline(
	ctx context.Context,
	profileID, expectedRevision int64,
	modelID string,
	replacements map[string]string,
	visionAction, reason string,
) (ModelDirectoryOverview, error) {
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return ModelDirectoryOverview{}, err
	}
	if aggregate.State.Revision != expectedRevision {
		return ModelDirectoryOverview{}, runtimeconfig.ErrRevisionConflict
	}
	if aggregate.Active == nil {
		return ModelDirectoryOverview{}, fmt.Errorf("%w: active routing Policy is required", profile.ErrInvalidConfig)
	}
	policy, config, err := deriveEmergencyPolicy(
		aggregate.Active.Policy, aggregate.Profile.Config, modelID, replacements, visionAction,
	)
	if err != nil {
		return ModelDirectoryOverview{}, err
	}
	prepared, err := service.store.PrepareEmergencyOffline(ctx, runtimeconfig.EmergencyOfflineInput{
		ProfileID: profileID, ExpectedRevision: expectedRevision, ModelID: modelID,
		Policy: policy, ProfileConfig: &config, Reason: reason, Actor: "admin",
	})
	if err != nil {
		return ModelDirectoryOverview{}, err
	}
	prospective := prepared.Prospective()
	if _, err := profile.ResolvePolicyRuntime(
		prospective.Profile, prospective.Models, &prospective.Active.Policy,
	); err != nil {
		return ModelDirectoryOverview{}, err
	}
	if _, err := service.coordinator.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective,
		Commit:      prepared.Commit,
	}); err != nil {
		return ModelDirectoryOverview{}, err
	}
	return service.Models(ctx, profileID)
}

func modelDirectoryOverview(aggregate runtimeconfig.Aggregate) ModelDirectoryOverview {
	return ModelDirectoryOverview{
		RuntimeState: aggregate.State,
		Active:       aggregate.Active,
		Models:       aggregate.Models,
	}
}

func deriveEmergencyPolicy(
	source profile.RoutingPolicyConfig,
	config profile.Config,
	modelID string,
	replacements map[string]string,
	visionAction string,
) (profile.RoutingPolicyConfig, profile.Config, error) {
	payload, err := json.Marshal(source)
	if err != nil {
		return profile.RoutingPolicyConfig{}, profile.Config{}, err
	}
	var policy profile.RoutingPolicyConfig
	if err := json.Unmarshal(payload, &policy); err != nil {
		return profile.RoutingPolicyConfig{}, profile.Config{}, err
	}
	if policy.Roles == nil {
		return profile.RoutingPolicyConfig{}, profile.Config{}, fmt.Errorf(
			"%w: routing Policy roles are required", profile.ErrInvalidConfig,
		)
	}
	participants := removeModel(policy.Roles.Participants, modelID)
	for _, key := range []string{"participant", "strong_baseline", "task_analyzer", "reviewer"} {
		if replacement := replacements[key]; replacement != "" {
			participants = appendUnique(participants, replacement)
		}
	}
	policy.Roles.Participants = participants
	if policy.Roles.StrongBaselineModel == modelID {
		policy.Roles.StrongBaselineModel = replacements["strong_baseline"]
		if policy.Roles.StrongBaselineModel == "" {
			return policy, config, missingEmergencyReplacement("strong_baseline")
		}
	}
	if policy.Roles.TaskAnalyzerModel == modelID {
		policy.Roles.TaskAnalyzerModel = replacements["task_analyzer"]
		if policy.Roles.TaskAnalyzerModel == "" {
			return policy, config, missingEmergencyReplacement("task_analyzer")
		}
	}
	if policy.Roles.ReviewerModel == modelID {
		policy.Roles.ReviewerModel = replacements["reviewer"]
		if policy.Roles.ReviewerModel == "" {
			return policy, config, missingEmergencyReplacement("reviewer")
		}
	}
	if policy.DynamicOptimization.ReviewerModel == modelID {
		policy.DynamicOptimization.ReviewerModel = policy.Roles.ReviewerModel
	}
	for index := range policy.Routes {
		candidates := policy.Routes[index].Candidates[:0]
		for _, candidate := range policy.Routes[index].Candidates {
			if candidate.Model != modelID {
				candidates = append(candidates, candidate)
			}
		}
		policy.Routes[index].Candidates = candidates
		if len(candidates) == 0 {
			return policy, config, fmt.Errorf(
				"%w: route %q would have no candidates", profile.ErrInvalidConfig, policy.Routes[index].ID,
			)
		}
	}
	if config.Vision.Enabled && config.Vision.Model == modelID {
		switch visionAction {
		case "disable":
			config.Vision.Enabled = false
			config.Vision.Model = ""
		case "replace":
			config.Vision.Model = replacements["vision"]
			if config.Vision.Model == "" {
				return policy, config, missingEmergencyReplacement("vision")
			}
		default:
			return policy, config, fmt.Errorf(
				"%w: vision_action must be replace or disable", profile.ErrInvalidConfig,
			)
		}
	}
	return policy, config, nil
}

func removeModel(models []string, target string) []string {
	result := make([]string, 0, len(models))
	for _, model := range models {
		if model != target {
			result = append(result, model)
		}
	}
	return result
}

func appendUnique(models []string, model string) []string {
	for _, existing := range models {
		if existing == model {
			return models
		}
	}
	return append(models, model)
}

func missingEmergencyReplacement(role string) error {
	return fmt.Errorf("%w: replacement for %s is required", profile.ErrInvalidConfig, role)
}
