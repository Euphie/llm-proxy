package evalcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

const sweBenchRepository = "swe-bench/swe-bench.github.io"

type sweBenchAdapter struct{}

func NewSWEBenchAdapter() SourceAdapter { return sweBenchAdapter{} }

func (sweBenchAdapter) ID() string { return "swebench" }

func (sweBenchAdapter) Load(
	ctx context.Context,
	downloader Downloader,
	models modelcatalog.Catalog,
	now time.Time,
	directory string,
) (ImportedSource, error) {
	branch, err := resolveGitHubDefaultBranch(ctx, downloader, directory, sweBenchRepository, "swebench-repository.json")
	if err != nil {
		return ImportedSource{}, err
	}
	commit, err := resolveGitHubCommit(ctx, downloader, directory, sweBenchRepository, branch, "swebench-commit.json")
	if err != nil {
		return ImportedSource{}, err
	}
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://raw.githubusercontent.com/" + sweBenchRepository + "/" + commit + "/data/leaderboards.json",
		Filename: "swebench-leaderboards.json", AllowedHosts: githubRawHosts,
	})
	if err != nil {
		return ImportedSource{}, err
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read SWE-bench leaderboard: %w", err)
	}
	resolver, err := NewCanonicalResolver(models, nil)
	if err != nil {
		return ImportedSource{}, err
	}
	return importSWEBench(contents, Source{
		ID: "swebench", Name: "SWE-bench", URL: "https://github.com/" + sweBenchRepository,
		License: "MIT", Version: commit, RetrievedAt: now.UTC(), SHA256: download.SHA256,
	}, resolver)
}

func importSWEBench(contents []byte, source Source, resolver CanonicalResolver) (ImportedSource, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var payload struct {
		Leaderboards []struct {
			Name    string `json:"name"`
			Results []struct {
				Name        string      `json:"name"`
				Folder      string      `json:"folder"`
				Resolved    json.Number `json:"resolved"`
				MiniVersion any         `json:"mini-swe-agent_version"`
				Tags        []string    `json:"tags"`
			} `json:"results"`
		} `json:"leaderboards"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return ImportedSource{}, fmt.Errorf("decode SWE-bench leaderboard: %w", err)
	}
	verifiedIndex := -1
	for index, leaderboard := range payload.Leaderboards {
		if leaderboard.Name == "Verified" {
			if verifiedIndex >= 0 {
				return ImportedSource{}, fmt.Errorf("SWE-bench contains duplicate Verified leaderboards")
			}
			verifiedIndex = index
		}
	}
	if verifiedIndex < 0 {
		return ImportedSource{}, fmt.Errorf("SWE-bench contains no Verified leaderboard")
	}
	settings := sha256.Sum256([]byte("swe-bench:Verified:resolved:fixed_samples=500"))
	settingsDigest := fmt.Sprintf("%x", settings)
	results := make([]Result, 0)
	skipped := 0
	for _, row := range payload.Leaderboards[verifiedIndex].Results {
		modelTags := make([]string, 0, 1)
		systemTags := make([]string, 0)
		for _, tag := range row.Tags {
			tag = strings.TrimSpace(tag)
			if strings.HasPrefix(tag, "Model:") {
				modelTags = append(modelTags, strings.TrimSpace(strings.TrimPrefix(tag, "Model:")))
			}
			if strings.HasPrefix(tag, "System:") {
				systemTags = append(systemTags, tag)
			}
		}
		if len(modelTags) != 1 || modelTags[0] == "" {
			continue
		}
		modelID, found := resolver.Resolve(modelTags[0])
		if !found {
			skipped++
			continue
		}
		version := sweBenchVersionString(row.MiniVersion)
		scaffold, scaffoldDigest, reproducible := sweBenchScaffold(row.Name, row.Folder, version, systemTags)
		if !reproducible {
			continue
		}
		scoreFloat, err := row.Resolved.Float64()
		if err != nil || math.IsNaN(scoreFloat) || math.IsInf(scoreFloat, 0) || scoreFloat < 0 || scoreFloat > 100 {
			return ImportedSource{}, fmt.Errorf("SWE-bench model %s has invalid resolved score", modelTags[0])
		}
		score := int(math.Round(scoreFloat * 100))
		lower, upper := wilsonIntervalBPS(score, 500)
		results = append(results, Result{
			SourceID: source.ID, Benchmark: "swe-bench-verified", Domain: "coding", ModelID: modelID,
			Metric: MetricBounded, ScoreBPS: score, LowerBPS: lower, UpperBPS: upper,
			Samples: 500, SettingsSHA256: settingsDigest, Scaffold: scaffold, ScaffoldSHA256: scaffoldDigest,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ModelID != results[j].ModelID {
			return results[i].ModelID < results[j].ModelID
		}
		return results[i].ScaffoldSHA256 < results[j].ScaffoldSHA256
	})
	return ImportedSource{Source: source, Results: results, Imported: len(results), SkippedUnmapped: skipped}, nil
}

func sweBenchVersionString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(string(typed))
	case float64:
		return strings.TrimSpace(fmt.Sprint(typed))
	default:
		return ""
	}
}

func sweBenchScaffold(name, folder, miniVersion string, systemTags []string) (string, string, bool) {
	name = strings.TrimSpace(name)
	folder = strings.TrimSpace(folder)
	miniVersion = strings.TrimSpace(miniVersion)
	if name == "" || folder == "" {
		return "", "", false
	}
	tags := append([]string(nil), systemTags...)
	for index := range tags {
		tags[index] = strings.TrimSpace(tags[index])
	}
	sort.Strings(tags)
	identityParts := []string{name, folder}
	displayParts := []string{name, "folder:" + folder}
	if miniVersion != "" {
		mini := "mini-swe-agent/" + miniVersion
		identityParts = append(identityParts, mini)
		displayParts = append(displayParts, mini)
	} else if len(tags) == 0 {
		return "", "", false
	}
	identityParts = append(identityParts, tags...)
	displayParts = append(displayParts, tags...)
	identity := strings.Join(identityParts, "\x00")
	display := strings.Join(displayParts, " · ")
	digest := sha256.Sum256([]byte(identity))
	return display, fmt.Sprintf("%x", digest), true
}
