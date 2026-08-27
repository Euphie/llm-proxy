package strategycompiler

import (
	"context"
	"sort"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
)

type EvaluationEvidenceProvider struct {
	store *evaluation.Store
	now   func() time.Time
}

func NewEvaluationEvidenceProvider(
	store *evaluation.Store,
	now func() time.Time,
) *EvaluationEvidenceProvider {
	if now == nil {
		now = time.Now
	}
	return &EvaluationEvidenceProvider{store: store, now: now}
}

func (provider *EvaluationEvidenceProvider) CandidateEstimate(
	ctx context.Context,
	key EvidenceKey,
) (LocalEstimate, bool, error) {
	if provider == nil || provider.store == nil {
		return LocalEstimate{}, false, nil
	}
	rows, err := provider.store.ListEvidence(ctx, key.ProfileID)
	if err != nil {
		return LocalEstimate{}, false, err
	}
	routeSet := make(map[string]struct{})
	for _, row := range rows {
		if row.Strategy == key.Strategy && row.CandidateModel == key.CandidateModel &&
			row.ReferenceModel == key.ReferenceModel && row.TaskType == key.Domain &&
			(key.Difficulty == "" || row.Difficulty == key.Difficulty) {
			routeSet[row.Route] = struct{}{}
		}
	}
	routes := make([]string, 0, len(routeSet))
	for route := range routeSet {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	best := LocalEstimate{}
	found := false
	for _, route := range routes {
		quality, ok := evaluation.EstimateQuality(rows, evaluation.EstimateRequest{
			Now: provider.now(), Strategy: key.Strategy, Route: route,
			CandidateModel: key.CandidateModel, ReferenceModel: key.ReferenceModel,
			TaskType: key.Domain, Difficulty: key.Difficulty,
			ConfiguredQualityBPS: 8_000, ConfiguredSevereErrorBPS: 500,
		})
		if !ok {
			continue
		}
		online, err := provider.store.OnlineStabilityFor(
			ctx, key.ProfileID, key.Strategy, route, key.CandidateModel,
			key.Domain, key.Difficulty,
		)
		if err != nil {
			return LocalEstimate{}, false, err
		}
		stability, stabilityOK := evaluation.EstimateStability(
			rows, online, evaluation.StabilityEstimateRequest{
				Now: provider.now(), Strategy: key.Strategy, Route: route,
				CandidateModel: key.CandidateModel, ReferenceModel: key.ReferenceModel,
				TaskType: key.Domain, Difficulty: key.Difficulty,
				ConfiguredStabilityBPS: 8_000, ConfiguredSevereErrorBPS: 500,
			},
		)
		estimate := LocalEstimate{
			QualityMeanBPS: quality.QualityMeanBPS, QualityLowerBPS: quality.QualityLowerBPS,
			SevereErrorUpperBPS: quality.SevereErrorUpperBPS,
			EffectiveSamples:    quality.EffectiveSamples,
			Reliable:            quality.Reliable && stabilityOK && stability.Reliable,
		}
		if stabilityOK {
			estimate.StabilityLowerBPS = stability.LowerBPS
			estimate.ExpectedLatencyMS = stability.ExpectedLatencyMS
		}
		if !found || estimate.Reliable && !best.Reliable ||
			estimate.Reliable == best.Reliable && moreConservativeEstimate(estimate, best) {
			best, found = estimate, true
		}
	}
	return best, found, nil
}

func moreConservativeEstimate(candidate, current LocalEstimate) bool {
	if candidate.QualityLowerBPS != current.QualityLowerBPS {
		return candidate.QualityLowerBPS < current.QualityLowerBPS
	}
	if candidate.StabilityLowerBPS != current.StabilityLowerBPS {
		return candidate.StabilityLowerBPS < current.StabilityLowerBPS
	}
	if candidate.SevereErrorUpperBPS != current.SevereErrorUpperBPS {
		return candidate.SevereErrorUpperBPS > current.SevereErrorUpperBPS
	}
	return candidate.EffectiveSamples < current.EffectiveSamples
}
