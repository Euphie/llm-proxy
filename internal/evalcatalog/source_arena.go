package evalcatalog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/parquet-go/parquet-go"
)

const arenaDataset = "lmarena-ai/leaderboard-dataset"

type arenaRow struct {
	ModelName              string `parquet:"model_name"`
	Rank                   int64  `parquet:"rank"`
	Category               string `parquet:"category"`
	LeaderboardPublishDate string `parquet:"leaderboard_publish_date"`
}

type arenaAdapter struct{}

type arenaServingVariant struct {
	base    string
	variant string
}

var arenaServingVariants = map[string]arenaServingVariant{
	"claude-opus-5-high":   {base: "claude-opus-5", variant: "high"},
	"claude-opus-5-max":    {base: "claude-opus-5", variant: "max"},
	"claude-sonnet-5-high": {base: "claude-sonnet-5", variant: "high"},
	"glm-5.2-max":          {base: "glm-5.2", variant: "max"},
}

func NewArenaAdapter() SourceAdapter { return arenaAdapter{} }

func (arenaAdapter) ID() string { return "arena" }

func (arenaAdapter) Load(
	ctx context.Context,
	downloader Downloader,
	models modelcatalog.Catalog,
	now time.Time,
	directory string,
) (ImportedSource, error) {
	revision, err := resolveHuggingFaceDatasetRevision(ctx, downloader, directory, arenaDataset, "arena-metadata.json")
	if err != nil {
		return ImportedSource{}, err
	}
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://huggingface.co/datasets/" + arenaDataset + "/resolve/" + revision + "/text_style_control/latest-00000-of-00001.parquet",
		Filename: "arena.parquet", AllowedHosts: huggingFaceHosts,
	})
	if err != nil {
		return ImportedSource{}, err
	}
	resolver, err := NewCanonicalResolver(models, nil)
	if err != nil {
		return ImportedSource{}, err
	}
	return importArena(download.Path, Source{
		ID: "arena", Name: "LMArena", URL: "https://huggingface.co/datasets/" + arenaDataset,
		License: "CC-BY-4.0", Version: revision, RetrievedAt: now.UTC(), SHA256: download.SHA256,
	}, resolver)
}

func importArena(path string, source Source, resolver CanonicalResolver) (ImportedSource, error) {
	rows, err := parquet.ReadFile[arenaRow](path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read Arena parquet: %w", err)
	}
	settings := sha256.Sum256([]byte("lmarena:text_style_control:category=overall:rank"))
	settingsDigest := fmt.Sprintf("%x", settings)
	seenRanks := make(map[int64]struct{})
	seenModels := make(map[string]struct{})
	results := make([]Result, 0)
	skipped := 0
	for index, row := range rows {
		if strings.ToLower(strings.TrimSpace(row.Category)) != "overall" {
			continue
		}
		if row.Rank <= 0 {
			return ImportedSource{}, fmt.Errorf("arena row %d has invalid rank", index+1)
		}
		if _, duplicate := seenRanks[row.Rank]; duplicate {
			return ImportedSource{}, fmt.Errorf("arena contains duplicate overall rank %d", row.Rank)
		}
		seenRanks[row.Rank] = struct{}{}
		modelID, variant, found := resolveArenaModel(resolver, row.ModelName)
		if !found {
			skipped++
			continue
		}
		modelVariant := modelID + "\x00" + variant
		if _, duplicate := seenModels[modelVariant]; duplicate {
			return ImportedSource{}, fmt.Errorf("arena contains duplicate overall model %s variant %s", modelID, variant)
		}
		seenModels[modelVariant] = struct{}{}
		results = append(results, Result{
			SourceID: source.ID, Benchmark: "lmarena-overall", Domain: "general", ModelID: modelID,
			Variant: variant, Metric: MetricRankOnly, Rank: int(row.Rank), SettingsSHA256: settingsDigest,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Rank < results[j].Rank })
	return ImportedSource{Source: source, Results: results, Imported: len(results), SkippedUnmapped: skipped}, nil
}

func resolveArenaModel(resolver CanonicalResolver, identifier string) (string, string, bool) {
	if modelID, found := resolver.Resolve(identifier); found {
		return modelID, "", true
	}
	variant, declared := arenaServingVariants[normalizeCanonicalIdentifier(identifier)]
	if !declared {
		return "", "", false
	}
	modelID, found := resolver.Resolve(variant.base)
	return modelID, variant.variant, found
}
