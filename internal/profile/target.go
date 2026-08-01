package profile

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	PrimaryTargetID  = "primary"
	maxBackupTargets = 16
)

var targetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var targetTrustIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,127}$`)

type TargetConfig struct {
	ID              string   `json:"id"`
	Upstream        string   `json:"upstream"`
	ProviderID      string   `json:"provider_id"`
	CredentialScope string   `json:"credential_scope"`
	Models          []string `json:"models"`
}

type TargetRuntime struct {
	ID       string
	Upstream string
	Models   []string

	modelSet map[string]struct{}
}

func (t TargetRuntime) Supports(model string) bool {
	if t.ID == PrimaryTargetID {
		return true
	}
	if t.modelSet != nil {
		_, ok := t.modelSet[model]
		return ok
	}
	for _, configured := range t.Models {
		if configured == model {
			return true
		}
	}
	return false
}

func (r Runtime) RoutingTargets(model string) []TargetRuntime {
	targets := make([]TargetRuntime, 0, len(r.Targets))
	for _, target := range r.Targets {
		if !target.Supports(model) {
			continue
		}
		cloned := target
		cloned.Models = append([]string(nil), target.Models...)
		cloned.modelSet = cloneTargetModelSet(target.modelSet)
		targets = append(targets, cloned)
	}
	return targets
}

func resolveTargets(
	primaryUpstream string,
	primaryProviderID string,
	primaryCredentialScope string,
	configured []TargetConfig,
	models ModelCatalog,
) ([]TargetRuntime, error) {
	if len(configured) > maxBackupTargets {
		return nil, fmt.Errorf("%w: at most %d backup Targets are allowed", ErrInvalidConfig, maxBackupTargets)
	}
	if len(configured) > 0 {
		if !targetTrustIDPattern.MatchString(primaryProviderID) {
			return nil, fmt.Errorf("%w: primary provider_id must be a valid trust identifier", ErrInvalidConfig)
		}
		if !targetTrustIDPattern.MatchString(primaryCredentialScope) {
			return nil, fmt.Errorf("%w: primary credential_scope must be a valid trust identifier", ErrInvalidConfig)
		}
	}
	targets := []TargetRuntime{{ID: PrimaryTargetID, Upstream: primaryUpstream}}
	seenIDs := map[string]struct{}{PrimaryTargetID: {}}
	seenUpstreams := map[string]struct{}{primaryUpstream: {}}
	for _, target := range configured {
		if target.ID != strings.TrimSpace(target.ID) || !targetIDPattern.MatchString(target.ID) {
			return nil, fmt.Errorf("%w: invalid Target ID %q", ErrInvalidConfig, target.ID)
		}
		if _, duplicate := seenIDs[target.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Target ID %q", ErrInvalidConfig, target.ID)
		}
		if !targetTrustIDPattern.MatchString(target.ProviderID) {
			return nil, fmt.Errorf("%w: Target %q provider_id must be a valid trust identifier", ErrInvalidConfig, target.ID)
		}
		if target.ProviderID != primaryProviderID {
			return nil, fmt.Errorf("%w: Target %q provider_id does not match primary", ErrInvalidConfig, target.ID)
		}
		if !targetTrustIDPattern.MatchString(target.CredentialScope) {
			return nil, fmt.Errorf("%w: Target %q credential_scope must be a valid trust identifier", ErrInvalidConfig, target.ID)
		}
		if target.CredentialScope != primaryCredentialScope {
			return nil, fmt.Errorf("%w: Target %q credential_scope does not match primary", ErrInvalidConfig, target.ID)
		}
		upstream, err := resolveUpstream(target.Upstream)
		if err != nil {
			return nil, fmt.Errorf("%w: Target %q: %v", ErrInvalidConfig, target.ID, err)
		}
		if _, duplicate := seenUpstreams[upstream]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Target upstream %q", ErrInvalidConfig, upstream)
		}
		if len(target.Models) == 0 {
			return nil, fmt.Errorf("%w: Target %q requires at least one model", ErrInvalidConfig, target.ID)
		}
		modelIDs := make([]string, 0, len(target.Models))
		modelSet := make(map[string]struct{}, len(target.Models))
		for _, model := range target.Models {
			if model == "" || model != strings.TrimSpace(model) {
				return nil, fmt.Errorf("%w: Target %q has an invalid model ID", ErrInvalidConfig, target.ID)
			}
			if _, ok := models[model]; !ok {
				return nil, fmt.Errorf("%w: Target %q references unknown model %q", ErrInvalidConfig, target.ID, model)
			}
			if _, duplicate := modelSet[model]; duplicate {
				return nil, fmt.Errorf("%w: Target %q duplicates model %q", ErrInvalidConfig, target.ID, model)
			}
			modelSet[model] = struct{}{}
			modelIDs = append(modelIDs, model)
		}
		seenIDs[target.ID] = struct{}{}
		seenUpstreams[upstream] = struct{}{}
		targets = append(targets, TargetRuntime{
			ID: target.ID, Upstream: upstream, Models: modelIDs, modelSet: modelSet,
		})
	}
	return targets, nil
}

func cloneTargetModelSet(source map[string]struct{}) map[string]struct{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]struct{}, len(source))
	for model := range source {
		cloned[model] = struct{}{}
	}
	return cloned
}
