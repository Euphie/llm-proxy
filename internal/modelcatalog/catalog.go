package modelcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

var (
	//go:embed data/catalog.json
	embeddedCatalog []byte
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Source struct {
	Name            string `json:"name"`
	Revision        string `json:"revision"`
	ModelsSHA256    string `json:"modelsSha256"`
	ProvidersSHA256 string `json:"providersSha256"`
	Retrieved       string `json:"retrieved"`
}

type Reference struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Model struct {
	ID                                string      `json:"id"`
	CanonicalID                       string      `json:"canonicalId"`
	APIIDs                            []string    `json:"apiIds"`
	Aliases                           []string    `json:"aliases"`
	CompatibilityAliases              []string    `json:"compatibilityAliases"`
	Provider                          string      `json:"provider"`
	Name                              string      `json:"name"`
	Family                            string      `json:"family"`
	ContextWindow                     *int64      `json:"context_window,omitempty"`
	MaxOutputTokens                   *int64      `json:"max_output_tokens,omitempty"`
	SupportsVision                    *bool       `json:"supports_vision,omitempty"`
	SupportsTools                     *bool       `json:"supports_tools,omitempty"`
	SupportsStructuredOutput          *bool       `json:"supports_structured_output,omitempty"`
	InputPriceMicroUSDPerMillion      *int64      `json:"input_price_micro_usd_per_million,omitempty"`
	OutputPriceMicroUSDPerMillion     *int64      `json:"output_price_micro_usd_per_million,omitempty"`
	CacheReadPriceMicroUSDPerMillion  *int64      `json:"cache_read_price_micro_usd_per_million,omitempty"`
	CacheWritePriceMicroUSDPerMillion *int64      `json:"cache_write_price_micro_usd_per_million,omitempty"`
	Lifecycle                         string      `json:"lifecycle"`
	ReleaseDate                       string      `json:"releaseDate,omitempty"`
	LastUpdated                       string      `json:"lastUpdated,omitempty"`
	References                        []Reference `json:"references"`
}

type Catalog struct {
	Source Source  `json:"source"`
	Models []Model `json:"models"`
}

func decodeCatalog(contents []byte) (Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode model catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Catalog{}, errors.New("decode model catalog: trailing JSON value")
		}
		return Catalog{}, fmt.Errorf("decode model catalog trailing data: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (catalog Catalog) Validate() error {
	if catalog.Source.Name == "" || catalog.Source.Retrieved == "" {
		return errors.New("model catalog source name and retrieved date are required")
	}
	for label, value := range map[string]string{
		"revision":         catalog.Source.Revision,
		"models digest":    catalog.Source.ModelsSHA256,
		"providers digest": catalog.Source.ProvidersSHA256,
	} {
		if !sha256Pattern.MatchString(value) {
			return fmt.Errorf("model catalog source %s must be a lowercase SHA-256", label)
		}
	}
	if len(catalog.Models) == 0 {
		return errors.New("model catalog must contain at least one model")
	}
	owners := make(map[string]string)
	previous := ""
	for _, model := range catalog.Models {
		if model.ID == "" || model.CanonicalID == "" || model.Provider == "" || model.Name == "" {
			return errors.New("model catalog entries require id, canonicalId, provider and name")
		}
		if previous != "" && strings.Compare(previous, model.CanonicalID) > 0 {
			return errors.New("model catalog canonical IDs must be sorted")
		}
		previous = model.CanonicalID
		if err := validateLimits(model); err != nil {
			return err
		}
		for _, identifier := range append(
			[]string{model.ID, model.CanonicalID},
			append(append(slices.Clone(model.APIIDs), model.Aliases...), model.CompatibilityAliases...)...,
		) {
			if identifier == "" || identifier != strings.TrimSpace(identifier) {
				return fmt.Errorf("model %s has an invalid identifier", model.CanonicalID)
			}
			key := strings.ToLower(identifier)
			if owner, found := owners[key]; found && owner != model.CanonicalID {
				return fmt.Errorf("duplicate model identifier %q for %s and %s", identifier, owner, model.CanonicalID)
			}
			owners[key] = model.CanonicalID
		}
	}
	return nil
}

func validateLimits(model Model) error {
	for label, value := range map[string]*int64{
		"context": model.ContextWindow,
		"output":  model.MaxOutputTokens,
	} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("model %s %s limit must be positive", model.CanonicalID, label)
		}
	}
	if model.ContextWindow != nil && model.MaxOutputTokens != nil && *model.MaxOutputTokens >= *model.ContextWindow {
		return fmt.Errorf("model %s output limit must be below context limit", model.CanonicalID)
	}
	for label, value := range map[string]*int64{
		"input":       model.InputPriceMicroUSDPerMillion,
		"output":      model.OutputPriceMicroUSDPerMillion,
		"cache read":  model.CacheReadPriceMicroUSDPerMillion,
		"cache write": model.CacheWritePriceMicroUSDPerMillion,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("model %s %s price must not be negative", model.CanonicalID, label)
		}
	}
	return nil
}

func cloneCatalog(catalog Catalog) Catalog {
	contents, err := json.Marshal(catalog)
	if err != nil {
		panic(err)
	}
	cloned, err := decodeCatalog(contents)
	if err != nil {
		panic(err)
	}
	return cloned
}
