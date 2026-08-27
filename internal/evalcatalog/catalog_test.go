package evalcatalog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeCatalogRejectsUnknownAndTrailingJSON(t *testing.T) {
	contents := marshalCatalogFixture(t, catalogFixture("fixture"))
	withUnknown := strings.Replace(string(contents), `"revision":`, `"unexpected":true,"revision":`, 1)
	if _, err := decodeCatalog([]byte(withUnknown)); err == nil {
		t.Fatal("decodeCatalog accepted an unknown field")
	}
	if _, err := decodeCatalog(append(contents, []byte(`{}`)...)); err == nil {
		t.Fatal("decodeCatalog accepted a trailing JSON value")
	}
}

func TestCatalogValidateRejectsDuplicateSourcesAndResults(t *testing.T) {
	catalog := catalogFixture("duplicates")
	catalog.Sources = append(catalog.Sources, catalog.Sources[0])
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate source") {
		t.Fatalf("duplicate source error=%v", err)
	}

	catalog = catalogFixture("duplicates")
	catalog.Results = append(catalog.Results, catalog.Results[0])
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate result") {
		t.Fatalf("duplicate result error=%v", err)
	}
}

func TestCatalogValidateRejectsInvalidMetricsAndModelIDs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{
			name: "non canonical model id",
			mutate: func(c *Catalog) {
				c.Results[0].ModelID = "alpha"
			},
			want: "canonical model id",
		},
		{
			name: "pairwise without baseline",
			mutate: func(c *Catalog) {
				c.Results[0].BaselineID = ""
			},
			want: "baseline",
		},
		{
			name: "score outside basis points",
			mutate: func(c *Catalog) {
				c.Results[0].ScoreBPS = 10_001
			},
			want: "score_bps",
		},
		{
			name: "invalid confidence interval",
			mutate: func(c *Catalog) {
				c.Results[0].LowerBPS = 8_000
				c.Results[0].UpperBPS = 7_000
			},
			want: "confidence interval",
		},
		{
			name: "bounded with baseline",
			mutate: func(c *Catalog) {
				c.Results[0].Metric = MetricBounded
			},
			want: "must not set baseline",
		},
		{
			name: "rank without rank",
			mutate: func(c *Catalog) {
				c.Results[0].Metric = MetricRankOnly
				c.Results[0].BaselineID = ""
				c.Results[0].ScoreBPS = 0
				c.Results[0].LowerBPS = 0
				c.Results[0].UpperBPS = 0
				c.Results[0].Samples = 0
			},
			want: "rank",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := catalogFixture(test.name)
			test.mutate(&catalog)
			if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error=%v, want containing %q", err, test.want)
			}
		})
	}
}

func catalogFixture(seed string) Catalog {
	digest := strings.Repeat("a", 64)
	return Catalog{
		SchemaVersion: 1,
		Revision:      digest,
		RetrievedAt:   time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		Sources: []Source{{
			ID:          "livebench",
			Name:        "LiveBench",
			URL:         "https://github.com/LiveBench/LiveBench",
			License:     "Apache-2.0",
			Version:     seed,
			RetrievedAt: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
			SHA256:      digest,
		}},
		Results: []Result{{
			SourceID:       "livebench",
			Benchmark:      "livebench-reasoning",
			Domain:         "reasoning",
			ModelID:        "acme/alpha",
			BaselineID:     "acme/baseline",
			Metric:         MetricPairwise,
			ScoreBPS:       6_000,
			LowerBPS:       5_500,
			UpperBPS:       6_500,
			Samples:        100,
			SettingsSHA256: digest,
		}},
	}
}

func marshalCatalogFixture(t *testing.T, catalog Catalog) []byte {
	t.Helper()
	contents, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
