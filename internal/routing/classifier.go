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
	if riskPolicy.StructuredOutputHighRisk && request.Facts.RequiresStructuredOutput {
		return localHighRisk(), true
	}
	text := strings.ToLower(strings.TrimSpace(request.routingText()))
	for _, signal := range riskPolicy.SensitiveTextPatterns {
		if strings.Contains(text, strings.ToLower(signal)) {
			return localHighRisk(), true
		}
	}
	for _, operation := range request.Facts.ActualToolOperations {
		operation = strings.ToLower(operation)
		for _, pattern := range riskPolicy.SensitiveToolPatterns {
			if strings.Contains(operation, strings.ToLower(pattern)) {
				return localHighRisk(), true
			}
		}
	}
	if strongBaseline.HasContextWindow {
		total := saturatingTokenSum(
			request.Facts.EstimatedInputTokens,
			request.Facts.RequestedOutputTokens,
		)
		if total >= thresholdTokens(
			uint64(strongBaseline.ContextWindow),
			uint64(riskPolicy.LongContextThresholdBPS),
		) {
			return localHighRisk(), true
		}
	}
	if request.Facts.EstimatedInputTokens <= 256 && !request.Facts.HasImages {
		for _, prefix := range simpleTextPrefixes {
			if strings.HasPrefix(text, prefix) {
				return Classification{
					TaskType: "simple", Risk: RiskNormal,
					ConfidenceBPS: 9500, Source: ClassificationSourceRule,
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

func localHighRisk() Classification {
	return Classification{
		TaskType: "high_risk", Risk: RiskHigh,
		ConfidenceBPS: 10_000, Source: ClassificationSourceRule,
	}
}
