package profile

import (
	"fmt"
	"strings"
)

type ModelCapabilityConfig struct {
	ID              string `json:"id"`
	ContextWindow   *int   `json:"context_window,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
	SupportsVision  *bool  `json:"supports_vision"`
}

type ModelCapability struct {
	ID                 string
	ContextWindow      int
	HasContextWindow   bool
	MaxOutputTokens    int
	HasMaxOutputTokens bool
	SupportsVision     bool
}

type ModelCatalog map[string]ModelCapability

type UnlistedModelPolicy string

const (
	UnlistedModelBypass   UnlistedModelPolicy = "bypass"
	UnlistedModelEnhance  UnlistedModelPolicy = "enhance"
	maxBrowserSafeInteger int64               = 9_007_199_254_740_991
)

func resolveModelCatalog(configs []ModelCapabilityConfig) (ModelCatalog, error) {
	catalog := make(ModelCatalog, len(configs))
	for _, config := range configs {
		if config.ID == "" || strings.TrimSpace(config.ID) != config.ID {
			return nil, fmt.Errorf("%w: model capability ID is required without surrounding whitespace", ErrInvalidConfig)
		}
		if _, exists := catalog[config.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate model capability ID %q", ErrInvalidConfig, config.ID)
		}
		if config.SupportsVision == nil {
			return nil, fmt.Errorf("%w: model capability %q supports_vision is required", ErrInvalidConfig, config.ID)
		}
		if config.ContextWindow != nil && *config.ContextWindow <= 0 {
			return nil, fmt.Errorf("%w: model capability %q context window must be positive", ErrInvalidConfig, config.ID)
		}
		if config.ContextWindow != nil && int64(*config.ContextWindow) > maxBrowserSafeInteger {
			return nil, fmt.Errorf("%w: model capability %q context window exceeds the browser safe integer limit", ErrInvalidConfig, config.ID)
		}
		if config.MaxOutputTokens != nil && *config.MaxOutputTokens <= 0 {
			return nil, fmt.Errorf("%w: model capability %q max output tokens must be positive", ErrInvalidConfig, config.ID)
		}
		if config.MaxOutputTokens != nil && int64(*config.MaxOutputTokens) > maxBrowserSafeInteger {
			return nil, fmt.Errorf("%w: model capability %q max output tokens exceeds the browser safe integer limit", ErrInvalidConfig, config.ID)
		}
		if config.ContextWindow != nil && config.MaxOutputTokens != nil &&
			*config.MaxOutputTokens >= *config.ContextWindow {
			return nil, fmt.Errorf("%w: model capability %q max output tokens must be less than context window", ErrInvalidConfig, config.ID)
		}

		capability := ModelCapability{
			ID:             config.ID,
			SupportsVision: *config.SupportsVision,
		}
		if config.ContextWindow != nil {
			capability.ContextWindow = *config.ContextWindow
			capability.HasContextWindow = true
		}
		if config.MaxOutputTokens != nil {
			capability.MaxOutputTokens = *config.MaxOutputTokens
			capability.HasMaxOutputTokens = true
		}
		catalog[config.ID] = capability
	}
	return catalog, nil
}

func (c ModelCatalog) Lookup(id string) (ModelCapability, bool) {
	capability, ok := c[id]
	return capability, ok
}
