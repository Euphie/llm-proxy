package routing

import (
	"strings"

	"github.com/Euphie/llm-proxy/internal/profile"
)

var highRiskTextSignals = []string{
	"delete production", "drop table", "deploy to production", "rotate credential",
	"删除生产", "清空数据库", "部署到生产", "修改密钥", "转账", "付款",
	"edit the file", "modify the code", "fix the code", "implement this", "refactor",
	"修改代码", "修复代码", "重构", "开始开发", "写代码",
}

var simpleTextPrefixes = []string{
	"hi", "hello", "hey", "who are you", "what is ", "who is ",
	"你好", "您好", "谢谢", "你是谁", "什么是", "请简单介绍",
}

func ClassifyLocal(
	request Request,
	strongBaseline profile.ModelCapability,
) (Classification, bool) {
	if request.Facts.HasTools || request.Facts.RequiresStructuredOutput {
		return localHighRisk(), true
	}
	text := strings.ToLower(strings.TrimSpace(request.routingText()))
	for _, signal := range highRiskTextSignals {
		if strings.Contains(text, signal) {
			return localHighRisk(), true
		}
	}
	if strongBaseline.HasContextWindow {
		total := request.Facts.EstimatedInputTokens + request.Facts.RequestedOutputTokens
		if int64(total)*4 >= int64(strongBaseline.ContextWindow)*3 {
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

func localHighRisk() Classification {
	return Classification{
		TaskType: "high_risk", Risk: RiskHigh,
		ConfidenceBPS: 10_000, Source: ClassificationSourceRule,
	}
}
