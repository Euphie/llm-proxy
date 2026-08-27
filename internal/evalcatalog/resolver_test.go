package evalcatalog

import (
	"testing"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

func TestCanonicalResolverMatchesOnlyExactUniqueIdentifiers(t *testing.T) {
	resolver, err := NewCanonicalResolver(resolverCatalog(), map[string]string{
		"LiveBench Alpha": "acme/alpha-2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{
		"alpha", " ACME/ALPHA-2026 ", "alpha-api", "alpha-alias", "claude-alpha", "livebench alpha",
	} {
		if got, ok := resolver.Resolve(identifier); !ok || got != "acme/alpha-2026" {
			t.Fatalf("Resolve(%q)=(%q,%v)", identifier, got, ok)
		}
	}
	for _, identifier := range []string{"alpha-2027", "alpha family", "prefix-alpha", "acme/alpha"} {
		if got, ok := resolver.Resolve(identifier); ok {
			t.Fatalf("Resolve(%q) unexpectedly matched %q", identifier, got)
		}
	}
}

func TestCanonicalResolverRejectsInvalidSourceAliases(t *testing.T) {
	catalog := resolverCatalog()
	if _, err := NewCanonicalResolver(catalog, map[string]string{"alpha": "acme/beta-2026"}); err == nil {
		t.Fatal("resolver accepted an ambiguous source alias")
	}
	if _, err := NewCanonicalResolver(catalog, map[string]string{"external": "acme/missing"}); err == nil {
		t.Fatal("resolver accepted an absent canonical target")
	}
}

func TestIsCanonicalModelID(t *testing.T) {
	for _, value := range []string{"acme/alpha-2026", "provider/model"} {
		if !IsCanonicalModelID(value) {
			t.Fatalf("IsCanonicalModelID(%q)=false", value)
		}
	}
	for _, value := range []string{"", "alpha", "acme/alpha/2026", " acme/alpha", "acme/alpha family"} {
		if IsCanonicalModelID(value) {
			t.Fatalf("IsCanonicalModelID(%q)=true", value)
		}
	}
}

func resolverCatalog() modelcatalog.Catalog {
	return modelcatalog.Catalog{Models: []modelcatalog.Model{
		{
			ID: "alpha", CanonicalID: "acme/alpha-2026", APIIDs: []string{"alpha-api"},
			Aliases: []string{"alpha-alias"}, CompatibilityAliases: []string{"claude-alpha"},
		},
		{ID: "beta", CanonicalID: "acme/beta-2026"},
	}}
}
