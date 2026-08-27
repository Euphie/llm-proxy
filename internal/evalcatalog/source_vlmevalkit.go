package evalcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

var vlmAnchorPattern = regexp.MustCompile(`(?is)<a\b[^>]*>(.*?)</a>`)
var vlmTagPattern = regexp.MustCompile(`(?is)<[^>]+>`)

var vlmBenchmarks = []string{
	"MMBench_V11", "MMStar", "MMMU_VAL", "MathVista", "OCRBench", "AI2D", "HallusionBench", "MMVet",
}

type vlmEvalKitAdapter struct{}

func NewVLMEvalKitAdapter() SourceAdapter { return vlmEvalKitAdapter{} }

func (vlmEvalKitAdapter) ID() string { return "vlmevalkit" }

func (vlmEvalKitAdapter) Load(
	ctx context.Context,
	downloader Downloader,
	models modelcatalog.Catalog,
	now time.Time,
	directory string,
) (ImportedSource, error) {
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL: "https://opencompass-open-vlm-leaderboard.hf.space/config", Filename: "vlmevalkit-config.json",
		AllowedHosts: []HostRule{{Host: "opencompass-open-vlm-leaderboard.hf.space"}},
	})
	if err != nil {
		return ImportedSource{}, err
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read VLMEvalKit config: %w", err)
	}
	resolver, err := NewCanonicalResolver(models, map[string]string{
		"Claude3.5": "anthropic/claude-3-5-sonnet-20241022",
	})
	if err != nil {
		return ImportedSource{}, err
	}
	return importVLMEvalKit(contents, Source{
		ID: "vlmevalkit", Name: "Open VLM Leaderboard", URL: "https://huggingface.co/spaces/opencompass/open_vlm_leaderboard",
		License: "Apache-2.0", Version: download.SHA256, RetrievedAt: now.UTC(), SHA256: download.SHA256,
	}, resolver)
}

type vlmDataFrame struct {
	headers []string
	rows    [][]any
}

func importVLMEvalKit(contents []byte, source Source, resolver CanonicalResolver) (ImportedSource, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var config any
	if err := decoder.Decode(&config); err != nil {
		return ImportedSource{}, fmt.Errorf("decode VLMEvalKit config: %w", err)
	}
	var frames []vlmDataFrame
	collectVLMDataFrames(config, &frames)
	matching := make([]vlmDataFrame, 0, 1)
	required := append([]string{"Method", "Avg Score"}, vlmBenchmarks...)
	for _, frame := range frames {
		if containsAllHeaders(frame.headers, required) {
			matching = append(matching, frame)
		}
	}
	if len(matching) != 1 {
		return ImportedSource{}, fmt.Errorf("VLMEvalKit config requires exactly one main leaderboard table, got %d", len(matching))
	}
	frame := matching[0]
	positions := make(map[string]int, len(frame.headers))
	for index, header := range frame.headers {
		positions[header] = index
	}
	settings := sha256.Sum256([]byte(strings.Join(required, "\x00")))
	settingsDigest := fmt.Sprintf("%x", settings)
	results := make([]Result, 0, len(frame.rows))
	seen := make(map[string]struct{})
	skipped := 0
	for index, row := range frame.rows {
		if len(row) < len(frame.headers) {
			return ImportedSource{}, fmt.Errorf("VLMEvalKit row %d has too few columns", index+1)
		}
		method := extractVLMMethod(fmt.Sprint(row[positions["Method"]]))
		modelID, found := resolver.Resolve(method)
		if !found {
			skipped++
			continue
		}
		if _, duplicate := seen[modelID]; duplicate {
			return ImportedSource{}, fmt.Errorf("VLMEvalKit contains duplicate model %s", modelID)
		}
		seen[modelID] = struct{}{}
		score, err := percentBPSValue(row[positions["Avg Score"]])
		if err != nil {
			return ImportedSource{}, fmt.Errorf("VLMEvalKit row %d: %w", index+1, err)
		}
		lower, upper := wilsonIntervalBPS(score, len(vlmBenchmarks))
		results = append(results, Result{
			SourceID: source.ID, Benchmark: "openvlm-main", Domain: "vision", ModelID: modelID,
			Metric: MetricBounded, ScoreBPS: score, LowerBPS: lower, UpperBPS: upper,
			Samples: len(vlmBenchmarks), SettingsSHA256: settingsDigest,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ModelID < results[j].ModelID })
	return ImportedSource{Source: source, Results: results, Imported: len(results), SkippedUnmapped: skipped}, nil
}

func collectVLMDataFrames(value any, frames *[]vlmDataFrame) {
	switch typed := value.(type) {
	case map[string]any:
		headers, headersOK := stringSlice(typed["headers"])
		rows, rowsOK := anyRows(typed["data"])
		if headersOK && rowsOK {
			*frames = append(*frames, vlmDataFrame{headers: headers, rows: rows})
		}
		for _, child := range typed {
			collectVLMDataFrames(child, frames)
		}
	case []any:
		for _, child := range typed {
			collectVLMDataFrames(child, frames)
		}
	}
}

func stringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func anyRows(value any) ([][]any, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	rows := make([][]any, len(values))
	for index, value := range values {
		row, ok := value.([]any)
		if !ok {
			return nil, false
		}
		rows[index] = row
	}
	return rows, true
}

func containsAllHeaders(headers, required []string) bool {
	set := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		set[header] = struct{}{}
	}
	for _, header := range required {
		if _, found := set[header]; !found {
			return false
		}
	}
	return true
}

func extractVLMMethod(value string) string {
	if match := vlmAnchorPattern.FindStringSubmatch(value); len(match) == 2 {
		value = match[1]
	}
	return strings.TrimSpace(html.UnescapeString(vlmTagPattern.ReplaceAllString(value, "")))
}

func percentBPSValue(value any) (int, error) {
	switch typed := value.(type) {
	case json.Number:
		return strictPercentBPS(string(typed))
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("score must be finite")
		}
		return strictPercentBPS(strconv.FormatFloat(typed, 'f', -1, 64))
	case string:
		return strictPercentBPS(typed)
	default:
		return 0, fmt.Errorf("score has unsupported type")
	}
}
