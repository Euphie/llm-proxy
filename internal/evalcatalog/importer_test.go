package evalcatalog

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildCatalogImportsFivePinnedOpenEvaluationFormats(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"livebench.csv": "Model,Score,Samples\nAlpha,82.5,200\nIgnored,99,200\n",
		"bfcl.csv":      "Model,Overall Acc\nAlpha FC,71.2\n",
		"vlm.csv":       "Model,MMMU\nAlpha Vision,64.0\n",
		"arena.csv":     "Rank,Model\n1,Alpha Chat\n",
		"swe.json":      `{"leaderboards":[{"name":"Verified","results":[{"name":"Alpha Agent","resolved":48.6}]}]}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("a", 64)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := func(id, adapter, path string, rules ...ImportRule) ImportSource {
		contents := []byte(files[path])
		hash := sha256.Sum256(contents)
		return ImportSource{
			ID: id, Name: id, URL: "https://example.com/" + id, License: "Apache-2.0",
			Version: "pinned-v1", RetrievedAt: now, Path: path,
			SHA256: fmt.Sprintf("%x", hash), Adapter: adapter,
			ModelMap: map[string]string{
				"Alpha": "acme/alpha", "Alpha FC": "acme/alpha-fc",
				"Alpha Vision": "acme/alpha-vision", "Alpha Chat": "acme/alpha-chat",
				"Alpha Agent": "acme/alpha-agent",
			},
			Rules: rules,
		}
	}
	bounded := func(benchmark, domain, scoreColumn string, samples int) ImportRule {
		rule := ImportRule{
			Benchmark: benchmark, Domain: domain, Metric: MetricBounded,
			ModelColumn: "Model", ScoreColumn: scoreColumn,
			FixedSamples: samples, ScoreScale: ScoreScalePercent, SettingsSHA256: digest,
		}
		if samples == 0 {
			rule.SamplesColumn = "Samples"
		}
		return rule
	}
	manifest := ImportManifest{
		SchemaVersion: 1, RetrievedAt: now,
		Sources: []ImportSource{
			source("livebench", AdapterLiveBenchCSV, "livebench.csv", bounded("livebench-reasoning", "reasoning", "Score", 0)),
			source("bfcl", AdapterBFCLCSV, "bfcl.csv", bounded("bfcl-v4", "tool_use", "Overall Acc", 1000)),
			source("vlmevalkit", AdapterVLMEvalKitCSV, "vlm.csv", bounded("mmmu", "vision", "MMMU", 900)),
			source("arena", AdapterArenaCSV, "arena.csv", ImportRule{
				Benchmark: "chatbot-arena", Domain: "general", Metric: MetricRankOnly,
				ModelColumn: "Model", RankColumn: "Rank", SettingsSHA256: digest,
			}),
			func() ImportSource {
				item := source("swebench", AdapterSWEBenchJSON, "swe.json", ImportRule{
					Benchmark: "swe-bench-verified", Domain: "coding", Metric: MetricBounded,
					Leaderboard: "Verified", ModelColumn: "name", ScoreColumn: "resolved",
					FixedSamples: 500, ScoreScale: ScoreScalePercent, SettingsSHA256: digest,
				})
				item.ScaffoldMap = map[string]string{"Alpha Agent": "SWE-agent 1.0"}
				item.ScaffoldSHA256Map = map[string]string{"Alpha Agent": digest}
				return item
			}(),
		},
	}

	catalog, report, err := BuildCatalog(manifest, directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Sources) != 5 || len(catalog.Results) != 5 || report.Imported != 5 || report.SkippedUnmapped != 1 {
		t.Fatalf("catalog=%+v report=%+v", catalog, report)
	}
	if catalog.Results[0].SourceID != "arena" {
		t.Fatalf("results are not deterministic: %+v", catalog.Results)
	}
	bySource := make(map[string]Result)
	for _, result := range catalog.Results {
		bySource[result.SourceID] = result
	}
	if got := bySource["livebench"]; got.ScoreBPS != 8250 || got.LowerBPS >= got.ScoreBPS || got.UpperBPS <= got.ScoreBPS || got.Samples != 200 {
		t.Fatalf("livebench result=%+v", got)
	}
	if got := bySource["arena"]; got.Metric != MetricRankOnly || got.Rank != 1 {
		t.Fatalf("arena result=%+v", got)
	}
	if got := bySource["swebench"]; got.Scaffold != "SWE-agent 1.0" || got.ScaffoldSHA256 != digest || got.ModelID != "acme/alpha-agent" {
		t.Fatalf("swebench result=%+v", got)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("generated catalog invalid: %v", err)
	}
}

func TestBuildCatalogRejectsUnpinnedInputAndMissingSWEBenchScaffold(t *testing.T) {
	directory := t.TempDir()
	contents := `{"leaderboards":[{"name":"Verified","results":[{"name":"Agent","resolved":50}]}]}`
	if err := os.WriteFile(filepath.Join(directory, "swe.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	base := ImportSource{
		ID: "swebench", Name: "SWE-bench", URL: "https://github.com/SWE-bench/swe-bench.github.io",
		License: "MIT", Version: "v1", RetrievedAt: time.Now(), Path: "swe.json",
		Adapter: AdapterSWEBenchJSON, SHA256: strings.Repeat("b", 64),
		ModelMap: map[string]string{"Agent": "acme/alpha"},
		Rules: []ImportRule{{
			Benchmark: "verified", Domain: "coding", Metric: MetricBounded,
			Leaderboard: "Verified", ModelColumn: "name", ScoreColumn: "resolved",
			FixedSamples: 500, ScoreScale: ScoreScalePercent, SettingsSHA256: digest,
		}},
	}
	manifest := ImportManifest{SchemaVersion: 1, RetrievedAt: time.Now(), Sources: []ImportSource{base}}
	if _, _, err := BuildCatalog(manifest, directory); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("unpinned error=%v", err)
	}

	hash := sha256.Sum256([]byte(contents))
	manifest.Sources[0].SHA256 = fmt.Sprintf("%x", hash)
	if _, _, err := BuildCatalog(manifest, directory); err == nil || !strings.Contains(err.Error(), "scaffold") {
		t.Fatalf("missing scaffold error=%v", err)
	}
}

func TestDecodeImportManifestRejectsUnknownFields(t *testing.T) {
	raw := `{"schema_version":1,"retrieved_at":"2026-08-05T00:00:00Z","sources":[],"unknown":true}`
	if _, err := DecodeImportManifest(strings.NewReader(raw)); err == nil {
		t.Fatal("DecodeImportManifest accepted an unknown field")
	}
}

func TestBuildCatalogRevisionIgnoresManifestRetrievalTime(t *testing.T) {
	directory := t.TempDir()
	contents := []byte("Model,Score\nAlpha,80\n")
	if err := os.WriteFile(filepath.Join(directory, "scores.csv"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	settings := strings.Repeat("d", 64)
	firstTime := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	manifest := ImportManifest{
		SchemaVersion: 1,
		RetrievedAt:   firstTime,
		Sources: []ImportSource{{
			ID: "fixture", Name: "Fixture", URL: "https://example.test/fixture", License: "MIT",
			Version: "v1", RetrievedAt: firstTime, Path: "scores.csv", SHA256: fmt.Sprintf("%x", digest),
			Adapter: AdapterLiveBenchCSV, ModelMap: map[string]string{"Alpha": "acme/alpha"},
			Rules: []ImportRule{{
				Benchmark: "fixture", Domain: "general", Metric: MetricBounded,
				ModelColumn: "Model", ScoreColumn: "Score", ScoreScale: ScoreScalePercent,
				FixedSamples: 100, SettingsSHA256: settings,
			}},
		}},
	}
	first, _, err := BuildCatalog(manifest, directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.RetrievedAt = firstTime.Add(24 * time.Hour)
	manifest.Sources[0].RetrievedAt = manifest.RetrievedAt
	second, _, err := BuildCatalog(manifest, directory)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("revision changed with retrieval time: %s != %s", first.Revision, second.Revision)
	}
}
