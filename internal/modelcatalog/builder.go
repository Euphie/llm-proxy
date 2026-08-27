package modelcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

type feedModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type feedLimit struct {
	Context *int64 `json:"context"`
	Output  *int64 `json:"output"`
}

type feedCost struct {
	Input           *float64   `json:"input"`
	Output          *float64   `json:"output"`
	CacheRead       *float64   `json:"cache_read"`
	CacheWrite      *float64   `json:"cache_write"`
	ContextOver200K *feedCost  `json:"context_over_200k"`
	Tiers           []feedCost `json:"tiers"`
}

type feedModel struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Family           string         `json:"family"`
	ToolCall         *bool          `json:"tool_call"`
	StructuredOutput *bool          `json:"structured_output"`
	Status           string         `json:"status"`
	ReleaseDate      string         `json:"release_date"`
	LastUpdated      string         `json:"last_updated"`
	Modalities       feedModalities `json:"modalities"`
	Limit            feedLimit      `json:"limit"`
	Cost             *feedCost      `json:"cost"`
}

type feedProvider struct {
	Name   string               `json:"name"`
	Models map[string]feedModel `json:"models"`
}

type providerModel struct {
	ProviderID string
	Key        string
	Model      feedModel
}

var compatibilityAliasesByCanonicalID = map[string][]string{
	"deepseek/deepseek-v4-flash": {"azure-ds-v4-flash"},
	"deepseek/deepseek-v4-pro":   {"azure-ds-v4-pro"},
	"zhipuai/glm-5.2":            {"claude-glm-5.2"},
}

func BuildFromFeeds(modelsContents, providersContents []byte, retrieved time.Time) (Catalog, error) {
	var canonical map[string]feedModel
	if err := decodeFeed(modelsContents, &canonical); err != nil {
		return Catalog{}, fmt.Errorf("decode Models.dev models feed: %w", err)
	}
	var providers map[string]feedProvider
	if err := decodeFeed(providersContents, &providers); err != nil {
		return Catalog{}, fmt.Errorf("decode Models.dev providers feed: %w", err)
	}
	index := indexProviderModels(providers)
	models := make([]Model, 0, len(canonical))
	for canonicalID, model := range canonical {
		known := providerEntries(canonicalID, providers, index)
		if !eligibleFeedModel(model, known) {
			continue
		}
		compact, err := compactFeedModel(canonicalID, model, providers, known)
		if err != nil {
			return Catalog{}, err
		}
		models = append(models, compact)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].CanonicalID < models[j].CanonicalID
	})
	modelsDigest := sha256.Sum256(modelsContents)
	providersDigest := sha256.Sum256(providersContents)
	revisionHash := sha256.New()
	revisionHash.Write([]byte("models.dev/catalog/v1\x00"))
	revisionHash.Write([]byte("models.json\x00" + hex.EncodeToString(modelsDigest[:]) + "\x00"))
	revisionHash.Write([]byte("api.json\x00" + hex.EncodeToString(providersDigest[:]) + "\x00"))
	catalog := Catalog{
		Source: Source{
			Name:            "Models.dev",
			Revision:        hex.EncodeToString(revisionHash.Sum(nil)),
			ModelsSHA256:    hex.EncodeToString(modelsDigest[:]),
			ProvidersSHA256: hex.EncodeToString(providersDigest[:]),
			Retrieved:       retrieved.UTC().Format("2006-01-02"),
		},
		Models: models,
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, fmt.Errorf("validate refreshed model catalog: %w", err)
	}
	return catalog, nil
}

func decodeFeed(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

func eligibleFeedModel(model feedModel, known []providerModel) bool {
	if model.ToolCall == nil || !*model.ToolCall || !contains(model.Modalities.Input, "text") || !contains(model.Modalities.Output, "text") {
		return false
	}
	if len(known) == 0 {
		return model.Status != "deprecated"
	}
	for _, entry := range known {
		if entry.Model.Status != "deprecated" {
			return true
		}
	}
	return false
}

func compactFeedModel(canonicalID string, canonical feedModel, providers map[string]feedProvider, known []providerModel) (Model, error) {
	providerID, id := splitCanonicalID(canonicalID)
	provider := providers[providerID]
	direct, directFound := provider.Models[id]
	modelID := id
	if directFound && direct.ID != "" {
		modelID = direct.ID
	}
	apiIDs := make([]string, 0, len(known)*2+1)
	for _, entry := range known {
		if entry.Key != canonicalID {
			apiIDs = append(apiIDs, entry.Key)
		}
		apiIDs = append(apiIDs, entry.Model.ID)
	}
	if len(known) == 0 {
		apiIDs = append(apiIDs, id)
	}
	inputPrice, err := conservativePrice(priceCost(directFound, direct.Cost, canonical.Cost), "input", canonicalID)
	if err != nil {
		return Model{}, err
	}
	outputPrice, err := conservativePrice(priceCost(directFound, direct.Cost, canonical.Cost), "output", canonicalID)
	if err != nil {
		return Model{}, err
	}
	cacheReadPrice, err := conservativePrice(priceCost(directFound, direct.Cost, canonical.Cost), "cache_read", canonicalID)
	if err != nil {
		return Model{}, err
	}
	cacheWritePrice, err := conservativePrice(priceCost(directFound, direct.Cost, canonical.Cost), "cache_write", canonicalID)
	if err != nil {
		return Model{}, err
	}
	providerName := provider.Name
	if providerName == "" {
		providerName = providerID
	}
	name := canonical.Name
	if name == "" {
		name = id
	}
	family := canonical.Family
	if family == "" {
		family = id
	}
	model := Model{
		ID:                                modelID,
		CanonicalID:                       canonicalID,
		APIIDs:                            sortedUnique(apiIDs),
		Aliases:                           []string{canonicalID},
		Provider:                          providerName,
		Name:                              name,
		Family:                            family,
		ContextWindow:                     cloneInt64(canonical.Limit.Context),
		SupportsVision:                    boolPointer(contains(canonical.Modalities.Input, "image")),
		SupportsTools:                     firstBool(directFound, direct.ToolCall, canonical.ToolCall),
		SupportsStructuredOutput:          firstBool(directFound, direct.StructuredOutput, canonical.StructuredOutput),
		InputPriceMicroUSDPerMillion:      inputPrice,
		OutputPriceMicroUSDPerMillion:     outputPrice,
		CacheReadPriceMicroUSDPerMillion:  cacheReadPrice,
		CacheWritePriceMicroUSDPerMillion: cacheWritePrice,
		Lifecycle:                         feedLifecycle(canonical.Status, known),
		ReleaseDate:                       canonical.ReleaseDate,
		LastUpdated:                       canonical.LastUpdated,
		References:                        []Reference{},
	}
	if canonical.Limit.Output != nil && (canonical.Limit.Context == nil || *canonical.Limit.Output < *canonical.Limit.Context) {
		model.MaxOutputTokens = cloneInt64(canonical.Limit.Output)
	}
	model.CompatibilityAliases = append(
		[]string(nil),
		compatibilityAliasesByCanonicalID[canonicalID]...,
	)
	if canonicalID == "zhipuai/glm-5.2" {
		model.References = []Reference{{
			Name: "Z.AI " + name,
			URL:  "https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2",
		}}
	}
	return model, nil
}

func indexProviderModels(providers map[string]feedProvider) map[string][]providerModel {
	index := make(map[string][]providerModel)
	for providerID, provider := range providers {
		for key, model := range provider.Models {
			for _, candidate := range []string{key, model.ID} {
				if !strings.Contains(candidate, "/") {
					continue
				}
				index[candidate] = append(index[candidate], providerModel{ProviderID: providerID, Key: key, Model: model})
			}
		}
	}
	return index
}

func providerEntries(canonicalID string, providers map[string]feedProvider, index map[string][]providerModel) []providerModel {
	providerID, id := splitCanonicalID(canonicalID)
	entries := append([]providerModel(nil), index[canonicalID]...)
	if model, found := providers[providerID].Models[id]; found {
		entries = append(entries, providerModel{ProviderID: providerID, Key: id, Model: model})
	}
	seen := make(map[string]struct{})
	result := make([]providerModel, 0, len(entries))
	for _, entry := range entries {
		key := entry.ProviderID + "\x00" + entry.Key
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, entry)
	}
	return result
}

func conservativePrice(cost *feedCost, field, canonicalID string) (*int64, error) {
	if cost == nil {
		return nil, nil
	}
	values := make([]*float64, 0, len(cost.Tiers)+2)
	values = append(values, costField(cost, field))
	if cost.ContextOver200K != nil {
		values = append(values, costField(cost.ContextOver200K, field))
	}
	for index := range cost.Tiers {
		values = append(values, costField(&cost.Tiers[index], field))
	}
	maximum := -1.0
	for _, value := range values {
		if value == nil {
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
			return nil, fmt.Errorf("model %s %s price must be a non-negative finite number", canonicalID, field)
		}
		maximum = math.Max(maximum, *value)
	}
	if maximum < 0 {
		return nil, nil
	}
	micro := math.Ceil(maximum * 1_000_000)
	if micro > math.MaxInt64 {
		return nil, fmt.Errorf("model %s %s price is too large", canonicalID, field)
	}
	value := int64(micro)
	return &value, nil
}

func priceCost(directFound bool, direct, canonical *feedCost) *feedCost {
	if directFound && direct != nil {
		return direct
	}
	return canonical
}

func costField(cost *feedCost, field string) *float64 {
	switch field {
	case "input":
		return cost.Input
	case "output":
		return cost.Output
	case "cache_read":
		return cost.CacheRead
	case "cache_write":
		return cost.CacheWrite
	default:
		return nil
	}
}

func feedLifecycle(canonicalStatus string, known []providerModel) string {
	preview := func(status string) bool {
		return status == "alpha" || status == "beta" || status == "preview"
	}
	if len(known) == 0 {
		if preview(canonicalStatus) {
			return "preview"
		}
		return "stable"
	}
	active := make([]string, 0, len(known))
	for _, entry := range known {
		if entry.Model.Status != "deprecated" {
			active = append(active, entry.Model.Status)
		}
	}
	if len(active) > 0 {
		allPreview := true
		for _, status := range active {
			allPreview = allPreview && preview(status)
		}
		if allPreview {
			return "preview"
		}
	}
	return "stable"
}

func firstBool(directFound bool, direct, canonical *bool) *bool {
	if directFound && direct != nil {
		return boolPointer(*direct)
	}
	if canonical == nil {
		return nil
	}
	return boolPointer(*canonical)
}

func splitCanonicalID(value string) (string, string) {
	provider, id, found := strings.Cut(value, "/")
	if !found {
		return value, value
	}
	return provider, id
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func boolPointer(value bool) *bool { return &value }
