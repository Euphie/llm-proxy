package evalcatalog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/parquet-go/parquet-go"
)

const liveBenchDataset = "livebench/model_judgment"

type liveBenchRow struct {
	Model    string  `parquet:"model"`
	Score    float64 `parquet:"score"`
	Category string  `parquet:"category"`
}

type liveBenchAggregate struct {
	total float64
	count int
}

type liveBenchAdapter struct{}

func NewLiveBenchAdapter() SourceAdapter { return liveBenchAdapter{} }

func (liveBenchAdapter) ID() string { return "livebench" }

func (liveBenchAdapter) Load(
	ctx context.Context,
	downloader Downloader,
	models modelcatalog.Catalog,
	now time.Time,
	directory string,
) (ImportedSource, error) {
	revision, err := resolveHuggingFaceDatasetRevision(ctx, downloader, directory, liveBenchDataset, "livebench-metadata.json")
	if err != nil {
		return ImportedSource{}, err
	}
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://huggingface.co/datasets/" + liveBenchDataset + "/resolve/" + revision + "/data/leaderboard-00000-of-00001.parquet",
		Filename: "livebench.parquet", AllowedHosts: huggingFaceHosts,
	})
	if err != nil {
		return ImportedSource{}, err
	}
	resolver, err := NewCanonicalResolver(models, nil)
	if err != nil {
		return ImportedSource{}, err
	}
	return importLiveBench(download.Path, Source{
		ID: "livebench", Name: "LiveBench", URL: "https://huggingface.co/datasets/" + liveBenchDataset,
		License: "Apache-2.0", Version: revision, RetrievedAt: now.UTC(), SHA256: download.SHA256,
	}, resolver)
}

func importLiveBench(path string, source Source, resolver CanonicalResolver) (ImportedSource, error) {
	rows, err := parquet.ReadFile[liveBenchRow](path)
	if err != nil {
		return ImportedSource{}, fmt.Errorf("read LiveBench parquet: %w", err)
	}
	if len(rows) == 0 {
		return ImportedSource{}, fmt.Errorf("LiveBench parquet contains no rows")
	}
	aggregates := make(map[string]map[string]*liveBenchAggregate)
	skipped := 0
	for index, row := range rows {
		if strings.TrimSpace(row.Model) == "" || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) || row.Score < 0 || row.Score > 1 {
			return ImportedSource{}, fmt.Errorf("LiveBench row %d has invalid model or score", index+1)
		}
		modelID, found := resolver.Resolve(row.Model)
		if !found {
			skipped++
			continue
		}
		modelAggregates := aggregates[modelID]
		if modelAggregates == nil {
			modelAggregates = make(map[string]*liveBenchAggregate)
			aggregates[modelID] = modelAggregates
		}
		addLiveBenchScore(modelAggregates, "general", row.Score)
		if domain := liveBenchDomain(row.Category); domain != "" {
			addLiveBenchScore(modelAggregates, domain, row.Score)
		}
	}
	settings := sha256.Sum256([]byte("livebench:model,score,category:general,reasoning,math,coding,language,instruction_following"))
	settingsDigest := fmt.Sprintf("%x", settings)
	modelIDs := make([]string, 0, len(aggregates))
	for modelID := range aggregates {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	results := make([]Result, 0)
	for _, modelID := range modelIDs {
		domains := make([]string, 0, len(aggregates[modelID]))
		for domain := range aggregates[modelID] {
			domains = append(domains, domain)
		}
		sort.Strings(domains)
		for _, domain := range domains {
			aggregate := aggregates[modelID][domain]
			score := int(math.Round(aggregate.total / float64(aggregate.count) * 10_000))
			lower, upper := wilsonIntervalBPS(score, aggregate.count)
			benchmark := "livebench-" + domain
			if domain == "general" {
				benchmark = "livebench-overall"
			}
			results = append(results, Result{
				SourceID: source.ID, Benchmark: benchmark, Domain: domain, ModelID: modelID,
				Metric: MetricBounded, ScoreBPS: score, LowerBPS: lower, UpperBPS: upper,
				Samples: aggregate.count, SettingsSHA256: settingsDigest,
			})
		}
	}
	return ImportedSource{Source: source, Results: results, Imported: len(results), SkippedUnmapped: skipped}, nil
}

func addLiveBenchScore(aggregates map[string]*liveBenchAggregate, domain string, score float64) {
	aggregate := aggregates[domain]
	if aggregate == nil {
		aggregate = &liveBenchAggregate{}
		aggregates[domain] = aggregate
	}
	aggregate.total += score
	aggregate.count++
}

func liveBenchDomain(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "reasoning", "math", "coding", "language", "instruction_following":
		return strings.ToLower(strings.TrimSpace(category))
	case "data_analysis":
		return "reasoning"
	default:
		return ""
	}
}
