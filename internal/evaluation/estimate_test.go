package evaluation

import (
	"math"
	"testing"
	"time"
)

func TestBetaQuantileHandlesUniformAndSkewedPosteriors(t *testing.T) {
	for _, test := range []struct {
		name        string
		probability float64
		alpha       float64
		beta        float64
		want        float64
	}{
		{name: "uniform lower", probability: 0.05, alpha: 1, beta: 1, want: 0.05},
		{name: "uniform upper", probability: 0.95, alpha: 1, beta: 1, want: 0.95},
		{name: "right skew lower", probability: 0.05, alpha: 1, beta: 9, want: 1 - math.Pow(0.95, 1.0/9)},
		{name: "right skew upper", probability: 0.95, alpha: 1, beta: 9, want: 1 - math.Pow(0.05, 1.0/9)},
		{name: "left skew lower", probability: 0.05, alpha: 9, beta: 1, want: math.Pow(0.05, 1.0/9)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := betaQuantile(test.probability, test.alpha, test.beta); math.Abs(got-test.want) > 1e-9 {
				t.Fatalf("betaQuantile()=%0.12f, want %0.12f", got, test.want)
			}
		})
	}
}

func TestEstimateQualityUsesConservativePosteriorAndTimeDecay(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{
		{
			Day: "2026-08-31", Strategy: "20260802-001", Route: "balanced",
			CandidateModel: "fast", ReferenceModel: "strong", Samples: 80,
			CandidateWins: 72, Ties: 4, ReferenceWins: 4, SevereErrors: 1,
			SelfEscalationEligibleSamples: 40, SelfEscalations: 10,
			SupportedSelfEscalations: 8, UnnecessarySelfEscalations: 2,
			MissedSelfEscalations: 3,
			CandidateCostMicroUSD: 800, ReferenceCostMicroUSD: 3200,
			ReviewerCostMicroUSD: 400, CandidateLatencyMS: 1600, ReferenceLatencyMS: 3200,
			Dimensions: dimensionAggregates(80, 72, 4, 4),
		},
		{
			Day: "2026-05-03", Strategy: "20260802-001", Route: "balanced",
			CandidateModel: "fast", ReferenceModel: "strong", Samples: 80,
			CandidateWins: 8, ReferenceWins: 72, SevereErrors: 40,
			Dimensions: dimensionAggregates(80, 8, 0, 72),
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
	if estimate.SelfEscalationEligibleSamples != 40 || estimate.SelfEscalations != 10 ||
		estimate.SupportedSelfEscalations != 8 || estimate.UnnecessarySelfEscalations != 2 ||
		estimate.MissedSelfEscalations != 3 || estimate.SelfEscalationPrecisionBPS != 8000 ||
		estimate.MissedSelfEscalationRateBPS != 1000 {
		t.Fatalf("self escalation estimate=%+v", estimate)
	}
}

func TestEstimateQualityUsesFiveDimensionWeightsAndPriorWhenSparse(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	dimensions := dimensionAggregates(40, 40, 0, 0)
	dimensions[DimensionCorrectness] = DimensionAggregate{
		Dimension: DimensionCorrectness, Samples: 40, ReferenceWins: 40,
	}
	estimate, ok := EstimateQuality([]EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 40,
		CandidateWins: 24, ReferenceWins: 16, Dimensions: dimensions,
	}}, EstimateRequest{
		Now: now, Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredQualityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	})
	if !ok || !estimate.Reliable || len(estimate.Dimensions) != len(ReviewDimensions) {
		t.Fatalf("estimate=%+v ok=%v", estimate, ok)
	}
	if estimate.Dimensions[DimensionCorrectness].MeanBPS >=
		estimate.Dimensions[DimensionCompleteness].MeanBPS {
		t.Fatalf("dimension weighting ignored evidence: %+v", estimate.Dimensions)
	}
	if estimate.QualityMeanBPS < 5000 || estimate.QualityMeanBPS > 7000 {
		t.Fatalf("weighted quality mean=%d", estimate.QualityMeanBPS)
	}
}

func dimensionAggregates(samples, candidateWins, ties, referenceWins int64) map[Dimension]DimensionAggregate {
	result := make(map[Dimension]DimensionAggregate, len(ReviewDimensions))
	for _, dimension := range ReviewDimensions {
		result[dimension] = DimensionAggregate{
			Dimension: dimension, Samples: samples, CandidateWins: candidateWins,
			Ties: ties, ReferenceWins: referenceWins,
		}
	}
	return result
}

func TestEstimateQualityRequiresEnoughCurrentStrategyEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "old", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 100,
		CandidateWins: 100,
		Dimensions:    dimensionAggregates(100, 100, 0, 0),
	}, {
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 5,
		CandidateWins: 5,
		Dimensions:    dimensionAggregates(5, 5, 0, 0),
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

func TestEstimateQualityTreatsTiesAsNonInferior(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	estimate, ok := EstimateQuality([]EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 100,
		Ties:       100,
		Dimensions: dimensionAggregates(100, 0, 100, 0),
	}}, EstimateRequest{
		Now: now, Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredQualityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	})
	if !ok || !estimate.Reliable || estimate.QualityLowerBPS < 9500 {
		t.Fatalf("non-inferiority estimate=%+v ok=%v", estimate, ok)
	}
}

func TestEstimateStabilityCombinesOnlineAndEvaluationEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 40,
		SevereErrors: 1, DeterministicFailures: 2,
	}}
	estimate, ok := EstimateStability(rows, OnlineStabilityAggregate{
		Samples: 40, SuccessfulCompletions: 38, CleanCompletions: 36,
		TotalLatencyMS: 4000,
	}, StabilityEstimateRequest{
		Now: now, Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredStabilityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	})
	if !ok || !estimate.Reliable || estimate.MeanBPS <= estimate.LowerBPS ||
		estimate.LowerBPS < 8000 || estimate.ExpectedLatencyMS != 100 {
		t.Fatalf("stability estimate=%+v ok=%v", estimate, ok)
	}
}

func TestEstimateStabilityRequiresEnoughRecentOnlineEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []EvidenceAggregate{{
		Day: "2026-08-31", Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong", Samples: 40,
	}}
	estimate, ok := EstimateStability(rows, OnlineStabilityAggregate{
		Samples: 100, SuccessfulCompletions: 95, CleanCompletions: 90,
		TotalLatencyMS: 10_000, EffectiveSamples: 10,
		EffectiveSuccessfulCompletions: 9.5, EffectiveCleanCompletions: 9,
		EffectiveLatencyMS: 1000,
	}, StabilityEstimateRequest{
		Now: now, Strategy: "active", Route: "balanced",
		CandidateModel: "fast", ReferenceModel: "strong",
		ConfiguredStabilityBPS: 9000, ConfiguredSevereErrorBPS: 100,
	})
	if !ok || estimate.Reliable || estimate.EffectiveOnlineSamples != 10 ||
		estimate.ExpectedLatencyMS != 100 {
		t.Fatalf("stability estimate=%+v ok=%v", estimate, ok)
	}
}
