package evalcatalog

import (
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

func TestBFCLImportStripsOnlyKnownPresentationSuffixes(t *testing.T) {
	resolver, err := NewCanonicalResolver(modelcatalog.Catalog{Models: []modelcatalog.Model{{
		ID: "claude-opus-4-5-20251101", CanonicalID: "anthropic/claude-opus-4-5-20251101",
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("Rank,Overall Acc,Model\n1,82.5,Claude-Opus-4-5-20251101 (FC)\n2,71.2,Claude-Opus-5\n")
	imported, err := importBFCL(contents, sourceFixture("bfcl"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 1 || imported.SkippedUnmapped != 1 {
		t.Fatalf("imported=%+v", imported)
	}
	result := imported.Results[0]
	if result.ModelID != "anthropic/claude-opus-4-5-20251101" || result.ScoreBPS != 8250 ||
		result.Samples != 1000 || result.Domain != "tool_use" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBFCLImportPrefersNativeFunctionCallingPresentation(t *testing.T) {
	resolver, err := NewCanonicalResolver(modelcatalog.Catalog{Models: []modelcatalog.Model{{
		ID: "alpha", CanonicalID: "acme/alpha-2026", APIIDs: []string{"alpha-alias"},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("Rank,Overall Acc,Model\n1,92,alpha (Prompt)\n2,81,alpha (FC)\n")
	imported, err := importBFCL(contents, sourceFixture("bfcl"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 1 || imported.Results[0].ScoreBPS != 8100 {
		t.Fatalf("imported=%+v", imported)
	}
}

func TestBFCLImportRejectsMalformedPercentAndSamePresentationDuplicates(t *testing.T) {
	resolver, err := NewCanonicalResolver(modelcatalog.Catalog{Models: []modelcatalog.Model{{
		ID: "alpha", CanonicalID: "acme/alpha-2026", APIIDs: []string{"alpha-alias"},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{
		"Rank,Overall Acc,Model\n1,82 percent,alpha\n",
		"Rank,Overall Acc,Model\n1,82,alpha (FC)\n2,81,alpha-alias (FC)\n",
	} {
		if _, err := importBFCL([]byte(contents), sourceFixture("bfcl"), resolver); err == nil {
			t.Fatalf("importBFCL accepted invalid CSV: %s", strings.TrimSpace(contents))
		}
	}
}
