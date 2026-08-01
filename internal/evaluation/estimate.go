package evaluation

import (
	"errors"
	"math"
	"time"
)

var ErrInsufficientEvidence = errors.New("insufficient routing quality evidence")

const (
	evidenceHalfLifeDays   = 30.0
	qualityPriorStrength   = 20.0
	minimumReliableSamples = 20.0
	oneSided95Z            = 1.645
)

type EstimateRequest struct {
	Now                      time.Time
	Strategy                 string
	Route                    string
	CandidateModel           string
	ReferenceModel           string
	ConfiguredQualityBPS     int
	ConfiguredSevereErrorBPS int
}

type QualityEstimate struct {
	Strategy              string  `json:"strategy"`
	Route                 string  `json:"route"`
	CandidateModel        string  `json:"candidate_model"`
	ReferenceModel        string  `json:"reference_model"`
	RawSamples            int64   `json:"raw_samples"`
	EffectiveSamples      float64 `json:"effective_samples"`
	Reliable              bool    `json:"reliable"`
	QualityMeanBPS        int     `json:"quality_mean_bps"`
	QualityLowerBPS       int     `json:"quality_lower_bps"`
	SevereErrorMeanBPS    int     `json:"severe_error_mean_bps"`
	SevereErrorUpperBPS   int     `json:"severe_error_upper_bps"`
	CandidateCostMicroUSD int64   `json:"candidate_cost_micro_usd"`
	ReferenceCostMicroUSD int64   `json:"reference_cost_micro_usd"`
	ReviewerCostMicroUSD  int64   `json:"reviewer_cost_micro_usd"`
	CandidateLatencyMS    int64   `json:"candidate_latency_ms"`
	ReferenceLatencyMS    int64   `json:"reference_latency_ms"`
}

func EstimateQuality(
	rows []EvidenceAggregate,
	request EstimateRequest,
) (QualityEstimate, bool) {
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	qualitySuccess := 0.0
	qualityFailure := 0.0
	severe := 0.0
	nonSevere := 0.0
	estimate := QualityEstimate{
		Strategy: request.Strategy, Route: request.Route,
		CandidateModel: request.CandidateModel, ReferenceModel: request.ReferenceModel,
	}
	for _, row := range rows {
		if row.Strategy != request.Strategy || row.Route != request.Route ||
			row.CandidateModel != request.CandidateModel ||
			row.ReferenceModel != request.ReferenceModel {
			continue
		}
		day, err := time.Parse(dayFormat, row.Day)
		if err != nil {
			continue
		}
		ageDays := now.Sub(day).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		weight := math.Exp2(-ageDays / evidenceHalfLifeDays)
		samples := float64(max(row.Samples, int64(0)))
		qualitySuccess += weight * (float64(max(row.CandidateWins, int64(0))) +
			0.5*float64(max(row.Ties, int64(0))))
		qualityFailure += weight * (float64(max(row.ReferenceWins, int64(0))) +
			0.5*float64(max(row.Ties, int64(0))))
		severeCount := float64(min(max(row.SevereErrors, int64(0)), row.Samples))
		severe += weight * severeCount
		nonSevere += weight * max(samples-severeCount, 0)
		estimate.RawSamples += row.Samples
		estimate.CandidateCostMicroUSD += row.CandidateCostMicroUSD
		estimate.ReferenceCostMicroUSD += row.ReferenceCostMicroUSD
		estimate.ReviewerCostMicroUSD += row.ReviewerCostMicroUSD
		estimate.CandidateLatencyMS += row.CandidateLatencyMS
		estimate.ReferenceLatencyMS += row.ReferenceLatencyMS
	}
	if estimate.RawSamples <= 0 {
		return QualityEstimate{}, false
	}
	estimate.EffectiveSamples = qualitySuccess + qualityFailure
	qualityMean := clampRate(float64(request.ConfiguredQualityBPS) / 10_000)
	severeMean := clampRate(float64(request.ConfiguredSevereErrorBPS) / 10_000)
	qualityAlpha := 0.5 + qualityMean*qualityPriorStrength + qualitySuccess
	qualityBeta := 0.5 + (1-qualityMean)*qualityPriorStrength + qualityFailure
	severeAlpha := 0.5 + severeMean*qualityPriorStrength + severe
	severeBeta := 0.5 + (1-severeMean)*qualityPriorStrength + nonSevere
	qualityPosterior, qualityDeviation := betaSummary(qualityAlpha, qualityBeta)
	severePosterior, severeDeviation := betaSummary(severeAlpha, severeBeta)
	estimate.QualityMeanBPS = rateBPS(qualityPosterior)
	estimate.QualityLowerBPS = rateBPS(max(qualityPosterior-oneSided95Z*qualityDeviation, 0))
	estimate.SevereErrorMeanBPS = rateBPS(severePosterior)
	estimate.SevereErrorUpperBPS = rateBPS(min(severePosterior+oneSided95Z*severeDeviation, 1))
	estimate.Reliable = estimate.EffectiveSamples >= minimumReliableSamples
	return estimate, true
}

func betaSummary(alpha float64, beta float64) (float64, float64) {
	total := alpha + beta
	mean := alpha / total
	variance := alpha * beta / (total * total * (total + 1))
	return mean, math.Sqrt(max(variance, 0))
}

func clampRate(value float64) float64 {
	return min(max(value, 0), 1)
}

func rateBPS(value float64) int {
	return min(max(int(math.Round(value*10_000)), 0), 10_000)
}
