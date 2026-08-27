package modelcatalog

import (
	"encoding/json"
	"testing"
	"time"
)

const canonicalFeedFixture = `{
  "zhipuai/glm-5.2": {
    "id": "zhipuai/glm-5.2",
    "name": "GLM-5.2",
    "family": "glm",
    "tool_call": true,
    "structured_output": true,
    "release_date": "2026-06-13",
    "last_updated": "2026-07-01",
    "modalities": {"input": ["text"], "output": ["text"]},
    "limit": {"context": 1000000, "output": 131072},
    "cost": {"input": 0.8, "output": 2.4, "context_over_200k": {"input": 1.2, "output": 3.1}}
  }
}`

const providerFeedFixture = `{
  "zhipuai": {
    "id": "zhipuai",
    "name": "Zhipu AI",
    "models": {
      "glm-5.2": {
        "id": "glm-5.2",
        "tool_call": true,
        "structured_output": true,
        "cost": {"input": 0.9, "output": 2.5, "cache_read": 0.09, "cache_write": 1.125,
          "tiers": [{"input": 1.5, "output": 4.0, "cache_read": 0.15, "cache_write": 1.875}]}
      }
    }
  }
}`

func TestBuildFromFeedsMatchesCatalogSemantics(t *testing.T) {
	catalog, err := BuildFromFeeds(
		[]byte(canonicalFeedFixture),
		[]byte(providerFeedFixture),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models=%d", len(catalog.Models))
	}
	model := catalog.Models[0]
	if model.ID != "glm-5.2" || model.CanonicalID != "zhipuai/glm-5.2" {
		t.Fatalf("identity=%+v", model)
	}
	if len(model.CompatibilityAliases) != 1 || model.CompatibilityAliases[0] != "claude-glm-5.2" {
		t.Fatalf("compatibility aliases=%v", model.CompatibilityAliases)
	}
	if model.InputPriceMicroUSDPerMillion == nil || *model.InputPriceMicroUSDPerMillion != 1_500_000 {
		t.Fatalf("input price=%v", model.InputPriceMicroUSDPerMillion)
	}
	if model.OutputPriceMicroUSDPerMillion == nil || *model.OutputPriceMicroUSDPerMillion != 4_000_000 {
		t.Fatalf("output price=%v", model.OutputPriceMicroUSDPerMillion)
	}
	contents, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var prices map[string]any
	if err := json.Unmarshal(contents, &prices); err != nil {
		t.Fatal(err)
	}
	if prices["cache_read_price_micro_usd_per_million"] != float64(150_000) ||
		prices["cache_write_price_micro_usd_per_million"] != float64(1_875_000) {
		t.Fatalf("cache prices=%s", contents)
	}
	if model.SupportsStructuredOutput == nil || !*model.SupportsStructuredOutput {
		t.Fatalf("structured output=%v", model.SupportsStructuredOutput)
	}
	if catalog.Source.Retrieved != "2026-08-02" || !sha256Pattern.MatchString(catalog.Source.Revision) {
		t.Fatalf("source=%+v", catalog.Source)
	}
}

func TestKnownGatewayCompatibilityAliases(t *testing.T) {
	for canonicalID, want := range map[string]string{
		"deepseek/deepseek-v4-flash": "azure-ds-v4-flash",
		"deepseek/deepseek-v4-pro":   "azure-ds-v4-pro",
		"zhipuai/glm-5.2":            "claude-glm-5.2",
	} {
		aliases := compatibilityAliasesByCanonicalID[canonicalID]
		if len(aliases) != 1 || aliases[0] != want {
			t.Fatalf("compatibility aliases for %s=%v, want %q", canonicalID, aliases, want)
		}
	}
}

func TestBuildFromFeedsRejectsIneligibleOrMalformedData(t *testing.T) {
	if _, err := BuildFromFeeds([]byte(`{"acme/no-tools":{"tool_call":false,"modalities":{"input":["text"],"output":["text"]}}}`), []byte(`{}`), time.Now()); err == nil {
		t.Fatal("BuildFromFeeds accepted an empty eligible catalog")
	}
	if _, err := BuildFromFeeds([]byte(`{`), []byte(`{}`), time.Now()); err == nil {
		t.Fatal("BuildFromFeeds accepted malformed JSON")
	}
	if _, err := BuildFromFeeds([]byte(canonicalFeedFixture+`{}`), []byte(providerFeedFixture), time.Now()); err == nil {
		t.Fatal("BuildFromFeeds accepted a trailing JSON value")
	}
}
