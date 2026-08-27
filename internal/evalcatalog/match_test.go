package evalcatalog

import (
	"testing"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

func TestResolveCanonicalModelMatchesOnlyDeclaredExactIdentifiers(t *testing.T) {
	catalog := modelcatalog.Catalog{Models: []modelcatalog.Model{
		{
			ID: "alpha", CanonicalID: "acme/alpha", APIIDs: []string{"alpha-api"},
			Aliases: []string{"alpha-alias"}, CompatibilityAliases: []string{"claude-alpha"},
		},
	}}
	for _, identifier := range []string{"alpha", "acme/alpha", "alpha-api", "alpha-alias", "claude-alpha", " Alpha ", "ALPHA"} {
		if got, ok := ResolveCanonicalModel(catalog, identifier); !ok || got != "acme/alpha" {
			t.Fatalf("ResolveCanonicalModel(%q)=(%q,%v)", identifier, got, ok)
		}
	}
	for _, identifier := range []string{"alpha api", "acme-alpha", "alpha-v2"} {
		if got, ok := ResolveCanonicalModel(catalog, identifier); ok {
			t.Fatalf("ResolveCanonicalModel(%q) unexpectedly matched %q", identifier, got)
		}
	}
}

func TestResolveCanonicalModelRejectsAmbiguousIdentifier(t *testing.T) {
	catalog := modelcatalog.Catalog{Models: []modelcatalog.Model{
		{ID: "alpha", CanonicalID: "acme/alpha", Aliases: []string{"shared"}},
		{ID: "beta", CanonicalID: "acme/beta", Aliases: []string{"shared"}},
	}}
	if got, ok := ResolveCanonicalModel(catalog, "shared"); ok {
		t.Fatalf("ambiguous identifier matched %q", got)
	}
}
