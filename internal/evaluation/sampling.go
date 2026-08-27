package evaluation

import (
	"context"
	"math"
	"time"
)

type SamplingKey struct {
	ProfileID      int64
	Strategy       string
	Route          string
	TaskType       string
	Difficulty     string
	Risk           string
	VisionMode     string
	CandidateModel string
	ReferenceModel string
}

func (s *Store) AdaptiveSampleRate(
	ctx context.Context,
	configuredCapBPS int,
	key SamplingKey,
) (int, error) {
	rows, err := s.ListEvidence(ctx, key.ProfileID)
	if err != nil {
		return 0, err
	}
	return AdaptiveSampleRateBPS(configuredCapBPS, s.now(), key, rows), nil
}

func AdaptiveSampleRateBPS(
	configuredCapBPS int,
	now time.Time,
	key SamplingKey,
	rows []EvidenceAggregate,
) int {
	capBPS := min(max(configuredCapBPS, 1), 10_000)
	if !completeSamplingKey(key) {
		return capBPS
	}
	if now.IsZero() {
		now = time.Now()
	}
	effectiveSamples := 0.0
	dailyDirections := make(map[string][2]int64)
	for _, row := range rows {
		if !samplingRowMatches(key, row) {
			continue
		}
		day, err := time.Parse(dayFormat, row.Day)
		if err != nil {
			continue
		}
		ageDays := max(now.UTC().Sub(day).Hours()/24, 0)
		weight := math.Exp2(-ageDays / evidenceHalfLifeDays)
		effectiveSamples += weight * float64(max(row.Samples, int64(0)))
		if weight >= 0.25 && (row.SevereErrors > 0 || row.DeterministicFailures > 0) {
			return capBPS
		}
		direction := dailyDirections[row.Day]
		direction[0] += row.CandidateWins + row.Ties
		direction[1] += row.ReferenceWins
		dailyDirections[row.Day] = direction
	}
	if hasDirectionalConflict(dailyDirections) || effectiveSamples < minimumReliableSamples {
		return capBPS
	}
	if effectiveSamples < 100 {
		return max(capBPS/2, 1)
	}
	return max(capBPS/4, 1)
}

func completeSamplingKey(key SamplingKey) bool {
	return key.ProfileID > 0 && key.Strategy != "" && key.Route != "" &&
		key.TaskType != "" && key.Difficulty != "" && key.Risk != "" &&
		key.VisionMode != "" && key.CandidateModel != "" && key.ReferenceModel != ""
}

func samplingRowMatches(key SamplingKey, row EvidenceAggregate) bool {
	return row.Strategy == key.Strategy && row.Route == key.Route &&
		row.TaskType == key.TaskType && row.Difficulty == key.Difficulty &&
		row.Risk == key.Risk && row.VisionMode == key.VisionMode &&
		row.CandidateModel == key.CandidateModel && row.ReferenceModel == key.ReferenceModel
}

func hasDirectionalConflict(days map[string][2]int64) bool {
	hasCandidateDirection := false
	hasReferenceDirection := false
	for _, outcomes := range days {
		total := outcomes[0] + outcomes[1]
		if total <= 0 {
			continue
		}
		noninferiorRate := float64(outcomes[0]) / float64(total)
		hasCandidateDirection = hasCandidateDirection || noninferiorRate >= 0.60
		hasReferenceDirection = hasReferenceDirection || noninferiorRate <= 0.40
	}
	return hasCandidateDirection && hasReferenceDirection
}

func conditionalSampleRateBPS(targetBPS int, configuredCapBPS int) int {
	capBPS := min(max(configuredCapBPS, 1), 10_000)
	targetBPS = min(max(targetBPS, 1), capBPS)
	return min(max((targetBPS*10_000+capBPS-1)/capBPS, 1), 10_000)
}
