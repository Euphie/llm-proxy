package evalcatalog

import (
	"strings"
	"testing"
	"time"
)

func TestAssembleCatalogRevisionIgnoresOrderAndRetrievalTime(t *testing.T) {
	firstTime := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(24 * time.Hour)
	firstSources := assemblySources(firstTime)
	secondSources := assemblySources(secondTime)
	secondSources[0], secondSources[1] = secondSources[1], secondSources[0]
	secondSources[0].Results[0], secondSources[0].Results[1] = secondSources[0].Results[1], secondSources[0].Results[0]

	first, firstReport, err := AssembleCatalog(firstTime, firstSources)
	if err != nil {
		t.Fatal(err)
	}
	second, secondReport, err := AssembleCatalog(secondTime, secondSources)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("revision changed with order/time: %s != %s", first.Revision, second.Revision)
	}
	if first.Sources[0].ID != "arena" || first.Results[0].SourceID != "arena" {
		t.Fatalf("catalog not sorted: sources=%+v results=%+v", first.Sources, first.Results)
	}
	if firstReport != (ImportReport{Imported: 3, SkippedUnmapped: 2}) || firstReport != secondReport {
		t.Fatalf("reports=%+v %+v", firstReport, secondReport)
	}
}

func TestAssembleCatalogRevisionTracksEvidenceContent(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	base, _, err := AssembleCatalog(now, assemblySources(now))
	if err != nil {
		t.Fatal(err)
	}

	changedDigest := assemblySources(now)
	changedDigest[0].Source.SHA256 = strings.Repeat("c", 64)
	withDigest, _, err := AssembleCatalog(now, changedDigest)
	if err != nil {
		t.Fatal(err)
	}
	if base.Revision == withDigest.Revision {
		t.Fatal("source digest did not change revision")
	}

	changedScore := assemblySources(now)
	changedScore[0].Results[0].ScoreBPS++
	withScore, _, err := AssembleCatalog(now, changedScore)
	if err != nil {
		t.Fatal(err)
	}
	if base.Revision == withScore.Revision {
		t.Fatal("score did not change revision")
	}
}

func TestAssembleCatalogRejectsDuplicateSourcesAndResults(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	sources := assemblySources(now)
	sources = append(sources, sources[0])
	if _, _, err := AssembleCatalog(now, sources); err == nil || !strings.Contains(err.Error(), "duplicate source") {
		t.Fatalf("duplicate source error=%v", err)
	}

	sources = assemblySources(now)
	sources[0].Results = append(sources[0].Results, sources[0].Results[0])
	if _, _, err := AssembleCatalog(now, sources); err == nil || !strings.Contains(err.Error(), "duplicate result") {
		t.Fatalf("duplicate result error=%v", err)
	}
}

func assemblySources(retrievedAt time.Time) []ImportedSource {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	settings := strings.Repeat("d", 64)
	return []ImportedSource{
		{
			Source: Source{ID: "livebench", Name: "LiveBench", URL: "https://example.test/livebench", License: "Apache-2.0", Version: "v1", RetrievedAt: retrievedAt, SHA256: digestA},
			Results: []Result{
				{SourceID: "livebench", Benchmark: "general", Domain: "general", ModelID: "acme/alpha", Metric: MetricBounded, ScoreBPS: 8000, LowerBPS: 7800, UpperBPS: 8200, Samples: 100, SettingsSHA256: settings},
			},
			Imported: 1, SkippedUnmapped: 2,
		},
		{
			Source: Source{ID: "arena", Name: "Arena", URL: "https://example.test/arena", License: "CC-BY-4.0", Version: "v2", RetrievedAt: retrievedAt, SHA256: digestB},
			Results: []Result{
				{SourceID: "arena", Benchmark: "overall", Domain: "general", ModelID: "acme/beta", Metric: MetricRankOnly, Rank: 2, SettingsSHA256: settings},
				{SourceID: "arena", Benchmark: "overall", Domain: "general", ModelID: "acme/alpha", Metric: MetricRankOnly, Rank: 1, SettingsSHA256: settings},
			},
			Imported: 2,
		},
	}
}
