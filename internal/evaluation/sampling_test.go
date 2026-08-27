package evaluation

import (
	"testing"
	"time"
)

func TestAdaptiveSampleRateBPSUsesOnlyMatchingRecentLocalEvidence(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	key := SamplingKey{
		ProfileID: 1, Strategy: "20260805-001", Route: "simple",
		TaskType: "simple", Difficulty: "easy", Risk: "normal", VisionMode: "none",
		CandidateModel: "fast", ReferenceModel: "strong",
	}
	matching := func(day string, samples int64) EvidenceAggregate {
		return EvidenceAggregate{
			Day: day, Strategy: key.Strategy, Route: key.Route,
			TaskType: key.TaskType, Difficulty: key.Difficulty, Risk: key.Risk,
			VisionMode: key.VisionMode, CandidateModel: key.CandidateModel,
			ReferenceModel: key.ReferenceModel, Samples: samples,
			CandidateWins: samples, Dimensions: map[Dimension]DimensionAggregate{},
		}
	}

	tests := []struct {
		name string
		rows []EvidenceAggregate
		want int
	}{
		{name: "cold start keeps configured cap", want: 4000},
		{name: "unrelated evidence is ignored", rows: []EvidenceAggregate{{
			Day: "2026-08-05", Strategy: key.Strategy, Route: "other", Samples: 500,
		}}, want: 4000},
		{name: "reliable evidence halves sampling", rows: []EvidenceAggregate{
			matching("2026-08-05", 30),
		}, want: 2000},
		{name: "mature evidence quarters sampling", rows: []EvidenceAggregate{
			matching("2026-08-05", 120),
		}, want: 1000},
		{name: "old evidence decays back to cold start", rows: []EvidenceAggregate{
			matching("2025-08-05", 500),
		}, want: 4000},
		{name: "recent severe errors retain the cap", rows: []EvidenceAggregate{
			func() EvidenceAggregate {
				row := matching("2026-08-05", 120)
				row.SevereErrors = 1
				return row
			}(),
		}, want: 4000},
		{name: "contradictory daily outcomes retain the cap", rows: []EvidenceAggregate{
			matching("2026-08-05", 30),
			func() EvidenceAggregate {
				row := matching("2026-08-04", 30)
				row.CandidateWins = 0
				row.ReferenceWins = 30
				return row
			}(),
		}, want: 4000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AdaptiveSampleRateBPS(4000, now, key, test.rows); got != test.want {
				t.Fatalf("AdaptiveSampleRateBPS()=%d, want %d", got, test.want)
			}
		})
	}
}

func TestAdaptiveSampleRateBPSNeverExceedsConfiguredCap(t *testing.T) {
	if got := AdaptiveSampleRateBPS(3, time.Now(), SamplingKey{}, nil); got != 3 {
		t.Fatalf("AdaptiveSampleRateBPS()=%d, want 3", got)
	}
	if got := AdaptiveSampleRateBPS(10_001, time.Now(), SamplingKey{}, nil); got != 10_000 {
		t.Fatalf("AdaptiveSampleRateBPS()=%d, want 10000", got)
	}
}
