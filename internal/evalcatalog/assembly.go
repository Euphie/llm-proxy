package evalcatalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type ImportedSource struct {
	Source          Source
	Results         []Result
	Imported        int
	SkippedUnmapped int
}

func AssembleCatalog(retrievedAt time.Time, imported []ImportedSource) (Catalog, ImportReport, error) {
	catalog := Catalog{
		SchemaVersion: 1,
		RetrievedAt:   retrievedAt.UTC(),
	}
	report := ImportReport{}
	for _, item := range imported {
		catalog.Sources = append(catalog.Sources, item.Source)
		catalog.Results = append(catalog.Results, item.Results...)
		report.Imported += item.Imported
		report.SkippedUnmapped += item.SkippedUnmapped
	}
	sort.Slice(catalog.Sources, func(i, j int) bool {
		return catalog.Sources[i].ID < catalog.Sources[j].ID
	})
	sort.Slice(catalog.Results, func(i, j int) bool {
		return resultSortKey(catalog.Results[i]) < resultSortKey(catalog.Results[j])
	})

	type revisionSource struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	}
	sources := make([]revisionSource, len(catalog.Sources))
	for index, source := range catalog.Sources {
		sources[index] = revisionSource{ID: source.ID, Version: source.Version, SHA256: source.SHA256}
	}
	seed, err := json.Marshal(struct {
		Sources []revisionSource `json:"sources"`
		Results []Result         `json:"results"`
	}{Sources: sources, Results: catalog.Results})
	if err != nil {
		return Catalog{}, ImportReport{}, fmt.Errorf("encode evaluation catalog revision: %w", err)
	}
	revision := sha256.Sum256(seed)
	catalog.Revision = fmt.Sprintf("%x", revision)
	if err := catalog.Validate(); err != nil {
		return Catalog{}, ImportReport{}, fmt.Errorf("validate assembled evaluation catalog: %w", err)
	}
	return catalog, report, nil
}
