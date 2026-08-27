package strategycompiler

import (
	"context"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type keyedCalibrationEvidence struct {
	estimates map[string]LocalEstimate
}

func (e keyedCalibrationEvidence) CandidateEstimate(
	_ context.Context,
	key EvidenceKey,
) (LocalEstimate, bool, error) {
	estimate, ok := e.estimates[key.Domain+"/"+key.Difficulty+"/"+key.CandidateModel]
	return estimate, ok, nil
}

func TestCalibrateSplitsSharedRouteWithoutChangingOtherTaskSegments(t *testing.T) {
	active := calibrationPolicy()
	compiler := &Compiler{local: keyedCalibrationEvidence{estimates: map[string]LocalEstimate{
		"simple/easy/fast": {
			QualityLowerBPS: 8_500, StabilityLowerBPS: 8_600,
			SevereErrorUpperBPS: 200, ExpectedLatencyMS: 100,
			EffectiveSamples: 30, Reliable: true,
		},
	}}}

	result, err := compiler.Calibrate(context.Background(), calibrationRecord(), active)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Critical || result.ReliableCandidates != 1 {
		t.Fatalf("result=%+v", result)
	}
	simpleRoute := taskRouteID(t, result.Policy, "simple", "easy")
	if !strings.HasPrefix(simpleRoute, "general-auto-") {
		t.Fatalf("simple route=%q", simpleRoute)
	}
	if got := taskRouteID(t, result.Policy, "coding", "easy"); got != "general" {
		t.Fatalf("coding route=%q want general", got)
	}
	updated := routeCandidate(t, result.Policy, simpleRoute, "fast")
	if updated.QualityScoreBPS != 8_500 || updated.StabilityScoreBPS != 8_600 ||
		updated.SevereErrorRateBPS != 200 || updated.ExpectedLatencyMS != 100 ||
		!updated.ProductionEligible {
		t.Fatalf("updated candidate=%+v", updated)
	}
	original := routeCandidate(t, result.Policy, "general", "fast")
	if original.QualityScoreBPS != 8_000 || original.StabilityScoreBPS != 8_000 ||
		original.SevereErrorRateBPS != 500 || original.ExpectedLatencyMS != 100 {
		t.Fatalf("original candidate was mutated: %+v", original)
	}
	if result.Policy.Roles.StrongBaselineModel != "strong" ||
		result.Policy.Budget.MaxTotalOutboundCalls != 5 {
		t.Fatalf("control-plane fields changed: roles=%+v budget=%+v", result.Policy.Roles, result.Policy.Budget)
	}
	clone := routeConfigByID(t, result.Policy, simpleRoute)
	if clone.MinQualityBPS != 8_000 || clone.MinStabilityBPS != 8_000 ||
		clone.MaxSevereErrorRateBPS != 500 || clone.Weights != routeConfigByID(t, active, "general").Weights {
		t.Fatalf("route controls changed: %+v", clone)
	}
}

func TestCalibrateMarksReliableEligibilityRegressionCritical(t *testing.T) {
	active := calibrationPolicy()
	active.Routes[0].Candidates[0].ProductionEligible = true
	compiler := &Compiler{local: keyedCalibrationEvidence{estimates: map[string]LocalEstimate{
		"simple/easy/fast": {
			QualityLowerBPS: 7_000, StabilityLowerBPS: 8_400,
			SevereErrorUpperBPS: 300, ExpectedLatencyMS: 100,
			EffectiveSamples: 30, Reliable: true,
		},
	}}}

	result, err := compiler.Calibrate(context.Background(), calibrationRecord(), active)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.Critical {
		t.Fatalf("result=%+v", result)
	}
}

func TestCalibrateIgnoresSmallReliableScoreDrift(t *testing.T) {
	active := calibrationPolicy()
	active.Routes[0].Candidates[0].ProductionEligible = true
	compiler := &Compiler{local: keyedCalibrationEvidence{estimates: map[string]LocalEstimate{
		"simple/easy/fast": {
			QualityLowerBPS: 8_100, StabilityLowerBPS: 8_000,
			SevereErrorUpperBPS: 500, ExpectedLatencyMS: 100,
			EffectiveSamples: 30, Reliable: true,
		},
		"coding/easy/fast": {
			QualityLowerBPS: 8_100, StabilityLowerBPS: 8_000,
			SevereErrorUpperBPS: 500, ExpectedLatencyMS: 100,
			EffectiveSamples: 30, Reliable: true,
		},
	}}}

	result, err := compiler.Calibrate(context.Background(), calibrationRecord(), active)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Critical || result.ReliableCandidates != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestCalibrateImmediatelyDemotesCandidateWhenReliableEvidenceDisappears(t *testing.T) {
	active := calibrationPolicy()
	active.Routes[0].Candidates[0].ProductionEligible = true
	compiler := &Compiler{local: keyedCalibrationEvidence{estimates: map[string]LocalEstimate{}}}

	result, err := compiler.Calibrate(context.Background(), calibrationRecord(), active)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.Critical || result.ReliableCandidates != 0 {
		t.Fatalf("result=%+v", result)
	}
	routeID := taskRouteID(t, result.Policy, "simple", "easy")
	if candidate := routeCandidate(t, result.Policy, routeID, "fast"); candidate.ProductionEligible {
		t.Fatalf("candidate remained production eligible: %+v", candidate)
	}
}

func calibrationRecord() profile.Record {
	toolSupport := true
	fastInput, fastOutput := int64(100_000), int64(200_000)
	strongInput, strongOutput := int64(500_000), int64(1_000_000)
	return profile.Record{ID: 9, Config: profile.Config{Models: []profile.ModelCapabilityConfig{
		{ID: "fast", SupportsTools: &toolSupport, InputPriceMicroUSDPerMillion: &fastInput, OutputPriceMicroUSDPerMillion: &fastOutput},
		{ID: "strong", SupportsTools: &toolSupport, InputPriceMicroUSDPerMillion: &strongInput, OutputPriceMicroUSDPerMillion: &strongOutput},
	}}}
}

func calibrationPolicy() profile.RoutingPolicyConfig {
	return profile.RoutingPolicyConfig{RoutingStrategyConfig: profile.RoutingStrategyConfig{
		Name: "20260817-001", DefaultRoute: "general",
		Roles: &profile.RoutingStrategyRolesConfig{
			Participants: []string{"fast", "strong"}, StrongBaselineModel: "strong",
			TaskAnalyzerModel: "fast", ReviewerModel: "strong",
		},
		TaskRoutes: []profile.TaskRouteConfig{
			{TaskType: "simple", Difficulty: "easy", Route: "general"},
			{TaskType: "coding", Difficulty: "easy", Route: "general"},
			{TaskType: "simple", Difficulty: "hard", Route: "strong"},
		},
		Routes: []profile.RouteConfig{
			{
				ID: "general", MinQualityBPS: 8_000, MinStabilityBPS: 8_000,
				MaxSevereErrorRateBPS: 500,
				Weights:               profile.RoutingWeightsConfig{QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000},
				Candidates: []profile.RouteCandidateConfig{{
					Model: "fast", QualityScoreBPS: 8_000, StabilityScoreBPS: 8_000,
					SevereErrorRateBPS: 500, ExpectedLatencyMS: 100,
				}},
			},
			{
				ID: "strong", MinQualityBPS: 8_000, MinStabilityBPS: 8_000,
				MaxSevereErrorRateBPS: 500,
				Weights:               profile.RoutingWeightsConfig{QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000},
				Candidates:            []profile.RouteCandidateConfig{{Model: "strong", QualityScoreBPS: 8_000, StabilityScoreBPS: 8_000, SevereErrorRateBPS: 500}},
			},
		},
		Budget: profile.AttemptBudgetConfig{MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5, MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m", MaxWorstCaseCostMicroUSD: 500_000},
	}}
}

func taskRouteID(t *testing.T, policy profile.RoutingPolicyConfig, taskType, difficulty string) string {
	t.Helper()
	for _, mapping := range policy.TaskRoutes {
		if mapping.TaskType == taskType && mapping.Difficulty == difficulty {
			return mapping.Route
		}
	}
	t.Fatalf("missing task route %s/%s", taskType, difficulty)
	return ""
}

func routeConfigByID(t *testing.T, policy profile.RoutingPolicyConfig, routeID string) profile.RouteConfig {
	t.Helper()
	for _, route := range policy.Routes {
		if route.ID == routeID {
			return route
		}
	}
	t.Fatalf("missing route %q", routeID)
	return profile.RouteConfig{}
}

func routeCandidate(t *testing.T, policy profile.RoutingPolicyConfig, routeID, model string) profile.RouteCandidateConfig {
	t.Helper()
	for _, candidate := range routeConfigByID(t, policy, routeID).Candidates {
		if candidate.Model == model {
			return candidate
		}
	}
	t.Fatalf("missing candidate %q in route %q", model, routeID)
	return profile.RouteCandidateConfig{}
}
