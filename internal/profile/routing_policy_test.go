package profile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
)

func TestResolvePolicyRuntimeRejectsUnavailableReferencedModel(t *testing.T) {
	record := Record{ID: 3, Slug: "policy", DisplayName: "Policy", Enabled: true}
	record.Config = NewConfig(ProtocolAnthropic, "https://example.com")
	record.Config.AutoRouting.Enabled = true
	policy := routingPolicyTestConfig()
	models := []modeldirectory.Record{
		policyModelRecord(t, "fast", modeldirectory.StatusAvailable),
		policyModelRecord(t, "strong", modeldirectory.StatusOffline),
	}

	_, err := ResolvePolicyRuntime(record, models, &policy)
	if err == nil || !strings.Contains(err.Error(), `model "strong" is not available`) {
		t.Fatalf("error=%v", err)
	}
}

func TestResolvePolicyRuntimeBuildsOnlyFromDirectoryAndPolicy(t *testing.T) {
	record := Record{ID: 3, Slug: "policy", DisplayName: "Policy", Enabled: true}
	record.Config = NewConfig(ProtocolAnthropic, "https://example.com")
	record.Config.AutoRouting.Enabled = true
	policy := routingPolicyTestConfig()
	models := []modeldirectory.Record{
		policyModelRecord(t, "fast", modeldirectory.StatusAvailable),
		policyModelRecord(t, "strong", modeldirectory.StatusAvailable),
	}

	runtime, err := ResolvePolicyRuntime(record, models, &policy)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AutoRouting.Strategy.Name != policy.Name ||
		runtime.AutoRouting.StrongBaselineModel != "strong" ||
		runtime.AutoRouting.TaskAnalyzerModel != "fast" ||
		runtime.ModelStatuses["fast"] != modeldirectory.StatusAvailable {
		t.Fatalf("runtime=%+v", runtime.AutoRouting)
	}
	if len(runtime.Models) != 2 || len(record.Config.Models) != 0 {
		t.Fatalf("runtime models=%d Profile models=%d", len(runtime.Models), len(record.Config.Models))
	}
}

func TestRoutingPolicyOmitsRemovedRiskPolicy(t *testing.T) {
	payload, err := json.Marshal(routingPolicyTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["risk_policy"]; exists {
		t.Fatalf("removed risk Policy survived JSON: %s", payload)
	}
}

func policyModelRecord(t *testing.T, id string, status modeldirectory.Status) modeldirectory.Record {
	t.Helper()
	contextWindow := 200_000
	maxOutput := 32_000
	vision := false
	tools := true
	structured := true
	inputPrice := int64(1_000_000)
	outputPrice := int64(5_000_000)
	capability, err := json.Marshal(ModelCapabilityConfig{
		ID: id, ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
		SupportsVision: &vision, SupportsTools: &tools, SupportsStructuredOutput: &structured,
		InputPriceMicroUSDPerMillion: &inputPrice, OutputPriceMicroUSDPerMillion: &outputPrice,
	})
	if err != nil {
		t.Fatal(err)
	}
	return modeldirectory.Record{ProfileID: 3, ModelID: id, CapabilityJSON: capability, Status: status}
}

func routingPolicyTestConfig() RoutingPolicyConfig {
	return RoutingPolicyConfig{
		RoutingStrategyConfig: RoutingStrategyConfig{
			Name: "20260813-001", DefaultRoute: "general",
			Roles: &RoutingStrategyRolesConfig{
				Participants: []string{"fast", "strong"}, StrongBaselineModel: "strong",
				TaskAnalyzerModel: "fast", ReviewerModel: "strong",
			},
			Routes: []RouteConfig{{
				ID: "general", MinQualityBPS: 8_000, MinStabilityBPS: 8_000,
				MaxSevereErrorRateBPS: 500,
				Weights:               RoutingWeightsConfig{QualityBPS: 4_000, StabilityBPS: 2_500, CostBPS: 2_500, PerformanceBPS: 1_000},
				Candidates: []RouteCandidateConfig{
					{Model: "fast", QualityScoreBPS: 8_000, StabilityScoreBPS: 8_000, SevereErrorRateBPS: 500},
					{Model: "strong", QualityScoreBPS: 10_000, StabilityScoreBPS: 10_000},
				},
			}},
			Budget: AttemptBudgetConfig{
				MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
				MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
				MaxWorstCaseCostMicroUSD: 10_000_000,
			},
		},
		AnalyzerTimeout: "15s", AnalyzerMinConfidenceBPS: 7_000, SessionTTL: "24h",
	}
}
