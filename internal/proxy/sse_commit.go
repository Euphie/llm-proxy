package proxy

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/provider"
)

var errInvalidPrecommitProtocolEvent = errors.New("stream produced an invalid protocol event before client commit")

type sseEvent struct {
	name        string
	data        string
	hasDataLine bool
	hasField    bool
}

func hasCompleteSSEEvent(buffer []byte, operation llmrequest.Operation) (bool, error) {
	normalized := strings.ReplaceAll(string(buffer), "\r\n", "\n")
	for {
		end := strings.Index(normalized, "\n\n")
		if end < 0 {
			return false, nil
		}
		event := parseSSEEvent(normalized[:end])
		normalized = normalized[end+2:]
		if !event.hasDataLine {
			if !event.hasField {
				continue
			}
			return false, invalidPrecommitProtocolEvent()
		}
		if strings.TrimSpace(event.data) == "" || strings.TrimSpace(event.data) == "[DONE]" {
			return false, invalidPrecommitProtocolEvent()
		}
		ready, err := validCommitEvent(operation, event)
		if err != nil || ready {
			return ready, err
		}
	}
}

func parseSSEEvent(raw string) sseEvent {
	var event sseEvent
	dataLines := make([]string, 0, 1)
	for line := range strings.SplitSeq(raw, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		event.hasField = true
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event.name = value
		case "data":
			event.hasDataLine = true
			dataLines = append(dataLines, value)
		}
	}
	event.data = strings.Join(dataLines, "\n")
	return event
}

func validCommitEvent(operation llmrequest.Operation, event sseEvent) (bool, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(event.data), &payload) != nil || payload == nil {
		return false, invalidPrecommitProtocolEvent()
	}
	switch operation {
	case llmrequest.OperationAnthropicMessages:
		return validAnthropicCommitEvent(event, payload)
	case llmrequest.OperationOpenAIChatCompletions:
		return validChatCommitEvent(event, payload)
	case llmrequest.OperationOpenAIResponses:
		return validResponsesCommitEvent(event, payload)
	default:
		return false, invalidPrecommitProtocolEvent()
	}
}

func validAnthropicCommitEvent(event sseEvent, payload map[string]json.RawMessage) (bool, error) {
	typeName := jsonString(payload["type"])
	if event.name == "" || typeName == "" || event.name != typeName {
		return false, invalidPrecommitProtocolEvent()
	}
	switch typeName {
	case "message_start":
		if !jsonObject(payload["message"]) {
			return false, invalidPrecommitProtocolEvent()
		}
		return true, nil
	case "ping":
		return false, nil
	default:
		return false, invalidPrecommitProtocolEvent()
	}
}

func validChatCommitEvent(event sseEvent, payload map[string]json.RawMessage) (bool, error) {
	if event.name != "" && event.name != "message" {
		return false, invalidPrecommitProtocolEvent()
	}
	if jsonString(payload["object"]) != "chat.completion.chunk" || payload["error"] != nil {
		return false, invalidPrecommitProtocolEvent()
	}
	var choices []json.RawMessage
	if raw, ok := payload["choices"]; !ok || json.Unmarshal(raw, &choices) != nil || len(choices) == 0 {
		return false, invalidPrecommitProtocolEvent()
	}
	return true, nil
}

func validResponsesCommitEvent(event sseEvent, payload map[string]json.RawMessage) (bool, error) {
	typeName := jsonString(payload["type"])
	if event.name == "" || typeName == "" || event.name != typeName || payload["error"] != nil ||
		(typeName != "response.created" && typeName != "response.queued") || !jsonObject(payload["response"]) {
		return false, invalidPrecommitProtocolEvent()
	}
	return true, nil
}

func invalidPrecommitProtocolEvent() error {
	return provider.NewFailure(
		provider.FailureRequestProtocolCapability,
		0,
		errInvalidPrecommitProtocolEvent,
	)
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func jsonObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return len(raw) != 0 && json.Unmarshal(raw, &value) == nil && value != nil
}
