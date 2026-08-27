package evalcatalog

import (
	"path/filepath"
	"testing"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/parquet-go/parquet-go"
)

func TestArenaImportUsesOnlyOverallRanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arena.parquet")
	rows := []arenaRow{
		{ModelName: "alpha", Rank: 1, Category: "overall", LeaderboardPublishDate: "2026-08-01"},
		{ModelName: "beta", Rank: 2, Category: "overall", LeaderboardPublishDate: "2026-08-01"},
		{ModelName: "alpha", Rank: 9, Category: "coding", LeaderboardPublishDate: "2026-08-01"},
		{ModelName: "unknown", Rank: 3, Category: "overall", LeaderboardPublishDate: "2026-08-01"},
	}
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importArena(path, sourceFixture("arena"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || imported.SkippedUnmapped != 1 || len(imported.Results) != 2 {
		t.Fatalf("imported=%+v", imported)
	}
	alpha := resultByModelDomain(t, imported.Results, "acme/alpha-2026", "general")
	if alpha.Metric != MetricRankOnly || alpha.Rank != 1 || alpha.ScoreBPS != 0 {
		t.Fatalf("alpha=%+v", alpha)
	}
}

func TestArenaImportRejectsDuplicateOverallRanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arena.parquet")
	if err := parquet.WriteFile(path, []arenaRow{
		{ModelName: "alpha", Rank: 1, Category: "overall"},
		{ModelName: "beta", Rank: 1, Category: "overall"},
	}); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importArena(path, sourceFixture("arena"), resolver); err == nil {
		t.Fatal("importArena accepted duplicate ranks")
	}
}

func TestArenaImportMapsDeclaredServingVariantsWithoutFamilyInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arena.parquet")
	rows := []arenaRow{
		{ModelName: "claude-opus-5-high", Rank: 7, Category: "overall"},
		{ModelName: "claude-opus-5-max", Rank: 8, Category: "overall"},
		{ModelName: "glm-5.2-max", Rank: 31, Category: "overall"},
		{ModelName: "claude-sonnet-5-high", Rank: 41, Category: "overall"},
		{ModelName: "claude-opus-6-high", Rank: 42, Category: "overall"},
	}
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	models := modelcatalog.Catalog{Models: []modelcatalog.Model{
		{ID: "claude-opus-5", CanonicalID: "anthropic/claude-opus-5"},
		{ID: "claude-sonnet-5", CanonicalID: "anthropic/claude-sonnet-5"},
		{ID: "glm-5.2", CanonicalID: "zhipuai/glm-5.2"},
		{ID: "claude-opus-6", CanonicalID: "anthropic/claude-opus-6"},
	}}
	resolver, err := NewCanonicalResolver(models, nil)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importArena(path, sourceFixture("arena"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, result := range imported.Results {
		counts[result.ModelID]++
	}
	if imported.Imported != 4 || imported.SkippedUnmapped != 1 {
		t.Fatalf("imported=%+v", imported)
	}
	if counts["anthropic/claude-opus-5"] != 2 ||
		counts["anthropic/claude-sonnet-5"] != 1 ||
		counts["zhipuai/glm-5.2"] != 1 {
		t.Fatalf("counts=%v results=%+v", counts, imported.Results)
	}
	if counts["anthropic/claude-opus-6"] != 0 {
		t.Fatalf("undeclared family variant was imported: %+v", imported.Results)
	}
}
