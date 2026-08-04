package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/routing"
)

type selfEscalationStreamGuard struct {
	pending []byte
}

func (g *selfEscalationStreamGuard) Filter(data []byte) ([]byte, error) {
	g.pending = append(g.pending, data...)
	token := []byte(routing.SelfEscalationToolName)
	if bytes.Contains(g.pending, token) {
		g.pending = nil
		return nil, invalidLateSelfEscalation()
	}
	keep := len(token) - 1
	if len(g.pending) <= keep {
		return nil, nil
	}
	emitLength := len(g.pending) - keep
	emit := append([]byte(nil), g.pending[:emitLength]...)
	g.pending = append(g.pending[:0], g.pending[emitLength:]...)
	return emit, nil
}

func (g *selfEscalationStreamGuard) Flush() []byte {
	remaining := append([]byte(nil), g.pending...)
	g.pending = nil
	return remaining
}

type selfEscalationDecision struct {
	Requested  bool
	ReasonCode string
}

var validSelfEscalationReasons = map[string]struct{}{
	"insufficient_reasoning": {},
	"missing_knowledge":      {},
	"complex_tool_plan":      {},
	"instruction_conflict":   {},
	"other":                  {},
}

func detectSelfEscalationResponse(
	operation llmrequest.Operation,
	body []byte,
) (selfEscalationDecision, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return selfEscalationDecision{}, invalidSelfEscalationResponse()
	}
	switch operation {
	case llmrequest.OperationAnthropicMessages:
		return detectAnthropicSelfEscalation(root)
	case llmrequest.OperationOpenAIChatCompletions:
		return detectChatSelfEscalation(root)
	case llmrequest.OperationOpenAIResponses:
		return detectResponsesSelfEscalation(root)
	default:
		return selfEscalationDecision{}, invalidSelfEscalationResponse()
	}
}

func detectSelfEscalationStream(
	operation llmrequest.Operation,
	buffer []byte,
) (selfEscalationDecision, bool, error) {
	normalized := strings.ReplaceAll(string(buffer), "\r\n", "\n")
	started := false
	chatToolNames := make(map[int]string)
	for {
		end := strings.Index(normalized, "\n\n")
		if end < 0 {
			return selfEscalationDecision{}, false, nil
		}
		event := parseSSEEvent(normalized[:end])
		normalized = normalized[end+2:]
		if !event.hasDataLine {
			if !event.hasField {
				continue
			}
			return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
		}
		if strings.TrimSpace(event.data) == "" || strings.TrimSpace(event.data) == "[DONE]" {
			return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal([]byte(event.data), &payload) != nil || payload == nil {
			return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
		}

		var decision selfEscalationDecision
		var ready bool
		var err error
		switch operation {
		case llmrequest.OperationAnthropicMessages:
			decision, ready, started, err = inspectAnthropicEscalationEvent(event, payload, started)
		case llmrequest.OperationOpenAIChatCompletions:
			decision, ready, err = inspectChatEscalationEvent(event, payload, chatToolNames)
			started = true
		case llmrequest.OperationOpenAIResponses:
			decision, ready, started, err = inspectResponsesEscalationEvent(event, payload, started)
		default:
			err = invalidPrecommitProtocolEvent()
		}
		if err != nil || ready || decision.Requested {
			return decision, ready, err
		}
	}
}

func inspectAnthropicEscalationEvent(
	event sseEvent,
	payload map[string]json.RawMessage,
	started bool,
) (selfEscalationDecision, bool, bool, error) {
	typeName := jsonString(payload["type"])
	if event.name == "" || event.name != typeName {
		return selfEscalationDecision{}, false, started, invalidPrecommitProtocolEvent()
	}
	if !started {
		ready, err := validAnthropicCommitEvent(event, payload)
		if err != nil {
			return selfEscalationDecision{}, false, false, err
		}
		return selfEscalationDecision{}, false, ready, nil
	}
	switch typeName {
	case "ping", "message_delta", "content_block_stop":
		return selfEscalationDecision{}, false, true, nil
	case "message_stop":
		return selfEscalationDecision{}, true, true, nil
	case "content_block_start":
		var block map[string]json.RawMessage
		if json.Unmarshal(payload["content_block"], &block) != nil || block == nil {
			return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
		}
		switch jsonString(block["type"]) {
		case "tool_use":
			if jsonString(block["name"]) == routing.SelfEscalationToolName {
				return requestedSelfEscalation(reasonFromObject(block["input"])), false, true, nil
			}
			return selfEscalationDecision{}, true, true, nil
		case "text":
			return selfEscalationDecision{}, true, true, nil
		case "thinking", "redacted_thinking":
			return selfEscalationDecision{}, false, true, nil
		default:
			return selfEscalationDecision{}, true, true, nil
		}
	case "content_block_delta":
		var delta map[string]json.RawMessage
		if json.Unmarshal(payload["delta"], &delta) != nil || delta == nil {
			return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
		}
		if jsonString(delta["type"]) == "text_delta" && jsonString(delta["text"]) != "" {
			return selfEscalationDecision{}, true, true, nil
		}
		return selfEscalationDecision{}, false, true, nil
	default:
		return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
	}
}

func inspectChatEscalationEvent(
	event sseEvent,
	payload map[string]json.RawMessage,
	toolNames map[int]string,
) (selfEscalationDecision, bool, error) {
	if _, err := validChatCommitEvent(event, payload); err != nil {
		return selfEscalationDecision{}, false, err
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(payload["choices"], &choices) != nil {
		return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
	}
	for _, choice := range choices {
		var delta map[string]json.RawMessage
		if raw := choice["delta"]; len(raw) > 0 && json.Unmarshal(raw, &delta) != nil {
			return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
		}
		if delta != nil {
			if text := jsonString(delta["content"]); text != "" {
				return selfEscalationDecision{}, true, nil
			}
			if refusal := jsonString(delta["refusal"]); refusal != "" {
				return selfEscalationDecision{}, true, nil
			}
			var calls []struct {
				Index    int `json:"index"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if raw := delta["tool_calls"]; len(raw) > 0 {
				if json.Unmarshal(raw, &calls) != nil {
					return selfEscalationDecision{}, false, invalidPrecommitProtocolEvent()
				}
				for _, call := range calls {
					toolNames[call.Index] += call.Function.Name
					name := toolNames[call.Index]
					if name == routing.SelfEscalationToolName {
						return requestedSelfEscalation("other"), false, nil
					}
					if name != "" && !strings.HasPrefix(routing.SelfEscalationToolName, name) {
						return selfEscalationDecision{}, true, nil
					}
				}
			}
		}
		if raw := choice["finish_reason"]; len(raw) > 0 && string(raw) != "null" {
			return selfEscalationDecision{}, true, nil
		}
	}
	return selfEscalationDecision{}, false, nil
}

func inspectResponsesEscalationEvent(
	event sseEvent,
	payload map[string]json.RawMessage,
	started bool,
) (selfEscalationDecision, bool, bool, error) {
	typeName := jsonString(payload["type"])
	if event.name == "" || event.name != typeName || payload["error"] != nil {
		return selfEscalationDecision{}, false, started, invalidPrecommitProtocolEvent()
	}
	if !started {
		ready, err := validResponsesCommitEvent(event, payload)
		if err != nil {
			return selfEscalationDecision{}, false, false, err
		}
		return selfEscalationDecision{}, false, ready, nil
	}
	switch typeName {
	case "response.created", "response.queued", "response.in_progress":
		return selfEscalationDecision{}, false, true, nil
	case "response.output_item.added":
		var item map[string]json.RawMessage
		if json.Unmarshal(payload["item"], &item) != nil || item == nil {
			return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
		}
		switch jsonString(item["type"]) {
		case "function_call":
			if jsonString(item["name"]) == routing.SelfEscalationToolName {
				return requestedSelfEscalation("other"), false, true, nil
			}
			return selfEscalationDecision{}, true, true, nil
		case "reasoning":
			return selfEscalationDecision{}, false, true, nil
		default:
			return selfEscalationDecision{}, true, true, nil
		}
	case "response.output_text.delta", "response.refusal.delta":
		return selfEscalationDecision{}, true, true, nil
	case "response.content_part.added":
		var part map[string]json.RawMessage
		if json.Unmarshal(payload["part"], &part) != nil || part == nil {
			return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
		}
		if kind := jsonString(part["type"]); kind == "output_text" || kind == "refusal" {
			return selfEscalationDecision{}, true, true, nil
		}
		return selfEscalationDecision{}, false, true, nil
	case "response.completed":
		return selfEscalationDecision{}, true, true, nil
	case "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.output_item.done", "response.content_part.done":
		return selfEscalationDecision{}, false, true, nil
	default:
		return selfEscalationDecision{}, false, true, invalidPrecommitProtocolEvent()
	}
}

func detectAnthropicSelfEscalation(
	root map[string]json.RawMessage,
) (selfEscalationDecision, error) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(root["content"], &blocks) != nil {
		return selfEscalationDecision{}, invalidSelfEscalationResponse()
	}
	meaningful := false
	for _, block := range blocks {
		typeName := jsonString(block["type"])
		switch typeName {
		case "thinking", "redacted_thinking":
			continue
		case "text":
			if strings.TrimSpace(jsonString(block["text"])) != "" {
				meaningful = true
			}
		case "tool_use":
			if jsonString(block["name"]) == routing.SelfEscalationToolName {
				if meaningful {
					return selfEscalationDecision{}, invalidLateSelfEscalation()
				}
				return requestedSelfEscalation(reasonFromObject(block["input"])), nil
			}
			meaningful = true
		default:
			meaningful = true
		}
	}
	return selfEscalationDecision{}, nil
}

func detectChatSelfEscalation(
	root map[string]json.RawMessage,
) (selfEscalationDecision, error) {
	var choices []struct {
		Message map[string]json.RawMessage `json:"message"`
	}
	if json.Unmarshal(root["choices"], &choices) != nil || len(choices) == 0 {
		return selfEscalationDecision{}, invalidSelfEscalationResponse()
	}
	meaningful := false
	for _, choice := range choices {
		if raw := choice.Message["content"]; len(raw) > 0 && string(raw) != "null" {
			var content string
			if json.Unmarshal(raw, &content) != nil || strings.TrimSpace(content) != "" {
				meaningful = true
			}
		}
		var calls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if raw := choice.Message["tool_calls"]; len(raw) > 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &calls) != nil {
				return selfEscalationDecision{}, invalidSelfEscalationResponse()
			}
		}
		for _, call := range calls {
			if call.Function.Name == routing.SelfEscalationToolName {
				if meaningful {
					return selfEscalationDecision{}, invalidLateSelfEscalation()
				}
				return requestedSelfEscalation(reasonFromArguments(call.Function.Arguments)), nil
			}
			meaningful = true
		}
	}
	return selfEscalationDecision{}, nil
}

func detectResponsesSelfEscalation(
	root map[string]json.RawMessage,
) (selfEscalationDecision, error) {
	var output []map[string]json.RawMessage
	if json.Unmarshal(root["output"], &output) != nil {
		return selfEscalationDecision{}, invalidSelfEscalationResponse()
	}
	meaningful := false
	for _, item := range output {
		switch jsonString(item["type"]) {
		case "reasoning":
			continue
		case "function_call":
			if jsonString(item["name"]) == routing.SelfEscalationToolName {
				if meaningful {
					return selfEscalationDecision{}, invalidLateSelfEscalation()
				}
				return requestedSelfEscalation(reasonFromArguments(jsonString(item["arguments"]))), nil
			}
			meaningful = true
		case "message":
			if responseMessageHasContent(item["content"]) {
				meaningful = true
			}
		default:
			meaningful = true
		}
	}
	return selfEscalationDecision{}, nil
}

func responseMessageHasContent(raw json.RawMessage) bool {
	var content []map[string]json.RawMessage
	if json.Unmarshal(raw, &content) != nil {
		return len(raw) > 0 && string(raw) != "null"
	}
	for _, part := range content {
		if jsonString(part["type"]) == "output_text" &&
			strings.TrimSpace(jsonString(part["text"])) != "" {
			return true
		}
		if jsonString(part["type"]) == "refusal" {
			return true
		}
	}
	return false
}

func reasonFromObject(raw json.RawMessage) string {
	var input struct {
		ReasonCode string `json:"reason_code"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return "other"
	}
	return normalizedSelfEscalationReason(input.ReasonCode)
}

func reasonFromArguments(arguments string) string {
	return reasonFromObject(json.RawMessage(arguments))
}

func normalizedSelfEscalationReason(reason string) string {
	if _, ok := validSelfEscalationReasons[reason]; ok {
		return reason
	}
	return "other"
}

func requestedSelfEscalation(reason string) selfEscalationDecision {
	return selfEscalationDecision{Requested: true, ReasonCode: normalizedSelfEscalationReason(reason)}
}

func invalidSelfEscalationResponse() error {
	return provider.NewFailure(
		provider.FailureRequestProtocolCapability,
		0,
		errors.New("upstream returned an invalid response while self escalation was enabled"),
	)
}

func invalidLateSelfEscalation() error {
	return provider.NewFailure(
		provider.FailureRequestProtocolCapability,
		0,
		errors.New("upstream requested self escalation after answer content"),
	)
}
