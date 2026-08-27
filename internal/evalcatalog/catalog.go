package evalcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	//go:embed data/catalog.json
	embeddedCatalog []byte
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type MetricKind string

const (
	MetricPairwise MetricKind = "pairwise_probability"
	MetricBounded  MetricKind = "bounded_score"
	MetricRankOnly MetricKind = "rank_only"
)

type Source struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	License     string    `json:"license"`
	Version     string    `json:"version"`
	RetrievedAt time.Time `json:"retrieved_at"`
	SHA256      string    `json:"sha256"`
}

type Result struct {
	SourceID       string     `json:"source_id"`
	Benchmark      string     `json:"benchmark"`
	Domain         string     `json:"domain"`
	ModelID        string     `json:"model_id"`
	Variant        string     `json:"variant,omitempty"`
	BaselineID     string     `json:"baseline_id,omitempty"`
	Metric         MetricKind `json:"metric"`
	ScoreBPS       int        `json:"score_bps,omitempty"`
	LowerBPS       int        `json:"lower_bps,omitempty"`
	UpperBPS       int        `json:"upper_bps,omitempty"`
	Samples        int        `json:"samples,omitempty"`
	Rank           int        `json:"rank,omitempty"`
	SettingsSHA256 string     `json:"settings_sha256"`
	Scaffold       string     `json:"scaffold,omitempty"`
	ScaffoldSHA256 string     `json:"scaffold_sha256,omitempty"`
}

type Catalog struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      string    `json:"revision"`
	RetrievedAt   time.Time `json:"retrieved_at"`
	Sources       []Source  `json:"sources"`
	Results       []Result  `json:"results"`
}

func decodeCatalog(contents []byte) (Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode evaluation catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Catalog{}, errors.New("decode evaluation catalog: trailing JSON value")
		}
		return Catalog{}, fmt.Errorf("decode evaluation catalog trailing data: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (catalog Catalog) Validate() error {
	if catalog.SchemaVersion != 1 {
		return fmt.Errorf("evaluation catalog schema_version must be 1, got %d", catalog.SchemaVersion)
	}
	if !sha256Pattern.MatchString(catalog.Revision) {
		return errors.New("evaluation catalog revision must be a lowercase SHA-256")
	}
	if catalog.RetrievedAt.IsZero() {
		return errors.New("evaluation catalog retrieved_at is required")
	}
	if len(catalog.Sources) == 0 {
		return errors.New("evaluation catalog must contain at least one source")
	}
	sources := make(map[string]struct{}, len(catalog.Sources))
	for _, source := range catalog.Sources {
		if source.ID == "" || source.ID != strings.TrimSpace(source.ID) || source.Name == "" || source.Version == "" || source.License == "" {
			return errors.New("evaluation catalog sources require id, name, license and version")
		}
		if _, exists := sources[source.ID]; exists {
			return fmt.Errorf("duplicate source %q", source.ID)
		}
		sources[source.ID] = struct{}{}
		parsedURL, err := url.ParseRequestURI(source.URL)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
			return fmt.Errorf("evaluation source %s has invalid URL", source.ID)
		}
		if source.RetrievedAt.IsZero() {
			return fmt.Errorf("evaluation source %s retrieved_at is required", source.ID)
		}
		if !sha256Pattern.MatchString(source.SHA256) {
			return fmt.Errorf("evaluation source %s sha256 must be a lowercase SHA-256", source.ID)
		}
	}
	results := make(map[string]struct{}, len(catalog.Results))
	for _, result := range catalog.Results {
		if _, exists := sources[result.SourceID]; !exists {
			return fmt.Errorf("evaluation result references unknown source %q", result.SourceID)
		}
		if result.Benchmark == "" || result.Domain == "" || !canonicalModelID(result.ModelID) {
			return fmt.Errorf("evaluation results require benchmark, domain and canonical model id")
		}
		if result.Variant != strings.TrimSpace(result.Variant) || strings.ContainsAny(result.Variant, " \t\r\n") {
			return fmt.Errorf("evaluation result %s/%s has invalid variant", result.SourceID, result.Benchmark)
		}
		if !sha256Pattern.MatchString(result.SettingsSHA256) {
			return fmt.Errorf("evaluation result %s/%s settings_sha256 must be a lowercase SHA-256", result.SourceID, result.Benchmark)
		}
		if result.ScaffoldSHA256 != "" && !sha256Pattern.MatchString(result.ScaffoldSHA256) {
			return fmt.Errorf("evaluation result %s/%s scaffold_sha256 must be a lowercase SHA-256", result.SourceID, result.Benchmark)
		}
		if err := validateResultMetric(result); err != nil {
			return fmt.Errorf("evaluation result %s/%s/%s: %w", result.SourceID, result.Benchmark, result.ModelID, err)
		}
		key := strings.Join([]string{
			result.SourceID, result.Benchmark, result.Domain, result.ModelID, result.Variant, result.BaselineID,
			string(result.Metric), result.SettingsSHA256, result.ScaffoldSHA256,
		}, "\x00")
		if _, exists := results[key]; exists {
			return fmt.Errorf("duplicate result for %s/%s/%s", result.SourceID, result.Benchmark, result.ModelID)
		}
		results[key] = struct{}{}
	}
	return nil
}

func validateResultMetric(result Result) error {
	switch result.Metric {
	case MetricPairwise:
		if !canonicalModelID(result.BaselineID) || result.BaselineID == result.ModelID {
			return errors.New("pairwise metric requires a different canonical baseline model id")
		}
		if result.Rank != 0 {
			return errors.New("pairwise metric must not set rank")
		}
		return validateScoredResult(result)
	case MetricBounded:
		if result.BaselineID != "" {
			return errors.New("bounded metric must not set baseline")
		}
		if result.Rank != 0 {
			return errors.New("bounded metric must not set rank")
		}
		return validateScoredResult(result)
	case MetricRankOnly:
		if result.BaselineID != "" {
			return errors.New("rank-only metric must not set baseline")
		}
		if result.Rank <= 0 {
			return errors.New("rank-only metric requires a positive rank")
		}
		if result.ScoreBPS != 0 || result.LowerBPS != 0 || result.UpperBPS != 0 || result.Samples != 0 {
			return errors.New("rank-only metric must not set score, interval or samples")
		}
		return nil
	default:
		return fmt.Errorf("unsupported metric %q", result.Metric)
	}
}

func validateScoredResult(result Result) error {
	if result.ScoreBPS < 0 || result.ScoreBPS > 10_000 {
		return errors.New("score_bps must be between 0 and 10000")
	}
	if result.LowerBPS < 0 || result.UpperBPS > 10_000 || result.LowerBPS > result.ScoreBPS || result.ScoreBPS > result.UpperBPS {
		return errors.New("confidence interval must contain score_bps and stay within 0..10000")
	}
	if result.Samples <= 0 {
		return errors.New("scored metric requires positive samples")
	}
	return nil
}

func IsCanonicalModelID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func canonicalModelID(value string) bool {
	return IsCanonicalModelID(value)
}

func cloneCatalog(catalog Catalog) Catalog {
	contents, err := json.Marshal(catalog)
	if err != nil {
		panic(err)
	}
	cloned, err := decodeCatalog(contents)
	if err != nil {
		panic(err)
	}
	return cloned
}
