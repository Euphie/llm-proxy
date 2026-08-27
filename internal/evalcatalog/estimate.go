package evalcatalog

import (
	"math"
	"sort"
	"time"
)

const (
	publicPriorSampleCap = 20.0
	freshnessHalfLife    = 180 * 24 * time.Hour
)

type PriorRequest struct {
	CandidateID     string
	BaselineID      string
	Domain          string
	ExactDomainOnly bool
	Now             time.Time
}

type EvidenceRef struct {
	SourceID       string     `json:"source_id"`
	Benchmark      string     `json:"benchmark"`
	Domain         string     `json:"domain"`
	Variant        string     `json:"variant,omitempty"`
	Metric         MetricKind `json:"metric"`
	SettingsSHA256 string     `json:"settings_sha256"`
	Scaffold       string     `json:"scaffold,omitempty"`
	Weight         float64    `json:"weight"`
}

type Prior struct {
	MeanBPS          int           `json:"mean_bps"`
	LowerBPS         int           `json:"lower_bps"`
	EffectiveSamples float64       `json:"effective_samples"`
	RankingOnly      bool          `json:"ranking_only"`
	RankAdvantage    int           `json:"rank_advantage,omitempty"`
	Evidence         []EvidenceRef `json:"evidence"`
}

type numericPrior struct {
	meanBPS  int
	lowerBPS int
	weight   float64
	evidence EvidenceRef
}

func EstimatePrior(catalog Catalog, request PriorRequest) (Prior, bool) {
	if !canonicalModelID(request.CandidateID) || !canonicalModelID(request.BaselineID) || request.CandidateID == request.BaselineID {
		return Prior{}, false
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	sources := make(map[string]Source, len(catalog.Sources))
	for _, source := range catalog.Sources {
		sources[source.ID] = source
	}
	numeric := make([]numericPrior, 0)
	for index, result := range catalog.Results {
		domainWeight := requestedDomainWeight(result.Domain, request)
		if domainWeight == 0 {
			continue
		}
		source := sources[result.SourceID]
		switch result.Metric {
		case MetricPairwise:
			mean, lower, matched := pairwisePrior(result, request.CandidateID, request.BaselineID)
			if !matched {
				continue
			}
			weight := equivalentWeight(result.Samples, source.RetrievedAt, now) * domainWeight
			if weight > 0 {
				numeric = append(numeric, numericPrior{
					meanBPS: mean, lowerBPS: lower, weight: weight,
					evidence: evidenceRef(result, weight),
				})
			}
		case MetricBounded:
			if result.ModelID != request.CandidateID {
				continue
			}
			baseline, found := matchingBoundedResult(catalog.Results, index, result, request.BaselineID)
			if !found {
				continue
			}
			mean := conservativeRatio(result.ScoreBPS, baseline.ScoreBPS)
			lower := conservativeRatio(result.LowerBPS, baseline.UpperBPS)
			weight := equivalentWeight(min(result.Samples, baseline.Samples), source.RetrievedAt, now) * domainWeight
			if weight > 0 {
				numeric = append(numeric, numericPrior{
					meanBPS: mean, lowerBPS: lower, weight: weight,
					evidence: evidenceRef(result, weight),
				})
			}
		}
	}
	if len(numeric) > 0 {
		return fuseNumericPriors(numeric), true
	}
	return estimateRankOnly(catalog, request)
}

func pairwisePrior(result Result, candidateID, baselineID string) (int, int, bool) {
	score := result.ScoreBPS
	lower := result.LowerBPS
	if result.ModelID == baselineID && result.BaselineID == candidateID {
		score = 10_000 - result.ScoreBPS
		lower = 10_000 - result.UpperBPS
	} else if result.ModelID != candidateID || result.BaselineID != baselineID {
		return 0, 0, false
	}
	return conservativeRatio(score, 5_000), conservativeRatio(lower, 5_000), true
}

func matchingBoundedResult(results []Result, skip int, candidate Result, baselineID string) (Result, bool) {
	for index, result := range results {
		if index == skip || result.Metric != MetricBounded || result.ModelID != baselineID {
			continue
		}
		if result.SourceID == candidate.SourceID && result.Benchmark == candidate.Benchmark &&
			result.Domain == candidate.Domain && result.SettingsSHA256 == candidate.SettingsSHA256 &&
			result.ScaffoldSHA256 == candidate.ScaffoldSHA256 {
			return result, true
		}
	}
	return Result{}, false
}

func conservativeRatio(numerator, denominator int) int {
	if denominator <= 0 {
		return 0
	}
	return min(10_000, int(math.Round(float64(numerator)*10_000/float64(denominator))))
}

func priorDomainWeight(evidenceDomain, requestedDomain string) float64 {
	if evidenceDomain == requestedDomain {
		return 1
	}
	switch evidenceDomain {
	case "language":
		switch requestedDomain {
		case "general":
			return 0.75
		case "simple":
			return 0.5
		}
	case "instruction_following":
		switch requestedDomain {
		case "general", "simple":
			return 0.75
		case "reasoning", "math", "coding", "tool_use", "vision":
			return 0.25
		}
	}
	if evidenceDomain == "general" && requestedDomain != "" {
		return 0.5
	}
	return 0
}

func requestedDomainWeight(evidenceDomain string, request PriorRequest) float64 {
	if request.ExactDomainOnly {
		if evidenceDomain == request.Domain {
			return 1
		}
		return 0
	}
	return priorDomainWeight(evidenceDomain, request.Domain)
}

func equivalentWeight(samples int, retrievedAt, now time.Time) float64 {
	if samples <= 0 {
		return 0
	}
	age := now.Sub(retrievedAt)
	if age < 0 {
		age = 0
	}
	freshness := math.Exp2(-float64(age) / float64(freshnessHalfLife))
	return min(float64(samples), publicPriorSampleCap) * freshness
}

func evidenceRef(result Result, weight float64) EvidenceRef {
	return EvidenceRef{
		SourceID: result.SourceID, Benchmark: result.Benchmark, Domain: result.Domain,
		Variant: result.Variant, Metric: result.Metric, SettingsSHA256: result.SettingsSHA256,
		Scaffold: result.Scaffold, Weight: weight,
	}
}

func fuseNumericPriors(priors []numericPrior) Prior {
	totalWeight := 0.0
	for _, prior := range priors {
		totalWeight += prior.weight
	}
	scale := 1.0
	if totalWeight > publicPriorSampleCap {
		scale = publicPriorSampleCap / totalWeight
	}
	weightedMeanLogOdds := 0.0
	weightedLowerLogOdds := 0.0
	effective := 0.0
	evidence := make([]EvidenceRef, 0, len(priors))
	for _, prior := range priors {
		weight := prior.weight * scale
		weightedMeanLogOdds += weight * logOdds(prior.meanBPS)
		weightedLowerLogOdds += weight * logOdds(prior.lowerBPS)
		effective += weight
		ref := prior.evidence
		ref.Weight = weight
		evidence = append(evidence, ref)
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].SourceID != evidence[j].SourceID {
			return evidence[i].SourceID < evidence[j].SourceID
		}
		return evidence[i].Benchmark < evidence[j].Benchmark
	})
	return Prior{
		MeanBPS:          probabilityBPS(weightedMeanLogOdds / effective),
		LowerBPS:         probabilityBPS(weightedLowerLogOdds / effective),
		EffectiveSamples: effective,
		Evidence:         evidence,
	}
}

func logOdds(bps int) float64 {
	probability := float64(min(max(bps, 100), 9_900)) / 10_000
	return math.Log(probability / (1 - probability))
}

func probabilityBPS(logOdds float64) int {
	return int(math.Round(10_000 / (1 + math.Exp(-logOdds))))
}

func estimateRankOnly(catalog Catalog, request PriorRequest) (Prior, bool) {
	type rankKey struct {
		source, benchmark, domain, settings, scaffold string
	}
	candidates := make(map[rankKey]Result)
	baselines := make(map[rankKey]Result)
	for _, result := range catalog.Results {
		if result.Metric != MetricRankOnly || requestedDomainWeight(result.Domain, request) == 0 {
			continue
		}
		key := rankKey{result.SourceID, result.Benchmark, result.Domain, result.SettingsSHA256, result.ScaffoldSHA256}
		switch result.ModelID {
		case request.CandidateID:
			keepConservativeRank(candidates, key, result)
		case request.BaselineID:
			keepConservativeRank(baselines, key, result)
		}
	}
	keys := make([]rankKey, 0, len(candidates))
	for key := range candidates {
		if _, found := baselines[key]; found {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return Prior{}, false
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		return keys[i].benchmark < keys[j].benchmark
	})
	advantage := 0
	evidence := make([]EvidenceRef, 0, len(keys))
	for _, key := range keys {
		candidate := candidates[key]
		advantage += baselines[key].Rank - candidate.Rank
		evidence = append(evidence, evidenceRef(candidate, 0))
	}
	return Prior{RankingOnly: true, RankAdvantage: advantage, Evidence: evidence}, true
}

func keepConservativeRank[K comparable](results map[K]Result, key K, candidate Result) {
	current, found := results[key]
	if !found || candidate.Rank > current.Rank {
		results[key] = candidate
	}
}
