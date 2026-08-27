package evalcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

const bfclRepository = "HuanzhiMao/BFCL-Result"

var strictPercentPattern = regexp.MustCompile(`^(?:100(?:\.0+)?|[0-9]{1,2}(?:\.[0-9]+)?)%?$`)
var bfclDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

var bfclPresentationSuffixes = []struct {
	suffix   string
	variant  string
	priority int
}{
	{suffix: " (FC)", variant: "fc", priority: 5},
	{suffix: " (FC thinking)", variant: "fc_thinking", priority: 4},
	{suffix: " (Prompt)", variant: "prompt", priority: 2},
	{suffix: " (Prompt + Thinking)", variant: "prompt_thinking", priority: 1},
}

type bfclAdapter struct{}

func NewBFCLAdapter() SourceAdapter { return bfclAdapter{} }

func (bfclAdapter) ID() string { return "bfcl" }

func (bfclAdapter) Load(
	ctx context.Context,
	downloader Downloader,
	models modelcatalog.Catalog,
	now time.Time,
	directory string,
) (ImportedSource, error) {
	commit, err := resolveGitHubCommit(ctx, downloader, directory, bfclRepository, "main", "bfcl-commit.json")
	if err != nil {
		return ImportedSource{}, err
	}
	listing, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://api.github.com/repos/" + bfclRepository + "/contents?ref=" + commit,
		Filename: "bfcl-contents.json", AllowedHosts: githubAPIHosts, MaxBytes: 4 << 20,
	})
	if err != nil {
		return ImportedSource{}, err
	}
	contents, err := os.ReadFile(listing.Path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read BFCL repository listing: %w", err)
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(contents, &entries); err != nil {
		return ImportedSource{}, fmt.Errorf("decode BFCL repository listing: %w", err)
	}
	latest := ""
	for _, entry := range entries {
		if entry.Type != "dir" || !bfclDatePattern.MatchString(entry.Name) {
			continue
		}
		if _, err := time.Parse("2006-01-02", entry.Name); err != nil {
			continue
		}
		if entry.Name > latest {
			latest = entry.Name
		}
	}
	if latest == "" {
		return ImportedSource{}, fmt.Errorf("BFCL repository contains no dated result directory")
	}
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://raw.githubusercontent.com/" + bfclRepository + "/" + commit + "/" + latest + "/score/data_overall.csv",
		Filename: "bfcl-overall.csv", AllowedHosts: githubRawHosts,
	})
	if err != nil {
		return ImportedSource{}, err
	}
	csvContents, err := os.ReadFile(download.Path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read BFCL results: %w", err)
	}
	resolver, err := NewCanonicalResolver(models, nil)
	if err != nil {
		return ImportedSource{}, err
	}
	return importBFCL(csvContents, Source{
		ID: "bfcl", Name: "Berkeley Function Calling Leaderboard", URL: "https://github.com/" + bfclRepository,
		License: "Apache-2.0", Version: commit + ":" + latest, RetrievedAt: now.UTC(), SHA256: download.SHA256,
	}, resolver)
}

func importBFCL(contents []byte, source Source, resolver CanonicalResolver) (ImportedSource, error) {
	rows, err := decodeCSV(contents)
	if err != nil {
		return ImportedSource{}, err
	}
	settings := sha256.Sum256([]byte("bfcl-v4:data_overall:Overall Acc:fixed_samples=1000:variant_priority=fc>fc_thinking>bare>prompt>prompt_thinking"))
	settingsDigest := fmt.Sprintf("%x", settings)
	type candidate struct {
		priority int
		variant  string
		result   Result
	}
	selected := make(map[string]candidate)
	skipped := 0
	for index, row := range rows {
		if _, err := parsePositiveIntCell(row, "Rank"); err != nil {
			return ImportedSource{}, fmt.Errorf("BFCL row %d: %w", index+1, err)
		}
		model, err := requiredCell(row, "Model")
		if err != nil {
			return ImportedSource{}, fmt.Errorf("BFCL row %d: %w", index+1, err)
		}
		model, variant, priority := bfclPresentation(model)
		scoreText, err := requiredCell(row, "Overall Acc")
		if err != nil {
			return ImportedSource{}, fmt.Errorf("BFCL row %d: %w", index+1, err)
		}
		score, err := strictPercentBPS(scoreText)
		if err != nil {
			return ImportedSource{}, fmt.Errorf("BFCL row %d: %w", index+1, err)
		}
		modelID, found := resolver.Resolve(model)
		if !found {
			skipped++
			continue
		}
		lower, upper := wilsonIntervalBPS(score, 1000)
		result := Result{
			SourceID: source.ID, Benchmark: "bfcl-v4-overall", Domain: "tool_use", ModelID: modelID,
			Metric: MetricBounded, ScoreBPS: score, LowerBPS: lower, UpperBPS: upper,
			Samples: 1000, SettingsSHA256: settingsDigest,
		}
		if current, exists := selected[modelID]; exists {
			if priority < current.priority {
				continue
			}
			if priority == current.priority {
				return ImportedSource{}, fmt.Errorf("BFCL contains duplicate model %s for %s results", modelID, variant)
			}
		}
		selected[modelID] = candidate{priority: priority, variant: variant, result: result}
	}
	results := make([]Result, 0, len(selected))
	for _, candidate := range selected {
		results = append(results, candidate.result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ModelID < results[j].ModelID })
	return ImportedSource{Source: source, Results: results, Imported: len(results), SkippedUnmapped: skipped}, nil
}

func bfclPresentation(model string) (string, string, int) {
	model = strings.TrimSpace(model)
	for _, presentation := range bfclPresentationSuffixes {
		if strings.HasSuffix(model, presentation.suffix) {
			return strings.TrimSpace(strings.TrimSuffix(model, presentation.suffix)), presentation.variant, presentation.priority
		}
	}
	return model, "bare", 3
}

func strictPercentBPS(value string) (int, error) {
	value = strings.TrimSpace(value)
	if !strictPercentPattern.MatchString(value) {
		return 0, fmt.Errorf("invalid percentage %q", value)
	}
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("invalid percentage %q", value)
	}
	return int(math.Round(parsed * 100)), nil
}
