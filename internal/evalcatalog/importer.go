package evalcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxImportBytes = 64 << 20

const (
	AdapterLiveBenchCSV  = "livebench_csv"
	AdapterBFCLCSV       = "bfcl_csv"
	AdapterVLMEvalKitCSV = "vlmevalkit_csv"
	AdapterArenaCSV      = "arena_csv"
	AdapterSWEBenchJSON  = "swebench_json"

	ScoreScalePercent    = "percent"
	ScoreScaleBasisPoint = "basis_points"
	ScoreScaleUnit       = "unit_interval"
)

type ImportManifest struct {
	SchemaVersion int            `json:"schema_version"`
	RetrievedAt   time.Time      `json:"retrieved_at"`
	Sources       []ImportSource `json:"sources"`
}

type ImportSource struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	URL               string            `json:"url"`
	License           string            `json:"license"`
	Version           string            `json:"version"`
	RetrievedAt       time.Time         `json:"retrieved_at"`
	Path              string            `json:"path"`
	SHA256            string            `json:"sha256"`
	Adapter           string            `json:"adapter"`
	ModelMap          map[string]string `json:"model_map"`
	ScaffoldMap       map[string]string `json:"scaffold_map,omitempty"`
	ScaffoldSHA256Map map[string]string `json:"scaffold_sha256_map,omitempty"`
	Rules             []ImportRule      `json:"rules"`
}

type ImportRule struct {
	Benchmark       string     `json:"benchmark"`
	Domain          string     `json:"domain"`
	Metric          MetricKind `json:"metric"`
	Leaderboard     string     `json:"leaderboard,omitempty"`
	ModelColumn     string     `json:"model_column"`
	BaselineModelID string     `json:"baseline_model_id,omitempty"`
	ScoreColumn     string     `json:"score_column,omitempty"`
	LowerColumn     string     `json:"lower_column,omitempty"`
	UpperColumn     string     `json:"upper_column,omitempty"`
	SamplesColumn   string     `json:"samples_column,omitempty"`
	FixedSamples    int        `json:"fixed_samples,omitempty"`
	RankColumn      string     `json:"rank_column,omitempty"`
	ScoreScale      string     `json:"score_scale,omitempty"`
	SettingsSHA256  string     `json:"settings_sha256"`
}

type ImportReport struct {
	Imported        int `json:"imported"`
	SkippedUnmapped int `json:"skipped_unmapped"`
}

func DecodeImportManifest(reader io.Reader) (ImportManifest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest ImportManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ImportManifest{}, fmt.Errorf("decode evaluation import manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ImportManifest{}, errors.New("decode evaluation import manifest: trailing JSON value")
		}
		return ImportManifest{}, fmt.Errorf("decode evaluation import manifest trailing data: %w", err)
	}
	return manifest, nil
}

func BuildCatalog(manifest ImportManifest, baseDirectory string) (Catalog, ImportReport, error) {
	if manifest.SchemaVersion != 1 || manifest.RetrievedAt.IsZero() || len(manifest.Sources) == 0 {
		return Catalog{}, ImportReport{}, errors.New("evaluation import manifest requires schema_version 1, retrieved_at and sources")
	}
	imported := make([]ImportedSource, 0, len(manifest.Sources))
	for _, input := range manifest.Sources {
		contents, err := readPinnedImport(baseDirectory, input)
		if err != nil {
			return Catalog{}, ImportReport{}, err
		}
		rowsByLeaderboard, err := importRows(input.Adapter, contents)
		if err != nil {
			return Catalog{}, ImportReport{}, fmt.Errorf("import evaluation source %s: %w", input.ID, err)
		}
		item := ImportedSource{Source: Source{
			ID: input.ID, Name: input.Name, URL: input.URL, License: input.License,
			Version: input.Version, RetrievedAt: input.RetrievedAt.UTC(), SHA256: input.SHA256,
		}}
		for _, rule := range input.Rules {
			rows, found := rowsByLeaderboard[rule.Leaderboard]
			if !found {
				return Catalog{}, ImportReport{}, fmt.Errorf("evaluation source %s has no leaderboard %q", input.ID, rule.Leaderboard)
			}
			for index, row := range rows {
				upstreamModel, err := requiredCell(row, rule.ModelColumn)
				if err != nil {
					return Catalog{}, ImportReport{}, fmt.Errorf("evaluation source %s row %d: %w", input.ID, index+1, err)
				}
				modelID, mapped := input.ModelMap[upstreamModel]
				if !mapped {
					item.SkippedUnmapped++
					continue
				}
				result, err := importResult(input, rule, row, upstreamModel, modelID, index)
				if err != nil {
					return Catalog{}, ImportReport{}, fmt.Errorf("evaluation source %s model %q: %w", input.ID, upstreamModel, err)
				}
				item.Results = append(item.Results, result)
				item.Imported++
			}
		}
		imported = append(imported, item)
	}
	return AssembleCatalog(manifest.RetrievedAt, imported)
}

func EncodeCatalog(writer io.Writer, catalog Catalog) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(catalog)
}

func readPinnedImport(baseDirectory string, input ImportSource) ([]byte, error) {
	if input.ID == "" || input.Path == "" || !sha256Pattern.MatchString(input.SHA256) ||
		input.RetrievedAt.IsZero() || len(input.Rules) == 0 || len(input.ModelMap) == 0 {
		return nil, fmt.Errorf("evaluation source %s is missing pinned import metadata", input.ID)
	}
	cleanPath := filepath.Clean(input.Path)
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("evaluation source %s path must stay inside the manifest directory", input.ID)
	}
	file, err := os.Open(filepath.Join(baseDirectory, cleanPath))
	if err != nil {
		return nil, fmt.Errorf("open evaluation source %s: %w", input.ID, err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read evaluation source %s: %w", input.ID, err)
	}
	if len(contents) > maxImportBytes {
		return nil, fmt.Errorf("evaluation source %s exceeds %d bytes", input.ID, maxImportBytes)
	}
	digest := sha256.Sum256(contents)
	if got := fmt.Sprintf("%x", digest); got != input.SHA256 {
		return nil, fmt.Errorf("evaluation source %s SHA-256 mismatch: got %s", input.ID, got)
	}
	return contents, nil
}

func importRows(adapter string, contents []byte) (map[string][]map[string]string, error) {
	switch adapter {
	case AdapterLiveBenchCSV, AdapterBFCLCSV, AdapterVLMEvalKitCSV, AdapterArenaCSV:
		rows, err := decodeCSV(contents)
		return map[string][]map[string]string{"": rows}, err
	case AdapterSWEBenchJSON:
		return decodeSWEBench(contents)
	default:
		return nil, fmt.Errorf("unsupported adapter %q", adapter)
	}
}

func decodeCSV(contents []byte) ([]map[string]string, error) {
	reader := csv.NewReader(bytes.NewReader(contents))
	reader.ReuseRecord = false
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("decode CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, errors.New("CSV requires a header and at least one data row")
	}
	headers := records[0]
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		if header == "" {
			return nil, errors.New("CSV contains an empty header")
		}
		if _, exists := seen[header]; exists {
			return nil, fmt.Errorf("CSV contains duplicate header %q", header)
		}
		seen[header] = struct{}{}
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]string, len(headers))
		for index, header := range headers {
			row[header] = strings.TrimSpace(record[index])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func decodeSWEBench(contents []byte) (map[string][]map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var payload struct {
		Leaderboards []struct {
			Name    string           `json:"name"`
			Results []map[string]any `json:"results"`
		} `json:"leaderboards"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode SWE-bench JSON: %w", err)
	}
	if len(payload.Leaderboards) == 0 {
		return nil, errors.New("SWE-bench JSON contains no leaderboards")
	}
	result := make(map[string][]map[string]string, len(payload.Leaderboards))
	for _, leaderboard := range payload.Leaderboards {
		if leaderboard.Name == "" {
			return nil, errors.New("SWE-bench leaderboard name is required")
		}
		rows := make([]map[string]string, 0, len(leaderboard.Results))
		for _, raw := range leaderboard.Results {
			row := make(map[string]string, len(raw))
			for key, value := range raw {
				switch typed := value.(type) {
				case string:
					row[key] = strings.TrimSpace(typed)
				case json.Number:
					row[key] = string(typed)
				case float64:
					row[key] = strconv.FormatFloat(typed, 'g', -1, 64)
				}
			}
			rows = append(rows, row)
		}
		result[leaderboard.Name] = rows
	}
	return result, nil
}

func importResult(
	input ImportSource,
	rule ImportRule,
	row map[string]string,
	upstreamModel string,
	modelID string,
	position int,
) (Result, error) {
	if rule.Benchmark == "" || rule.Domain == "" || rule.ModelColumn == "" ||
		!sha256Pattern.MatchString(rule.SettingsSHA256) {
		return Result{}, errors.New("rule requires benchmark, domain, model_column and settings_sha256")
	}
	result := Result{
		SourceID: input.ID, Benchmark: rule.Benchmark, Domain: rule.Domain,
		ModelID: modelID, Metric: rule.Metric, SettingsSHA256: rule.SettingsSHA256,
	}
	if scaffold, found := input.ScaffoldMap[upstreamModel]; found {
		result.Scaffold = scaffold
		result.ScaffoldSHA256 = input.ScaffoldSHA256Map[upstreamModel]
	}
	if input.Adapter == AdapterSWEBenchJSON &&
		(result.Scaffold == "" || !sha256Pattern.MatchString(result.ScaffoldSHA256)) {
		return Result{}, errors.New("SWE-bench result requires an exact scaffold and scaffold SHA-256")
	}
	switch rule.Metric {
	case MetricRankOnly:
		result.Rank = position + 1
		if rule.RankColumn != "" {
			rank, err := parsePositiveIntCell(row, rule.RankColumn)
			if err != nil {
				return Result{}, err
			}
			result.Rank = rank
		}
		return result, nil
	case MetricBounded, MetricPairwise:
		if rule.Metric == MetricPairwise {
			result.BaselineID = rule.BaselineModelID
		}
		score, err := parseScaledScore(row, rule.ScoreColumn, rule.ScoreScale)
		if err != nil {
			return Result{}, err
		}
		result.ScoreBPS = score
		result.Samples = rule.FixedSamples
		if rule.SamplesColumn != "" {
			samples, parseErr := parsePositiveIntCell(row, rule.SamplesColumn)
			if parseErr != nil {
				return Result{}, parseErr
			}
			result.Samples = samples
		}
		if result.Samples <= 0 {
			return Result{}, errors.New("scored result requires samples_column or fixed_samples")
		}
		if rule.LowerColumn != "" || rule.UpperColumn != "" {
			if rule.LowerColumn == "" || rule.UpperColumn == "" {
				return Result{}, errors.New("confidence interval requires both lower_column and upper_column")
			}
			result.LowerBPS, err = parseScaledScore(row, rule.LowerColumn, rule.ScoreScale)
			if err != nil {
				return Result{}, err
			}
			result.UpperBPS, err = parseScaledScore(row, rule.UpperColumn, rule.ScoreScale)
			if err != nil {
				return Result{}, err
			}
		} else {
			result.LowerBPS, result.UpperBPS = wilsonIntervalBPS(result.ScoreBPS, result.Samples)
		}
		return result, nil
	default:
		return Result{}, fmt.Errorf("unsupported metric %q", rule.Metric)
	}
}

func requiredCell(row map[string]string, column string) (string, error) {
	value, found := row[column]
	if !found {
		return "", fmt.Errorf("column %q is missing", column)
	}
	if value == "" {
		return "", fmt.Errorf("column %q is empty", column)
	}
	return value, nil
}

func parsePositiveIntCell(row map[string]string, column string) (int, error) {
	value, err := requiredCell(row, column)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("column %q must be a positive integer", column)
	}
	return parsed, nil
}

func parseScaledScore(row map[string]string, column string, scale string) (int, error) {
	value, err := requiredCell(row, column)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("column %q must be a finite score", column)
	}
	multiplier := 0.0
	switch scale {
	case ScoreScalePercent:
		multiplier = 100
	case ScoreScaleBasisPoint:
		multiplier = 1
	case ScoreScaleUnit:
		multiplier = 10_000
	default:
		return 0, fmt.Errorf("unsupported score_scale %q", scale)
	}
	result := int(math.Round(parsed * multiplier))
	if result < 0 || result > 10_000 {
		return 0, fmt.Errorf("column %q score is outside 0..10000 basis points", column)
	}
	return result, nil
}

func wilsonIntervalBPS(scoreBPS int, samples int) (int, int) {
	const z = 1.6448536269514722
	p := float64(scoreBPS) / 10_000
	n := float64(samples)
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denominator
	return max(int(math.Floor((center-margin)*10_000)), 0),
		min(int(math.Ceil((center+margin)*10_000)), 10_000)
}

func resultSortKey(result Result) string {
	return strings.Join([]string{
		result.SourceID, result.Benchmark, result.Domain, result.ModelID,
		result.Variant, result.BaselineID, string(result.Metric), result.SettingsSHA256,
	}, "\x00")
}
