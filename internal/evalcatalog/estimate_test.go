package evalcatalog

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEstimatePriorNormalizesPairwiseProbabilityAgainstSelfBaseline(t *testing.T) {
	catalog := catalogFixture("pairwise")
	catalog.Results[0].ScoreBPS = 4_500
	catalog.Results[0].LowerBPS = 4_000
	catalog.Results[0].UpperBPS = 5_000

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha",
		BaselineID:  "acme/baseline",
		Domain:      "reasoning",
		Now:         time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("EstimatePrior returned no prior")
	}
	if prior.MeanBPS != 9_000 || prior.LowerBPS != 8_000 {
		t.Fatalf("pairwise prior=%+v", prior)
	}
	if prior.EffectiveSamples <= 0 || prior.EffectiveSamples > 20 || len(prior.Evidence) != 1 {
		t.Fatalf("pairwise evidence=%+v", prior)
	}
}

func TestEstimatePriorHandlesInversePairwiseRecord(t *testing.T) {
	catalog := catalogFixture("inverse")
	catalog.Results[0].ModelID = "acme/baseline"
	catalog.Results[0].BaselineID = "acme/alpha"
	catalog.Results[0].ScoreBPS = 5_500
	catalog.Results[0].LowerBPS = 5_000
	catalog.Results[0].UpperBPS = 6_000

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "reasoning",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || prior.MeanBPS != 9_000 || prior.LowerBPS != 8_000 {
		t.Fatalf("inverse prior=%+v ok=%v", prior, ok)
	}
}

func TestEstimatePriorUsesConservativeBoundedScoreRatio(t *testing.T) {
	catalog := catalogFixture("bounded")
	catalog.Results = []Result{
		boundedResult("acme/alpha", 8_500, 8_000, 9_000),
		boundedResult("acme/baseline", 9_500, 9_000, 10_000),
	}

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "reasoning",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || prior.MeanBPS != 8_947 || prior.LowerBPS != 8_000 {
		t.Fatalf("bounded prior=%+v ok=%v", prior, ok)
	}
}

func TestEstimatePriorReturnsRankOnlyOrderingWithoutHardThreshold(t *testing.T) {
	catalog := catalogFixture("rank")
	digest := strings.Repeat("a", 64)
	catalog.Results = []Result{
		{SourceID: "livebench", Benchmark: "rank", Domain: "reasoning", ModelID: "acme/alpha", Metric: MetricRankOnly, Rank: 2, SettingsSHA256: digest},
		{SourceID: "livebench", Benchmark: "rank", Domain: "reasoning", ModelID: "acme/baseline", Metric: MetricRankOnly, Rank: 1, SettingsSHA256: digest},
	}

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "reasoning",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || !prior.RankingOnly || prior.MeanBPS != 0 || prior.LowerBPS != 0 || prior.RankAdvantage != -1 {
		t.Fatalf("rank prior=%+v ok=%v", prior, ok)
	}
}

func TestEstimatePriorUsesConservativeRankAcrossServingVariants(t *testing.T) {
	catalog := catalogFixture("rank-variants")
	digest := strings.Repeat("a", 64)
	catalog.Results = []Result{
		{SourceID: "livebench", Benchmark: "rank", Domain: "reasoning", ModelID: "acme/alpha", Variant: "max", Metric: MetricRankOnly, Rank: 8, SettingsSHA256: digest},
		{SourceID: "livebench", Benchmark: "rank", Domain: "reasoning", ModelID: "acme/alpha", Variant: "high", Metric: MetricRankOnly, Rank: 7, SettingsSHA256: digest},
		{SourceID: "livebench", Benchmark: "rank", Domain: "reasoning", ModelID: "acme/baseline", Metric: MetricRankOnly, Rank: 10, SettingsSHA256: digest},
	}

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "reasoning",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || !prior.RankingOnly || prior.RankAdvantage != 2 {
		t.Fatalf("rank prior=%+v ok=%v", prior, ok)
	}
	if len(prior.Evidence) != 1 || prior.Evidence[0].Variant != "max" {
		t.Fatalf("evidence=%+v", prior.Evidence)
	}
}

func TestEstimatePriorFusesFreshSourcesAndCapsEquivalentSamples(t *testing.T) {
	catalog := catalogFixture("fusion")
	digestB := strings.Repeat("b", 64)
	catalog.Sources = append(catalog.Sources, Source{
		ID: "old", Name: "Old", URL: "https://example.com/old", License: "CC-BY-4.0", Version: "v1",
		RetrievedAt: time.Date(2025, 8, 5, 0, 0, 0, 0, time.UTC), SHA256: digestB,
	})
	catalog.Results[0].ScoreBPS = 4_000
	catalog.Results[0].LowerBPS = 3_500
	catalog.Results[0].UpperBPS = 4_500
	old := catalog.Results[0]
	old.SourceID = "old"
	old.SettingsSHA256 = digestB
	old.ScoreBPS = 5_000
	old.LowerBPS = 4_500
	old.UpperBPS = 5_500
	catalog.Results = append(catalog.Results, old)

	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "reasoning",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || prior.MeanBPS <= 8_000 || prior.MeanBPS >= 9_000 {
		t.Fatalf("freshness fusion prior=%+v ok=%v", prior, ok)
	}
	if prior.EffectiveSamples > 20 || math.Abs(prior.EffectiveSamples-20) > 0.001 {
		t.Fatalf("effective samples=%f", prior.EffectiveSamples)
	}
	if len(prior.Evidence) != 2 {
		t.Fatalf("evidence=%+v", prior.Evidence)
	}
}

func TestEstimatePriorUsesGeneralEvidenceForSpecificDomain(t *testing.T) {
	catalog := catalogFixture("general")
	catalog.Results[0].Domain = "general"
	prior, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "coding",
		Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if !ok || prior.MeanBPS == 0 {
		t.Fatalf("general prior=%+v ok=%v", prior, ok)
	}
}

func TestEstimatePriorCanRequireExactDomainEvidence(t *testing.T) {
	catalog := catalogFixture("general")
	catalog.Results[0].Domain = "general"
	_, ok := EstimatePrior(catalog, PriorRequest{
		CandidateID: "acme/alpha", BaselineID: "acme/baseline", Domain: "coding",
		ExactDomainOnly: true,
		Now:             time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if ok {
		t.Fatal("general evidence was accepted as exact coding coverage")
	}
}

func TestEstimatePriorMapsImportedEvaluationDomainsConservatively(t *testing.T) {
	tests := []struct {
		evidenceDomain  string
		requestedDomain string
		wantSamples     float64
	}{
		{evidenceDomain: "language", requestedDomain: "general", wantSamples: 15},
		{evidenceDomain: "language", requestedDomain: "simple", wantSamples: 10},
		{evidenceDomain: "instruction_following", requestedDomain: "general", wantSamples: 15},
		{evidenceDomain: "instruction_following", requestedDomain: "coding", wantSamples: 5},
	}
	for _, tt := range tests {
		t.Run(tt.evidenceDomain+"_to_"+tt.requestedDomain, func(t *testing.T) {
			catalog := catalogFixture("mapped-domain")
			catalog.Results[0].Domain = tt.evidenceDomain
			prior, ok := EstimatePrior(catalog, PriorRequest{
				CandidateID: "acme/alpha", BaselineID: "acme/baseline",
				Domain: tt.requestedDomain,
				Now:    time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
			})
			if !ok || math.Abs(prior.EffectiveSamples-tt.wantSamples) > 0.1 {
				t.Fatalf("prior=%+v ok=%v", prior, ok)
			}
		})
	}
}

func boundedResult(model string, score, lower, upper int) Result {
	return Result{
		SourceID: "livebench", Benchmark: "bounded", Domain: "reasoning", ModelID: model,
		Metric: MetricBounded, ScoreBPS: score, LowerBPS: lower, UpperBPS: upper, Samples: 50,
		SettingsSHA256: strings.Repeat("a", 64),
	}
}
