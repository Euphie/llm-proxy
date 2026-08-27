package profile

import (
	"encoding/json"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
)

type RoutingPolicyConfig struct {
	RoutingStrategyConfig
	AnalyzerTimeout           string                    `json:"analyzer_timeout"`
	AnalyzerMinConfidenceBPS  int                       `json:"analyzer_min_confidence_bps"`
	SessionTTL                string                    `json:"session_ttl"`
	SessionLockTokenThreshold int                       `json:"session_lock_token_threshold"`
	SelfEscalation            SelfEscalationConfig      `json:"self_escalation"`
	DynamicOptimization       DynamicOptimizationConfig `json:"dynamic_optimization"`
}

func PolicyFromLegacy(auto AutoRoutingConfig, strategy RoutingStrategyConfig) RoutingPolicyConfig {
	return RoutingPolicyConfig{
		RoutingStrategyConfig:     strategy,
		AnalyzerTimeout:           auto.AnalyzerTimeout,
		AnalyzerMinConfidenceBPS:  auto.AnalyzerMinConfidenceBPS,
		SessionTTL:                auto.SessionTTL,
		SessionLockTokenThreshold: auto.SessionLockTokenThreshold,
		SelfEscalation:            auto.SelfEscalation,
		DynamicOptimization:       auto.DynamicOptimization,
	}
}

func (policy RoutingPolicyConfig) AutoConfig() AutoRoutingConfig {
	auto := AutoRoutingConfig{
		Enabled:                   true,
		AnalyzerTimeout:           policy.AnalyzerTimeout,
		AnalyzerMinConfidenceBPS:  policy.AnalyzerMinConfidenceBPS,
		SessionTTL:                policy.SessionTTL,
		SessionLockTokenThreshold: policy.SessionLockTokenThreshold,
		SelfEscalation:            policy.SelfEscalation,
		DynamicOptimization:       policy.DynamicOptimization,
		Strategy:                  policy.RoutingStrategyConfig,
	}
	ApplyRoutingStrategyRoles(&auto, policy.RoutingStrategyConfig)
	return auto
}

func ResolvePolicyRuntime(
	record Record,
	models []modeldirectory.Record,
	policy *RoutingPolicyConfig,
) (Runtime, error) {
	configs := make([]ModelCapabilityConfig, 0, len(models))
	statuses := make(map[string]modeldirectory.Status, len(models))
	for _, model := range models {
		var capability ModelCapabilityConfig
		if err := json.Unmarshal(model.CapabilityJSON, &capability); err != nil {
			return Runtime{}, fmt.Errorf("%w: decode model %q capability: %v", ErrInvalidConfig, model.ModelID, err)
		}
		if capability.ID != model.ModelID {
			return Runtime{}, fmt.Errorf(
				"%w: model directory ID %q does not match capability ID %q",
				ErrInvalidConfig, model.ModelID, capability.ID,
			)
		}
		if _, duplicate := statuses[model.ModelID]; duplicate {
			return Runtime{}, fmt.Errorf("%w: duplicate model directory ID %q", ErrInvalidConfig, model.ModelID)
		}
		statuses[model.ModelID] = model.Status
		configs = append(configs, capability)
	}

	record.Config.Models = configs
	if record.Config.AutoRouting.Enabled {
		if policy == nil {
			return Runtime{}, fmt.Errorf("%w: active routing Policy is required", ErrInvalidConfig)
		}
		for model := range referencedPolicyModels(*policy) {
			if statuses[model] != modeldirectory.StatusAvailable {
				return Runtime{}, fmt.Errorf("%w: model %q is not available", ErrInvalidConfig, model)
			}
		}
		record.Config.AutoRouting = policy.AutoConfig()
	}
	if record.Config.Vision.Enabled && statuses[record.Config.Vision.Model] != modeldirectory.StatusAvailable {
		return Runtime{}, fmt.Errorf(
			"%w: vision model %q is not available", ErrInvalidConfig, record.Config.Vision.Model,
		)
	}
	runtime, err := record.Resolve()
	if err != nil {
		return Runtime{}, err
	}
	runtime.ModelStatuses = statuses
	return runtime, nil
}

func referencedPolicyModels(policy RoutingPolicyConfig) map[string]struct{} {
	references := make(map[string]struct{})
	if policy.Roles != nil {
		for _, model := range policy.Roles.Participants {
			references[model] = struct{}{}
		}
		references[policy.Roles.StrongBaselineModel] = struct{}{}
		references[policy.Roles.TaskAnalyzerModel] = struct{}{}
		if policy.Roles.ReviewerModel != "" {
			references[policy.Roles.ReviewerModel] = struct{}{}
		}
	}
	if policy.DynamicOptimization.Enabled && policy.DynamicOptimization.ReviewerModel != "" {
		references[policy.DynamicOptimization.ReviewerModel] = struct{}{}
	}
	for _, route := range policy.Routes {
		for _, candidate := range route.Candidates {
			references[candidate.Model] = struct{}{}
		}
	}
	delete(references, "")
	return references
}
