package strategycompiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Euphie/llm-proxy/internal/profile"
)

const (
	calibrationQualityDeltaBPS   = 300
	calibrationStabilityDeltaBPS = 300
	calibrationErrorDeltaBPS     = 100
)

var automaticRouteSuffix = regexp.MustCompile(`-auto-[0-9a-f]{8}$`)

type CalibrationResult struct {
	Policy             profile.RoutingPolicyConfig
	Changed            bool
	Critical           bool
	Digest             string
	ReliableCandidates int
}

func (compiler *Compiler) Calibrate(
	ctx context.Context,
	record profile.Record,
	active profile.RoutingPolicyConfig,
) (CalibrationResult, error) {
	policy, err := cloneRoutingPolicy(active)
	if err != nil {
		return CalibrationResult{}, err
	}
	result := CalibrationResult{Policy: policy}
	if compiler == nil || compiler.local == nil || policy.Roles == nil ||
		policy.Roles.StrongBaselineModel == "" {
		result.Digest, err = routingPolicyDigest(policy)
		return result, err
	}

	routes := make(map[string]profile.RouteConfig, len(policy.Routes))
	for _, route := range policy.Routes {
		routes[route.ID] = route
	}
	generated := make(map[string]profile.RouteConfig)
	for index := range policy.TaskRoutes {
		mapping := &policy.TaskRoutes[index]
		if mapping.TaskType == "" || mapping.Difficulty == "" ||
			mapping.Difficulty == "unknown" || mapping.Difficulty == "hard" {
			continue
		}
		route, ok := routes[mapping.Route]
		if !ok || len(route.Candidates) == 0 {
			continue
		}
		updated := route
		updated.Candidates = append([]profile.RouteCandidateConfig(nil), route.Candidates...)
		segmentChanged := false
		for candidateIndex := range updated.Candidates {
			candidate := &updated.Candidates[candidateIndex]
			if candidate.Model == policy.Roles.StrongBaselineModel {
				continue
			}
			estimate, found, estimateErr := compiler.local.CandidateEstimate(ctx, EvidenceKey{
				ProfileID: record.ID, Strategy: policy.Name,
				CandidateModel: candidate.Model,
				ReferenceModel: policy.Roles.StrongBaselineModel,
				Domain:         mapping.TaskType, Difficulty: mapping.Difficulty,
			})
			if estimateErr != nil {
				return CalibrationResult{}, estimateErr
			}
			if !found || !estimate.Reliable {
				if candidate.ProductionEligible {
					candidate.ProductionEligible = false
					segmentChanged = true
					result.Critical = true
				}
				continue
			}
			result.ReliableCandidates++
			next := *candidate
			next.QualityScoreBPS = clampBPS(estimate.QualityLowerBPS)
			next.StabilityScoreBPS = clampBPS(estimate.StabilityLowerBPS)
			next.SevereErrorRateBPS = clampBPS(estimate.SevereErrorUpperBPS)
			next.ExpectedLatencyMS = max(estimate.ExpectedLatencyMS, 0)
			next.ProductionEligible = candidateMeetsRouteGates(next, route)
			if !significantCalibration(*candidate, next, route) {
				continue
			}
			if candidate.ProductionEligible && !next.ProductionEligible {
				result.Critical = true
			}
			*candidate = next
			segmentChanged = true
		}
		if !segmentChanged {
			continue
		}
		sortCalibratedCandidates(updated.Candidates, route.Weights, record, policy.Roles.Participants)
		updated.ID, err = calibratedRouteID(route.ID, updated)
		if err != nil {
			return CalibrationResult{}, err
		}
		mapping.Route = updated.ID
		generated[updated.ID] = updated
		result.Changed = true
	}

	if result.Changed {
		for _, route := range generated {
			routes[route.ID] = route
		}
		referenced := map[string]struct{}{policy.DefaultRoute: {}}
		for _, mapping := range policy.TaskRoutes {
			referenced[mapping.Route] = struct{}{}
		}
		policy.Routes = policy.Routes[:0]
		for _, route := range routes {
			if automaticRouteSuffix.MatchString(route.ID) {
				if _, keep := referenced[route.ID]; !keep {
					continue
				}
			}
			policy.Routes = append(policy.Routes, route)
		}
		sort.SliceStable(policy.Routes, func(i, j int) bool {
			return policy.Routes[i].ID < policy.Routes[j].ID
		})
		result.Policy = policy
	}
	result.Digest, err = routingPolicyDigest(result.Policy)
	return result, err
}

func significantCalibration(
	current profile.RouteCandidateConfig,
	next profile.RouteCandidateConfig,
	route profile.RouteConfig,
) bool {
	if current.ProductionEligible != next.ProductionEligible {
		return true
	}
	if candidateMeetsRouteGates(current, route) != candidateMeetsRouteGates(next, route) {
		return true
	}
	if absInt(current.QualityScoreBPS-next.QualityScoreBPS) >= calibrationQualityDeltaBPS ||
		absInt(current.StabilityScoreBPS-next.StabilityScoreBPS) >= calibrationStabilityDeltaBPS ||
		absInt(current.SevereErrorRateBPS-next.SevereErrorRateBPS) >= calibrationErrorDeltaBPS {
		return true
	}
	if current.ExpectedLatencyMS == next.ExpectedLatencyMS {
		return false
	}
	if current.ExpectedLatencyMS == 0 || next.ExpectedLatencyMS == 0 {
		return true
	}
	return absInt64(current.ExpectedLatencyMS-next.ExpectedLatencyMS)*10 >= current.ExpectedLatencyMS
}

func calibratedRouteID(base string, route profile.RouteConfig) (string, error) {
	route.ID = ""
	payload, err := json.Marshal(route)
	if err != nil {
		return "", fmt.Errorf("encode calibrated route: %w", err)
	}
	hash := sha256.Sum256(payload)
	suffix := "-auto-" + hex.EncodeToString(hash[:4])
	base = automaticRouteSuffix.ReplaceAllString(base, "")
	base = strings.TrimRight(base, "-_")
	if limit := 63 - len(suffix); len(base) > limit {
		base = strings.TrimRight(base[:limit], "-_")
	}
	if base == "" {
		base = "route"
	}
	return base + suffix, nil
}

func sortCalibratedCandidates(
	candidates []profile.RouteCandidateConfig,
	weights profile.RoutingWeightsConfig,
	record profile.Record,
	participants []string,
) {
	models := make(map[string]profile.ModelCapabilityConfig, len(record.Config.Models))
	for _, model := range record.Config.Models {
		models[model.ID] = model
	}
	maxPrice := int64(0)
	for _, model := range participants {
		maxPrice = max(maxPrice, representativePrice(models[model]))
	}
	score := func(candidate profile.RouteCandidateConfig) int64 {
		costBPS := 0
		if maxPrice > 0 {
			price := min(representativePrice(models[candidate.Model]), maxPrice)
			costBPS = int((maxPrice - price) * 10_000 / maxPrice)
		}
		performanceBPS := 0
		if candidate.ExpectedLatencyMS > 0 {
			performanceBPS = min(int(10_000_000/max(candidate.ExpectedLatencyMS, 1)), 10_000)
		}
		return int64(candidate.QualityScoreBPS)*int64(weights.QualityBPS) +
			int64(candidate.StabilityScoreBPS)*int64(weights.StabilityBPS) +
			int64(costBPS)*int64(weights.CostBPS) +
			int64(performanceBPS)*int64(weights.PerformanceBPS)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := score(candidates[i]), score(candidates[j])
		if left != right {
			return left > right
		}
		return candidates[i].Model < candidates[j].Model
	})
}

func cloneRoutingPolicy(policy profile.RoutingPolicyConfig) (profile.RoutingPolicyConfig, error) {
	payload, err := json.Marshal(policy)
	if err != nil {
		return profile.RoutingPolicyConfig{}, fmt.Errorf("encode routing Policy: %w", err)
	}
	var cloned profile.RoutingPolicyConfig
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return profile.RoutingPolicyConfig{}, fmt.Errorf("clone routing Policy: %w", err)
	}
	return cloned, nil
}

func routingPolicyDigest(policy profile.RoutingPolicyConfig) (string, error) {
	payload, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode routing Policy digest: %w", err)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
