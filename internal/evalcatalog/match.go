package evalcatalog

import "github.com/Euphie/llm-proxy/internal/modelcatalog"

func ResolveCanonicalModel(catalog modelcatalog.Catalog, configuredID string) (string, bool) {
	resolver, err := NewCanonicalResolver(catalog, nil)
	if err != nil {
		return "", false
	}
	return resolver.Resolve(configuredID)
}
