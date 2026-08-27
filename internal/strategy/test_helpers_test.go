package strategy

import "github.com/Euphie/llm-proxy/internal/profile"

func strategyTestConfig() profile.Config {
	no := false
	yes := true
	contextWindow := 200_000
	maxOutput := 16_000
	fastInput := int64(100_000)
	fastOutput := int64(400_000)
	strongInput := int64(3_000_000)
	strongOutput := int64(15_000_000)
	config := profile.NewConfig(profile.ProtocolAnthropic, "https://example.test")
	config.Models = []profile.ModelCapabilityConfig{
		{
			ID: "fast", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &no, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &fastInput, OutputPriceMicroUSDPerMillion: &fastOutput,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &yes, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &strongInput, OutputPriceMicroUSDPerMillion: &strongOutput,
		},
	}
	config.AutoRouting = profile.AutoRoutingConfig{
		Enabled: true, Participants: []string{"fast", "strong"},
		StrongBaselineModel: "strong", TaskAnalyzerModel: "fast",
		AnalyzerTimeout: "5s", AnalyzerMinConfidenceBPS: 7000,
		Strategy: profile.RoutingStrategyConfig{
			Name: "20260802-001", Alias: "当前", DefaultRoute: "balanced",
			TaskRoutes: []profile.TaskRouteConfig{{TaskType: "simple", Route: "balanced"}},
			Routes: []profile.RouteConfig{{
				ID: "balanced", MinQualityBPS: 9000, MaxSevereErrorRateBPS: 100,
				Candidates: []profile.RouteCandidateConfig{
					{Model: "fast", QualityScoreBPS: 9200, SevereErrorRateBPS: 50},
					{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
				},
			}},
			Budget: profile.AttemptBudgetConfig{
				MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
				MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
				MaxWorstCaseCostMicroUSD: 500_000,
			},
		},
	}
	return config
}
