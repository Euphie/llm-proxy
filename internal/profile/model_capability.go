package profile

import (
	"fmt"
	"strings"
)

type ModelCapabilityConfig struct {
	ID                                string `json:"id"`
	ContextWindow                     *int   `json:"context_window,omitempty"`
	MaxOutputTokens                   *int   `json:"max_output_tokens,omitempty"`
	SupportsVision                    *bool  `json:"supports_vision"`
	SupportsTools                     *bool  `json:"supports_tools,omitempty"`
	SupportsStructuredOutput          *bool  `json:"supports_structured_output,omitempty"`
	InputPriceMicroUSDPerMillion      *int64 `json:"input_price_micro_usd_per_million,omitempty"`
	OutputPriceMicroUSDPerMillion     *int64 `json:"output_price_micro_usd_per_million,omitempty"`
	CacheReadPriceMicroUSDPerMillion  *int64 `json:"cache_read_price_micro_usd_per_million,omitempty"`
	CacheWritePriceMicroUSDPerMillion *int64 `json:"cache_write_price_micro_usd_per_million,omitempty"`
}

type ModelCapability struct {
	ID                                string
	ContextWindow                     int
	HasContextWindow                  bool
	MaxOutputTokens                   int
	HasMaxOutputTokens                bool
	SupportsVision                    bool
	SupportsTools                     bool
	HasSupportsTools                  bool
	SupportsStructuredOutput          bool
	HasSupportsStructuredOutput       bool
	InputPriceMicroUSDPerMillion      int64
	HasInputPrice                     bool
	OutputPriceMicroUSDPerMillion     int64
	HasOutputPrice                    bool
	CacheReadPriceMicroUSDPerMillion  int64
	HasCacheReadPrice                 bool
	CacheWritePriceMicroUSDPerMillion int64
	HasCacheWritePrice                bool
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
		if err := validatePrice(config.ID, "input", config.InputPriceMicroUSDPerMillion); err != nil {
			return nil, err
		}
		if err := validatePrice(config.ID, "output", config.OutputPriceMicroUSDPerMillion); err != nil {
			return nil, err
		}
		if err := validatePrice(config.ID, "cache read", config.CacheReadPriceMicroUSDPerMillion); err != nil {
			return nil, err
		}
		if err := validatePrice(config.ID, "cache write", config.CacheWritePriceMicroUSDPerMillion); err != nil {
			return nil, err
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
		if config.SupportsTools != nil {
			capability.SupportsTools = *config.SupportsTools
			capability.HasSupportsTools = true
		}
		if config.SupportsStructuredOutput != nil {
			capability.SupportsStructuredOutput = *config.SupportsStructuredOutput
			capability.HasSupportsStructuredOutput = true
		}
		if config.InputPriceMicroUSDPerMillion != nil {
			capability.InputPriceMicroUSDPerMillion = *config.InputPriceMicroUSDPerMillion
			capability.HasInputPrice = true
		}
		if config.OutputPriceMicroUSDPerMillion != nil {
			capability.OutputPriceMicroUSDPerMillion = *config.OutputPriceMicroUSDPerMillion
			capability.HasOutputPrice = true
		}
		if config.CacheReadPriceMicroUSDPerMillion != nil {
			capability.CacheReadPriceMicroUSDPerMillion = *config.CacheReadPriceMicroUSDPerMillion
			capability.HasCacheReadPrice = true
		}
		if config.CacheWritePriceMicroUSDPerMillion != nil {
			capability.CacheWritePriceMicroUSDPerMillion = *config.CacheWritePriceMicroUSDPerMillion
			capability.HasCacheWritePrice = true
		}
		catalog[config.ID] = capability
	}
	return catalog, nil
}

func validatePrice(modelID, kind string, price *int64) error {
	if price == nil {
		return nil
	}
	if *price < 0 {
		return fmt.Errorf("%w: model capability %q %s price must not be negative", ErrInvalidConfig, modelID, kind)
	}
	if *price > maxBrowserSafeInteger {
		return fmt.Errorf("%w: model capability %q %s price exceeds the browser safe integer limit", ErrInvalidConfig, modelID, kind)
	}
	return nil
}

func (c ModelCatalog) Lookup(id string) (ModelCapability, bool) {
	capability, ok := c[id]
	return capability, ok
}
