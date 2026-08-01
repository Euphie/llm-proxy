package evaluation

import (
	"testing"
	"time"
)

func TestEstimateQualityUsesConservativePosteriorAndTimeDecay(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{
		{
			Day: "2026-08-31", Strategy: "20260802-001", Route: "balanced",
			CandidateModel: "fast", ReferenceModel: "strong", Samples: 80,
			CandidateWins: 72, Ties: 4, ReferenceWins: 4, SevereErrors: 1,
			CandidateCostMicroUSD: 800, ReferenceCostMicroUSD: 3200,
			ReviewerCostMicroUSD: 400, CandidateLatencyMS: 1600, ReferenceLatencyMS: 3200,
		},
		{
			Day: "2026-05-03", Strategy: "20260802-001", Route: "balanced",
			CandidateModel: "fast", ReferenceModel: "strong", Samples: 80,
			CandidateWins: 8, ReferenceWins: 72, SevereErrors: 40,
		},
	}

	estimate, ok := EstimateQuality(rows, EstimateRequest{
		Now: now, Strategy: "20260802-001", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredQualityBPS: 9200, ConfiguredSevereErrorBPS: 50,
	})
	if !ok {
		t.Fatal("EstimateQuality() returned no evidence")
	}
	if estimate.RawSamples != 160 || estimate.EffectiveSamples <= 80 || estimate.EffectiveSamples >= 100 {
		t.Fatalf("sample counts=%+v", estimate)
	}
	if estimate.QualityLowerBPS >= estimate.QualityMeanBPS ||
		estimate.SevereErrorUpperBPS <= estimate.SevereErrorMeanBPS {
		t.Fatalf("intervals are not conservative: %+v", estimate)
	}
	if estimate.QualityMeanBPS < 8500 || estimate.SevereErrorMeanBPS > 500 {
		t.Fatalf("old evidence was not decayed: %+v", estimate)
	}
	if estimate.CandidateCostMicroUSD != 800 || estimate.ReferenceCostMicroUSD != 3200 ||
		estimate.ReviewerCostMicroUSD != 400 {
		t.Fatalf("cost totals=%+v", estimate)
	}
}

func TestEstimateQualityRequiresEnoughCurrentStrategyEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "old", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 100,
		CandidateWins: 100,
	}, {
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 5,
		CandidateWins: 5,
	}}

	estimate, ok := EstimateQuality(rows, EstimateRequest{
		Now: now, Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredQualityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	})
	if !ok || estimate.RawSamples != 5 || estimate.Reliable {
		t.Fatalf("estimate=%+v ok=%v", estimate, ok)
	}
	if _, ok := EstimateQuality(rows, EstimateRequest{
		Now: now, Strategy: "missing", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredQualityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	}); ok {
		t.Fatal("unrelated evidence produced an estimate")
	}
}
