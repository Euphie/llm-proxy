package proxy

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
)

const (
	promptLeakMatchRunes        = 64
	promptLeakProbeRunes        = 1024
	promptLeakTokenWindow       = 5
	promptLeakMinTokenWindows   = 6
	promptLeakMinCoveredTokens  = 20
	promptLeakMinControlSignals = 3
)

var errPromptDisclosure = errors.New("upstream response reproduced protected prompt context")

var systemBoundaryTagPattern = regexp.MustCompile(`(?i)</\s*system(?:[-_][a-z0-9_-]*)?\s*>`)

type promptLeakDetector struct {
	operation             llmrequest.Operation
	protected             []string
	protectedTokenWindows []map[string]struct{}
	agentContext          bool
}

var agentControlDisclosurePatterns = [][]string{
	{"available", "user", "invocable", "skills"},
	{"invoke", "skill", "skill", "tool"},
	{"never", "read", "skill", "files"},
	{"sessionstart", "hook"},
	{"extremely_important"},
	{"test", "message", "subscript", "tags"},
	{"skill", "has", "been", "loaded"},
	{"check", "skills", "might", "apply"},
	{"relevant", "skills", "before", "responding"},
}

func newPromptLeakDetector(operation llmrequest.Operation, requestBody []byte) *promptLeakDetector {
	var root map[string]json.RawMessage
	if json.Unmarshal(requestBody, &root) != nil || root == nil {
		return nil
	}
	protected := protectedPromptTexts(operation, root)
	if len(protected) == 0 {
		return nil
	}
	tokenWindows := make([]map[string]struct{}, len(protected))
	agentContext := false
	for index, source := range protected {
		tokenWindows[index] = promptTokenWindows(source)
		agentContext = agentContext || internalAgentContext(source)
	}
	return &promptLeakDetector{
		operation:             operation,
		protected:             protected,
		protectedTokenWindows: tokenWindows,
		agentContext:          agentContext,
	}
}

func (d *promptLeakDetector) responseLeaks(body []byte) bool {
	if d == nil {
		return false
	}
	return d.matches(extractResponseText(d.operation, body))
}

func (d *promptLeakDetector) inspectStream(buffer []byte) (bool, bool) {
	if d == nil {
		return false, true
	}
	text, complete := extractStreamText(d.operation, buffer)
	if d.matches(text) {
		return true, false
	}
	return false, complete || len([]rune(normalizePromptText(text))) >= promptLeakProbeRunes
}

func (d *promptLeakDetector) matches(output string) bool {
	normalized := []rune(normalizePromptText(output))
	if len(normalized) >= promptLeakMatchRunes {
		for start := 0; start+promptLeakMatchRunes <= len(normalized); start += promptLeakMatchRunes / 4 {
			window := string(normalized[start : start+promptLeakMatchRunes])
			for _, source := range d.protected {
				if strings.Contains(source, window) {
					return true
				}
			}
		}
		last := string(normalized[len(normalized)-promptLeakMatchRunes:])
		for _, source := range d.protected {
			if strings.Contains(source, last) {
				return true
			}
		}
	}
	return d.matchesTokenReproduction(output) || d.matchesAgentControlDisclosure(output)
}

func (d *promptLeakDetector) matchesTokenReproduction(output string) bool {
	tokens := promptTokens(output)
	if len(tokens) < promptLeakMinCoveredTokens {
		return false
	}
	for _, protectedWindows := range d.protectedTokenWindows {
		if len(protectedWindows) == 0 {
			continue
		}
		matched := make(map[string]struct{}, promptLeakMinTokenWindows)
		covered := make([]bool, len(tokens))
		for start := 0; start+promptLeakTokenWindow <= len(tokens); start++ {
			window := promptTokenWindow(tokens[start : start+promptLeakTokenWindow])
			if _, ok := protectedWindows[window]; !ok {
				continue
			}
			if _, duplicate := matched[window]; duplicate {
				continue
			}
			matched[window] = struct{}{}
			for index := start; index < start+promptLeakTokenWindow; index++ {
				covered[index] = true
			}
		}
		if len(matched) < promptLeakMinTokenWindows {
			continue
		}
		coveredCount := 0
		for _, isCovered := range covered {
			if isCovered {
				coveredCount++
			}
		}
		if coveredCount >= promptLeakMinCoveredTokens {
			return true
		}
	}
	return false
}

func (d *promptLeakDetector) matchesAgentControlDisclosure(output string) bool {
	if !d.agentContext {
		return false
	}
	tokens := promptTokens(output)
	signalCount := 0
	for _, pattern := range agentControlDisclosurePatterns {
		if containsOrderedPromptTokens(tokens, pattern, 4) {
			signalCount++
			if signalCount >= promptLeakMinControlSignals {
				return true
			}
		}
	}
	if signalCount == 0 {
		return false
	}
	return systemBoundaryTagPattern.MatchString(output) ||
		containsOrderedPromptTokens(tokens, []string{"i", "m", "claude"}, 2) ||
		containsOrderedPromptTokens(tokens, []string{"i", "am", "claude"}, 2)
}

func containsOrderedPromptTokens(tokens, pattern []string, maxDistance int) bool {
	if len(pattern) == 0 || len(tokens) < len(pattern) {
		return false
	}
	for start, token := range tokens {
		if token != pattern[0] {
			continue
		}
		position := start
		matched := true
		for _, wanted := range pattern[1:] {
			found := -1
			limit := min(len(tokens), position+maxDistance+1)
			for index := position + 1; index < limit; index++ {
				if tokens[index] == wanted {
					found = index
					break
				}
			}
			if found < 0 {
				matched = false
				break
			}
			position = found
		}
		if matched {
			return true
		}
	}
	return false
}

func promptTokenWindows(value string) map[string]struct{} {
	tokens := promptTokens(value)
	if len(tokens) < promptLeakTokenWindow {
		return nil
	}
	windows := make(map[string]struct{}, len(tokens)-promptLeakTokenWindow+1)
	for start := 0; start+promptLeakTokenWindow <= len(tokens); start++ {
		windows[promptTokenWindow(tokens[start:start+promptLeakTokenWindow])] = struct{}{}
	}
	return windows
}

func promptTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

func promptTokenWindow(tokens []string) string {
	return strings.Join(tokens, "\x00")
}

func protectedPromptTexts(operation llmrequest.Operation, root map[string]json.RawMessage) []string {
	texts := make([]string, 0, 8)
	appendProtected := func(text string) {
		normalized := normalizePromptText(text)
		if len([]rune(normalized)) >= promptLeakMatchRunes {
			texts = append(texts, normalized)
		}
	}
	if operation == llmrequest.OperationAnthropicMessages {
		for _, text := range rawPromptTexts(root["system"]) {
			appendProtected(text)
		}
	}
	if operation == llmrequest.OperationOpenAIResponses {
		for _, text := range rawPromptTexts(root["instructions"]) {
			appendProtected(text)
		}
	}
	collection := root["messages"]
	if operation == llmrequest.OperationOpenAIResponses {
		collection = root["input"]
	}
	var messages []json.RawMessage
	if json.Unmarshal(collection, &messages) != nil {
		return texts
	}
	for _, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		role := rawJSONString(message["role"])
		for _, text := range rawPromptTexts(message["content"]) {
			if role == "system" || role == "developer" || internalAgentContext(text) {
				appendProtected(text)
			}
		}
	}
	return texts
}

func internalAgentContext(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"sessionstart hook additional context:",
		"<extremely_important>",
		"available user-invocable skills",
		"skills are available for use with the skill tool",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func rawPromptTexts(raw json.RawMessage) []string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []string{text}
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := rawJSONString(block["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func extractResponseText(operation llmrequest.Operation, body []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var texts []string
	switch operation {
	case llmrequest.OperationAnthropicMessages:
		texts = rawPromptTexts(root["content"])
	case llmrequest.OperationOpenAIChatCompletions:
		var choices []map[string]json.RawMessage
		_ = json.Unmarshal(root["choices"], &choices)
		for _, choice := range choices {
			var message map[string]json.RawMessage
			if json.Unmarshal(choice["message"], &message) == nil {
				texts = append(texts, rawPromptTexts(message["content"])...)
			}
		}
	case llmrequest.OperationOpenAIResponses:
		var output []map[string]json.RawMessage
		_ = json.Unmarshal(root["output"], &output)
		for _, item := range output {
			texts = append(texts, rawPromptTexts(item["content"])...)
		}
	}
	return strings.Join(texts, "\n")
}

func extractStreamText(operation llmrequest.Operation, buffer []byte) (string, bool) {
	remaining := buffer
	var text strings.Builder
	complete := false
	for {
		end, ok := completeSSEEventEnd(remaining)
		if !ok {
			break
		}
		event := parseSSEEvent(strings.TrimSuffix(
			strings.ReplaceAll(string(remaining[:end]), "\r\n", "\n"), "\n\n",
		))
		remaining = remaining[end:]
		if strings.TrimSpace(event.data) == "[DONE]" {
			complete = true
			continue
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal([]byte(event.data), &payload) != nil {
			continue
		}
		switch operation {
		case llmrequest.OperationAnthropicMessages:
			typeName := rawJSONString(payload["type"])
			if typeName == "message_stop" {
				complete = true
			}
			if typeName == "content_block_start" {
				var block map[string]json.RawMessage
				if json.Unmarshal(payload["content_block"], &block) == nil && rawJSONString(block["type"]) == "text" {
					text.WriteString(rawJSONString(block["text"]))
				}
			}
			if typeName == "content_block_delta" {
				var delta map[string]json.RawMessage
				if json.Unmarshal(payload["delta"], &delta) == nil && rawJSONString(delta["type"]) == "text_delta" {
					text.WriteString(rawJSONString(delta["text"]))
				}
			}
		case llmrequest.OperationOpenAIChatCompletions:
			var choices []map[string]json.RawMessage
			_ = json.Unmarshal(payload["choices"], &choices)
			for _, choice := range choices {
				if finish := choice["finish_reason"]; len(finish) > 0 && string(finish) != "null" {
					complete = true
				}
				var delta map[string]json.RawMessage
				if json.Unmarshal(choice["delta"], &delta) == nil {
					text.WriteString(rawJSONString(delta["content"]))
				}
			}
		case llmrequest.OperationOpenAIResponses:
			typeName := rawJSONString(payload["type"])
			if typeName == "response.completed" || typeName == "response.failed" || typeName == "response.incomplete" {
				complete = true
			}
			if typeName == "response.output_text.delta" {
				text.WriteString(rawJSONString(payload["delta"]))
			}
		}
	}
	return text.String(), complete
}

func normalizePromptText(value string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(value), unicode.IsSpace), " ")
}

func rawJSONString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
