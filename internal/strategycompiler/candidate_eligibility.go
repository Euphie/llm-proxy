package strategycompiler

import "github.com/Euphie/llm-proxy/internal/profile"

func candidateMeetsRouteGates(
	candidate profile.RouteCandidateConfig,
	route profile.RouteConfig,
) bool {
	return candidate.QualityScoreBPS >= route.MinQualityBPS &&
		candidate.StabilityScoreBPS >= route.MinStabilityBPS &&
		candidate.SevereErrorRateBPS <= route.MaxSevereErrorRateBPS
}
