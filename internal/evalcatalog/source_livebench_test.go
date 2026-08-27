package evalcatalog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/parquet-go/parquet-go"
)

func TestLiveBenchImportAggregatesExactModelsByDomainAndOverall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "livebench.parquet")
	rows := []liveBenchRow{
		{Model: "Alpha API", Score: 0.8, Category: "reasoning"},
		{Model: "Alpha API", Score: 0.6, Category: "reasoning"},
		{Model: "Alpha API", Score: 0.9, Category: "math"},
		{Model: "beta", Score: 0.5, Category: "coding"},
		{Model: "Unknown", Score: 1, Category: "language"},
	}
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), map[string]string{
		"Alpha API": "acme/alpha-2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importLiveBench(path, sourceFixture("livebench"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 5 || imported.SkippedUnmapped != 1 {
		t.Fatalf("imported=%+v", imported)
	}
	alphaGeneral := resultByModelDomain(t, imported.Results, "acme/alpha-2026", "general")
	if alphaGeneral.ScoreBPS != 7667 || alphaGeneral.Samples != 3 ||
		alphaGeneral.LowerBPS >= alphaGeneral.ScoreBPS || alphaGeneral.UpperBPS <= alphaGeneral.ScoreBPS {
		t.Fatalf("alpha general=%+v", alphaGeneral)
	}
	alphaReasoning := resultByModelDomain(t, imported.Results, "acme/alpha-2026", "reasoning")
	if alphaReasoning.ScoreBPS != 7000 || alphaReasoning.Samples != 2 {
		t.Fatalf("alpha reasoning=%+v", alphaReasoning)
	}
	if got := resultByModelDomain(t, imported.Results, "acme/beta-2026", "coding"); got.ScoreBPS != 5000 || got.Samples != 1 {
		t.Fatalf("beta coding=%+v", got)
	}
}

func TestLiveBenchImportRejectsInvalidScores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "livebench.parquet")
	if err := parquet.WriteFile(path, []liveBenchRow{{
		Model: "alpha", Score: 1.1, Category: "reasoning",
	}}); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importLiveBench(path, sourceFixture("livebench"), resolver); err == nil {
		t.Fatal("importLiveBench accepted a score outside 0..1")
	}
}

func TestLiveBenchImportIgnoresUnrelatedSchemaChanges(t *testing.T) {
	type upstreamRow struct {
		QuestionID string  `parquet:"question_id"`
		Task       string  `parquet:"task"`
		Model      string  `parquet:"model"`
		Score      float64 `parquet:"score"`
		Category   string  `parquet:"category"`
	}
	path := filepath.Join(t.TempDir(), "livebench.parquet")
	if err := parquet.WriteFile(path, []upstreamRow{{
		QuestionID: "02af5e41681a8e07", Task: "r1", Model: "alpha", Score: 0.8, Category: "reasoning",
	}}); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importLiveBench(path, sourceFixture("livebench"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || imported.Results[0].Samples != 1 {
		t.Fatalf("imported=%+v", imported)
	}
}

func sourceFixtureModelCatalog() modelcatalog.Catalog {
	return modelcatalog.Catalog{Models: []modelcatalog.Model{
		{ID: "alpha", CanonicalID: "acme/alpha-2026", APIIDs: []string{"alpha-api"}},
		{ID: "beta", CanonicalID: "acme/beta-2026"},
	}}
}

func sourceFixture(id string) Source {
	return Source{
		ID: id, Name: id, URL: "https://example.test/" + id, License: "MIT", Version: "v1",
		RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		SHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func resultByModelDomain(t *testing.T, results []Result, modelID, domain string) Result {
	t.Helper()
	for _, result := range results {
		if result.ModelID == modelID && result.Domain == domain {
			return result
		}
	}
	t.Fatalf("result %s/%s not found in %+v", modelID, domain, results)
	return Result{}
}
