package routing

import (
	"strings"

	"github.com/Euphie/llm-proxy/internal/profile"
)

var simpleTextPrefixes = []string{
	"hi", "hello", "hey", "who are you", "what is ", "who is ",
	"你好", "您好", "谢谢", "你是谁", "什么是", "请简单介绍",
}

func ClassifyLocal(
	request Request,
	strongBaseline profile.ModelCapability,
	riskPolicy profile.RiskPolicyRuntime,
) (Classification, bool) {
	if classification, matched := ClassifyHardRisk(request, strongBaseline, riskPolicy); matched {
		return classification, true
	}
	return classifySimple(request)
}

func ClassifyHardRisk(
	request Request,
	strongBaseline profile.ModelCapability,
	riskPolicy profile.RiskPolicyRuntime,
) (Classification, bool) {
	_ = strongBaseline
	text := strings.ToLower(strings.TrimSpace(request.LatestUserText()))
	for _, signal := range riskPolicy.SensitiveTextPatterns {
		if strings.Contains(text, strings.ToLower(signal)) {
			return localHighRisk("sensitive_latest_user_text"), true
		}
	}
	operation := strings.ToLower(request.Facts.ForcedToolOperation)
	for _, pattern := range riskPolicy.SensitiveToolPatterns {
		if operation != "" && strings.Contains(operation, strings.ToLower(pattern)) {
			return localHighRisk("forced_sensitive_tool"), true
		}
	}
	return Classification{}, false
}

func classifySimple(request Request) (Classification, bool) {
	text := strings.ToLower(strings.TrimSpace(request.LatestUserText()))
	if request.Facts.EstimatedInputTokens <= 256 && !request.Facts.HasImages {
		for _, prefix := range simpleTextPrefixes {
			if strings.HasPrefix(text, prefix) {
				return Classification{
					TaskType: "simple", Difficulty: DifficultyEasy, Risk: RiskNormal,
					ConfidenceBPS: 9500, Source: ClassificationSourceRule,
					ReasonCodes: []string{"short_simple_prefix"},
				}, true
			}
		}
	}
	return Classification{}, false
}

func saturatingTokenSum(left, right int) uint64 {
	if left < 0 || right < 0 {
		return ^uint64(0)
	}
	a, b := uint64(left), uint64(right)
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func thresholdTokens(contextWindow, thresholdBPS uint64) uint64 {
	quotient, remainder := contextWindow/10_000, contextWindow%10_000
	return quotient*thresholdBPS + (remainder*thresholdBPS+9_999)/10_000
}

func localHighRisk(reason string) Classification {
	return Classification{
		TaskType: "unknown", Difficulty: DifficultyUnknown, Risk: RiskHigh,
		ConfidenceBPS: 10_000, Source: ClassificationSourceRule,
		ReasonCodes: []string{reason},
	}
}
