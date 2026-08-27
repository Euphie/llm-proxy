package evalcatalog

import (
	"fmt"
	"strings"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

type CanonicalResolver struct {
	index map[string]string
}

func NewCanonicalResolver(catalog modelcatalog.Catalog, sourceAliases map[string]string) (CanonicalResolver, error) {
	index := make(map[string]string)
	canonical := make(map[string]string, len(catalog.Models))
	add := func(identifier, modelID string) error {
		key := normalizeCanonicalIdentifier(identifier)
		if key == "" {
			return fmt.Errorf("empty model identifier for %s", modelID)
		}
		if owner, exists := index[key]; exists && owner != modelID {
			return fmt.Errorf("ambiguous model identifier %q for %s and %s", identifier, owner, modelID)
		}
		index[key] = modelID
		return nil
	}
	for _, model := range catalog.Models {
		if !IsCanonicalModelID(model.CanonicalID) {
			return CanonicalResolver{}, fmt.Errorf("invalid canonical model id %q", model.CanonicalID)
		}
		canonical[normalizeCanonicalIdentifier(model.CanonicalID)] = model.CanonicalID
		identifiers := []string{model.ID, model.CanonicalID}
		identifiers = append(identifiers, model.APIIDs...)
		identifiers = append(identifiers, model.Aliases...)
		identifiers = append(identifiers, model.CompatibilityAliases...)
		for _, identifier := range identifiers {
			if err := add(identifier, model.CanonicalID); err != nil {
				return CanonicalResolver{}, err
			}
		}
	}
	for alias, target := range sourceAliases {
		canonicalTarget, exists := canonical[normalizeCanonicalIdentifier(target)]
		if !exists {
			return CanonicalResolver{}, fmt.Errorf("source alias %q targets absent canonical model %q", alias, target)
		}
		if err := add(alias, canonicalTarget); err != nil {
			return CanonicalResolver{}, err
		}
	}
	return CanonicalResolver{index: index}, nil
}

func (resolver CanonicalResolver) Resolve(identifier string) (string, bool) {
	modelID, found := resolver.index[normalizeCanonicalIdentifier(identifier)]
	return modelID, found
}

func normalizeCanonicalIdentifier(identifier string) string {
	return strings.ToLower(strings.TrimSpace(identifier))
}
