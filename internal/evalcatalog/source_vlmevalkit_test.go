package evalcatalog

import (
	"testing"
)

func TestVLMEvalKitImportSelectsUniqueMainTableAndExtractsAnchors(t *testing.T) {
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), map[string]string{
		"Alpha API": "acme/alpha-2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{
  "components": [
    {"type":"dataframe","props":{"value":{"headers":["Name","Score"],"data":[["noise",1]]}}},
    {"type":"dataframe","props":{"value":{"headers":["Rank","Method","Eval Date","Avg Score","MMBench_V11","MMStar","MMMU_VAL","MathVista","OCRBench","AI2D","HallusionBench","MMVet"],"data":[
      [1,"<a href='https://example.test/alpha'>Alpha API</a>","2026-08-01",80,81,82,83,84,85,86,87,88],
      [2,"Unknown","2026-08-01",70,71,72,73,74,75,76,77,78]
    ]}}}
  ]
}`)
	imported, err := importVLMEvalKit(contents, sourceFixture("vlmevalkit"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 1 || imported.SkippedUnmapped != 1 {
		t.Fatalf("imported=%+v", imported)
	}
	result := imported.Results[0]
	if result.ModelID != "acme/alpha-2026" || result.ScoreBPS != 8000 || result.Samples != 8 ||
		result.Domain != "vision" || result.LowerBPS >= result.ScoreBPS || result.UpperBPS <= result.ScoreBPS {
		t.Fatalf("result=%+v", result)
	}
}

func TestVLMEvalKitImportFailsWhenMainTableIsMissingOrAmbiguous(t *testing.T) {
	resolver, err := NewCanonicalResolver(sourceFixtureModelCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := []byte(`{"components":[{"props":{"value":{"headers":["Name"],"data":[]}}}]}`)
	if _, err := importVLMEvalKit(missing, sourceFixture("vlmevalkit"), resolver); err == nil {
		t.Fatal("importVLMEvalKit accepted a missing main table")
	}
	main := `{"headers":["Method","Avg Score","MMBench_V11","MMStar","MMMU_VAL","MathVista","OCRBench","AI2D","HallusionBench","MMVet"],"data":[]}`
	ambiguous := []byte(`{"tables":[` + main + `,` + main + `]}`)
	if _, err := importVLMEvalKit(ambiguous, sourceFixture("vlmevalkit"), resolver); err == nil {
		t.Fatal("importVLMEvalKit accepted ambiguous main tables")
	}
}
