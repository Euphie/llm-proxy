package evalcatalog

import (
	"testing"
)

func TestSWEBenchImportRequiresOneExactModelAndReproducibleScaffold(t *testing.T) {
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{
  "leaderboards": [{"name":"Verified","results":[
    {"name":"Alpha Agent","folder":"alpha-agent","resolved":48.6,"mini-swe-agent_version":"1.2.0","tags":["Model: alpha","System: Attempts - 1"]},
    {"name":"Composite","folder":"composite","resolved":50,"tags":["Model: alpha","Model: beta","System: Attempts - 1"]},
    {"name":"Beta System","folder":"beta-system","resolved":40,"tags":["Model: beta","System: Attempts - 1"]},
    {"name":"Unknown","folder":"unknown","resolved":60,"tags":["Model: unknown","System: Attempts - 1"]}
  ]}]
}`)
	imported, err := importSWEBench(contents, sourceFixture("swebench"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 2 || imported.SkippedUnmapped != 1 || len(imported.Results) != 2 {
		t.Fatalf("imported=%+v", imported)
	}
	alpha := resultByModelDomain(t, imported.Results, "acme/alpha-2026", "coding")
	if alpha.ScoreBPS != 4860 || alpha.Samples != 500 || alpha.Scaffold != "Alpha Agent · folder:alpha-agent · mini-swe-agent/1.2.0 · System: Attempts - 1" || alpha.ScaffoldSHA256 == "" {
		t.Fatalf("alpha=%+v", alpha)
	}
	beta := resultByModelDomain(t, imported.Results, "acme/beta-2026", "coding")
	firstName, firstDigest, ok := sweBenchScaffold("Beta System", "beta-system", "", []string{"System: Attempts - 1"})
	secondName, secondDigest, secondOK := sweBenchScaffold("Beta System", "beta-system", "", []string{"System: Attempts - 1"})
	if !ok || !secondOK || firstName != secondName || firstDigest != secondDigest || beta.ScaffoldSHA256 != firstDigest {
		t.Fatalf("scaffolds=%q/%q %q/%q beta=%+v", firstName, secondName, firstDigest, secondDigest, beta)
	}
}

func TestSWEBenchScaffoldDistinguishesOfficialResultFolders(t *testing.T) {
	firstName, firstDigest, firstOK := sweBenchScaffold(
		"EPAM AI/Run Developer Agent + GPT4o",
		"20241016_epam-ai-run-gpt-4o",
		"",
		[]string{"System: Attempts - 2+"},
	)
	secondName, secondDigest, secondOK := sweBenchScaffold(
		"EPAM AI/Run Developer Agent + GPT4o",
		"20240820_epam-ai-run-gpt-4o",
		"",
		[]string{"System: Attempts - 2+"},
	)
	if !firstOK || !secondOK || firstName == secondName || firstDigest == secondDigest {
		t.Fatalf("first=%q/%q/%t second=%q/%q/%t", firstName, firstDigest, firstOK, secondName, secondDigest, secondOK)
	}
}

func TestSWEBenchImportSkipsRowsWithoutReproducibleScaffold(t *testing.T) {
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{"leaderboards":[{"name":"Verified","results":[
    {"name":"","folder":"alpha","resolved":50,"tags":["Model: alpha"]}
  ]}]}`)
	imported, err := importSWEBench(contents, sourceFixture("swebench"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 0 || len(imported.Results) != 0 {
		t.Fatalf("imported=%+v", imported)
	}
}
