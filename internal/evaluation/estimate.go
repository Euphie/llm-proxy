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
)

type EstimateRequest struct {
	Now                      time.Time
	Strategy                 string
	Route                    string
	CandidateModel           string
	ReferenceModel           string
	TaskType                 string
	Difficulty               string
	Risk                     string
	VisionMode               string
	ConfiguredQualityBPS     int
	ConfiguredSevereErrorBPS int
}

type DimensionEstimate struct {
	Dimension        Dimension `json:"dimension"`
	WeightBPS        int       `json:"weight_bps"`
	RawSamples       int64     `json:"raw_samples"`
	EffectiveSamples float64   `json:"effective_samples"`
	MeanBPS          int       `json:"mean_bps"`
	LowerBPS         int       `json:"lower_bps"`
	Reliable         bool      `json:"reliable"`
}

type QualityEstimate struct {
	Strategy                      string                          `json:"strategy"`
	Route                         string                          `json:"route"`
	CandidateModel                string                          `json:"candidate_model"`
	ReferenceModel                string                          `json:"reference_model"`
	RawSamples                    int64                           `json:"raw_samples"`
	EffectiveSamples              float64                         `json:"effective_samples"`
	Reliable                      bool                            `json:"reliable"`
	QualityMeanBPS                int                             `json:"quality_mean_bps"`
	QualityLowerBPS               int                             `json:"quality_lower_bps"`
	SevereErrorMeanBPS            int                             `json:"severe_error_mean_bps"`
	SevereErrorUpperBPS           int                             `json:"severe_error_upper_bps"`
	CandidateCostMicroUSD         int64                           `json:"candidate_cost_micro_usd"`
	ReferenceCostMicroUSD         int64                           `json:"reference_cost_micro_usd"`
	ReviewerCostMicroUSD          int64                           `json:"reviewer_cost_micro_usd"`
	CandidateLatencyMS            int64                           `json:"candidate_latency_ms"`
	ReferenceLatencyMS            int64                           `json:"reference_latency_ms"`
	SelfEscalationEligibleSamples int64                           `json:"self_escalation_eligible_samples"`
	SelfEscalations               int64                           `json:"self_escalations"`
	SupportedSelfEscalations      int64                           `json:"supported_self_escalations"`
	UnnecessarySelfEscalations    int64                           `json:"unnecessary_self_escalations"`
	MissedSelfEscalations         int64                           `json:"missed_self_escalations"`
	SelfEscalationPrecisionBPS    int                             `json:"self_escalation_precision_bps"`
	MissedSelfEscalationRateBPS   int                             `json:"missed_self_escalation_rate_bps"`
	Dimensions                    map[Dimension]DimensionEstimate `json:"dimensions"`
}

type PerformanceFilter struct {
	ProfileID  *int64
	Model      string
	From       time.Time
	To         time.Time
	TaskType   string
	Difficulty string
	Risk       string
	VisionMode string
	Page       int
	PageSize   int
}

type PerformanceItem struct {
	ProfileID      int64           `json:"profile_id"`
	ProfileSlug    string          `json:"profile_slug"`
	ProfileName    string          `json:"profile_name"`
	Strategy       string          `json:"strategy"`
	Route          string          `json:"route"`
	TaskType       string          `json:"task_type"`
	Difficulty     string          `json:"difficulty"`
	Risk           string          `json:"risk"`
	VisionMode     string          `json:"vision_mode"`
	CandidateModel string          `json:"candidate_model"`
	ReferenceModel string          `json:"reference_model"`
	PriorKnown     bool            `json:"prior_known"`
	Estimate       QualityEstimate `json:"estimate"`
}

type PerformanceSummary struct {
	Groups                int   `json:"groups"`
	ReliableGroups        int   `json:"reliable_groups"`
	RawSamples            int64 `json:"raw_samples"`
	CandidateCostMicroUSD int64 `json:"candidate_cost_micro_usd"`
	ReferenceCostMicroUSD int64 `json:"reference_cost_micro_usd"`
	ReviewerCostMicroUSD  int64 `json:"reviewer_cost_micro_usd"`
}

type PerformancePage struct {
	Items      []PerformanceItem  `json:"items"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	Total      int                `json:"total"`
	TotalPages int                `json:"total_pages"`
	Summary    PerformanceSummary `json:"summary"`
}

func EstimateQuality(
	rows []EvidenceAggregate,
	request EstimateRequest,
) (QualityEstimate, bool) {
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	severe := 0.0
	nonSevere := 0.0
	eligibleEscalations := 0.0
	requestedEscalations := 0.0
	supportedEscalations := 0.0
	missedEscalations := 0.0
	estimate := QualityEstimate{
		Strategy: request.Strategy, Route: request.Route,
		CandidateModel: request.CandidateModel, ReferenceModel: request.ReferenceModel,
		Dimensions: make(map[Dimension]DimensionEstimate, len(ReviewDimensions)),
	}
	dimensionSuccess := make(map[Dimension]float64, len(ReviewDimensions))
	dimensionFailure := make(map[Dimension]float64, len(ReviewDimensions))
	dimensionSamples := make(map[Dimension]int64, len(ReviewDimensions))
	for _, row := range rows {
		if row.Strategy != request.Strategy || row.Route != request.Route ||
			row.CandidateModel != request.CandidateModel ||
			row.ReferenceModel != request.ReferenceModel ||
			(request.TaskType != "" && row.TaskType != request.TaskType) ||
			(request.Difficulty != "" && row.Difficulty != request.Difficulty) ||
			(request.Risk != "" && row.Risk != request.Risk) ||
			(request.VisionMode != "" && row.VisionMode != request.VisionMode) {
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
		for _, dimension := range ReviewDimensions {
			aggregate, ok := row.Dimensions[dimension]
			if !ok {
				continue
			}
			dimensionSuccess[dimension] += weight * (float64(max(aggregate.CandidateWins, int64(0))) +
				0.5*float64(max(aggregate.Ties, int64(0))))
			dimensionFailure[dimension] += weight * (float64(max(aggregate.ReferenceWins, int64(0))) +
				0.5*float64(max(aggregate.Ties, int64(0))))
			dimensionSamples[dimension] += aggregate.Samples
		}
		severeCount := float64(min(max(row.SevereErrors, int64(0)), row.Samples))
		severe += weight * severeCount
		nonSevere += weight * max(samples-severeCount, 0)
		eligibleEscalations += weight * float64(max(row.SelfEscalationEligibleSamples, int64(0)))
		requestedEscalations += weight * float64(max(row.SelfEscalations, int64(0)))
		supportedEscalations += weight * float64(max(row.SupportedSelfEscalations, int64(0)))
		missedEscalations += weight * float64(max(row.MissedSelfEscalations, int64(0)))
		estimate.RawSamples += row.Samples
		estimate.SelfEscalationEligibleSamples += row.SelfEscalationEligibleSamples
		estimate.SelfEscalations += row.SelfEscalations
		estimate.SupportedSelfEscalations += row.SupportedSelfEscalations
		estimate.UnnecessarySelfEscalations += row.UnnecessarySelfEscalations
		estimate.MissedSelfEscalations += row.MissedSelfEscalations
		estimate.CandidateCostMicroUSD += row.CandidateCostMicroUSD
		estimate.ReferenceCostMicroUSD += row.ReferenceCostMicroUSD
		estimate.ReviewerCostMicroUSD += row.ReviewerCostMicroUSD
		estimate.CandidateLatencyMS += row.CandidateLatencyMS
		estimate.ReferenceLatencyMS += row.ReferenceLatencyMS
	}
	if estimate.RawSamples <= 0 {
		return QualityEstimate{}, false
	}
	qualityMean := clampRate(float64(request.ConfiguredQualityBPS) / 10_000)
	severeMean := clampRate(float64(request.ConfiguredSevereErrorBPS) / 10_000)
	weightedMean := 0
	weightedLower := 0
	reliable := true
	totalEffective := 0.0
	for _, dimension := range ReviewDimensions {
		success := dimensionSuccess[dimension]
		failure := dimensionFailure[dimension]
		effective := success + failure
		alpha := max(qualityMean*qualityPriorStrength+success, 0.000001)
		beta := max((1-qualityMean)*qualityPriorStrength+failure, 0.000001)
		mean := betaMean(alpha, beta)
		dimensionEstimate := DimensionEstimate{
			Dimension: dimension, WeightBPS: DimensionWeightsBPS[dimension],
			RawSamples: dimensionSamples[dimension], EffectiveSamples: effective,
			MeanBPS:  rateBPS(mean),
			LowerBPS: rateBPS(betaQuantile(0.05, alpha, beta)),
			Reliable: effective >= minimumReliableSamples,
		}
		estimate.Dimensions[dimension] = dimensionEstimate
		weightedMean += dimensionEstimate.MeanBPS * dimensionEstimate.WeightBPS
		weightedLower += dimensionEstimate.LowerBPS * dimensionEstimate.WeightBPS
		totalEffective += effective
		reliable = reliable && dimensionEstimate.Reliable
	}
	estimate.EffectiveSamples = totalEffective / float64(len(ReviewDimensions))
	estimate.QualityMeanBPS = weightedMean / 10_000
	estimate.QualityLowerBPS = weightedLower / 10_000
	severeAlpha := max(severeMean*qualityPriorStrength+severe, 0.000001)
	severeBeta := max((1-severeMean)*qualityPriorStrength+nonSevere, 0.000001)
	severePosterior := betaMean(severeAlpha, severeBeta)
	estimate.SevereErrorMeanBPS = rateBPS(severePosterior)
	estimate.SevereErrorUpperBPS = rateBPS(betaQuantile(0.95, severeAlpha, severeBeta))
	if requestedEscalations > 0 {
		estimate.SelfEscalationPrecisionBPS = rateBPS(supportedEscalations / requestedEscalations)
	}
	notRequestedEscalations := max(eligibleEscalations-requestedEscalations, 0)
	if notRequestedEscalations > 0 {
		estimate.MissedSelfEscalationRateBPS = rateBPS(missedEscalations / notRequestedEscalations)
	}
	estimate.Reliable = reliable
	return estimate, true
}

func betaMean(alpha float64, beta float64) float64 {
	return alpha / (alpha + beta)
}

func betaQuantile(probability float64, alpha float64, beta float64) float64 {
	if probability <= 0 {
		return 0
	}
	if probability >= 1 {
		return 1
	}
	lower, upper := 0.0, 1.0
	for range 120 {
		mid := lower + (upper-lower)/2
		if regularizedIncompleteBeta(mid, alpha, beta) < probability {
			lower = mid
		} else {
			upper = mid
		}
	}
	return lower + (upper-lower)/2
}

func regularizedIncompleteBeta(x float64, alpha float64, beta float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	logAlphaBeta, _ := math.Lgamma(alpha + beta)
	logAlpha, _ := math.Lgamma(alpha)
	logBeta, _ := math.Lgamma(beta)
	front := math.Exp(logAlphaBeta - logAlpha - logBeta + alpha*math.Log(x) + beta*math.Log1p(-x))
	if x < (alpha+1)/(alpha+beta+2) {
		return clampRate(front * betaContinuedFraction(alpha, beta, x) / alpha)
	}
	return clampRate(1 - front*betaContinuedFraction(beta, alpha, 1-x)/beta)
}

func betaContinuedFraction(alpha float64, beta float64, x float64) float64 {
	const (
		maxIterations = 300
		epsilon       = 3e-14
		minimum       = 1e-300
	)
	qab := alpha + beta
	qap := alpha + 1
	qam := alpha - 1
	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < minimum {
		d = minimum
	}
	d = 1 / d
	h := d
	for iteration := 1; iteration <= maxIterations; iteration++ {
		m := float64(iteration)
		m2 := 2 * m
		aa := m * (beta - m) * x / ((qam + m2) * (alpha + m2))
		d = 1 + aa*d
		if math.Abs(d) < minimum {
			d = minimum
		}
		c = 1 + aa/c
		if math.Abs(c) < minimum {
			c = minimum
		}
		d = 1 / d
		h *= d * c
		aa = -(alpha + m) * (qab + m) * x / ((alpha + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < minimum {
			d = minimum
		}
		c = 1 + aa/c
		if math.Abs(c) < minimum {
			c = minimum
		}
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < epsilon {
			break
		}
	}
	return h
}

func clampRate(value float64) float64 {
	return min(max(value, 0), 1)
}

func rateBPS(value float64) int {
	return min(max(int(math.Round(value*10_000)), 0), 10_000)
}
