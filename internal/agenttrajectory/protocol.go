package agenttrajectory

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Euphie/llm-proxy/internal/routing"
)

const (
	maxProtocolBodyBytes = 8 << 20
	maxProtocolEvents    = 512
)

func ParseTurn(operation routing.Operation, requestBody, responseBody []byte) (Turn, error) {
	if !supportedOperation(operation) || len(requestBody) > maxProtocolBodyBytes || len(responseBody) > maxProtocolBodyBytes {
		return Turn{}, ErrInvalidProtocol
	}
	history, err := parseRequest(operation, requestBody)
	if err != nil {
		return Turn{}, err
	}
	response, completion, err := parseResponse(operation, responseBody)
	if err != nil {
		return Turn{}, err
	}
	if len(history)+len(response) > maxProtocolEvents {
		return Turn{}, ErrInvalidProtocol
	}
	turn := Turn{History: history, Response: response, Completion: completion}
	for _, event := range append(append([]Event{}, history...), response...) {
		if event.Kind == EventToolCall {
			turn.ToolCalls++
		}
	}
	if completion == CompletionComplete {
		turn.FinalText = joinedText(response, EventAssistantText)
	}
	return turn, nil
}

func supportedOperation(operation routing.Operation) bool {
	switch operation {
	case routing.OperationAnthropicMessages,
		routing.OperationOpenAIChatCompletions,
		routing.OperationOpenAIResponses:
		return true
	default:
		return false
	}
}

func parseRequest(operation routing.Operation, body []byte) ([]Event, error) {
	var root map[string]json.RawMessage
	if err := decodeJSONObject(body, &root); err != nil {
		return nil, err
	}
	switch operation {
	case routing.OperationAnthropicMessages:
		return parseAnthropicHistory(root["messages"])
	case routing.OperationOpenAIChatCompletions:
		return parseChatHistory(root["messages"])
	case routing.OperationOpenAIResponses:
		return parseResponsesHistory(root["input"])
	default:
		return nil, ErrInvalidProtocol
	}
}

func parseResponse(operation routing.Operation, body []byte) ([]Event, CompletionState, error) {
	payloads, err := protocolPayloads(body)
	if err != nil {
		return nil, "", err
	}
	switch operation {
	case routing.OperationAnthropicMessages:
		return parseAnthropicResponse(payloads)
	case routing.OperationOpenAIChatCompletions:
		return parseChatResponse(payloads)
	case routing.OperationOpenAIResponses:
		return parseResponsesResponse(payloads)
	default:
		return nil, "", ErrInvalidProtocol
	}
}

func parseAnthropicHistory(raw json.RawMessage) ([]Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil {
		return nil, ErrInvalidProtocol
	}
	events := make([]Event, 0)
	for _, message := range messages {
		parsed, err := parseAnthropicContent(message.Role, message.Content)
		if err != nil {
			return nil, err
		}
		events = append(events, parsed...)
	}
	return events, nil
}

func parseAnthropicContent(role string, raw json.RawMessage) ([]Event, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return textEvent(role, text), nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, ErrInvalidProtocol
	}
	events := make([]Event, 0, len(blocks))
	for _, block := range blocks {
		switch rawString(block["type"]) {
		case "text":
			events = append(events, textEvent(role, rawString(block["text"]))...)
		case "tool_use":
			events = append(events, Event{
				Role: "assistant", Kind: EventToolCall,
				CallID: rawString(block["id"]), ToolName: rawString(block["name"]),
				Arguments: normalizedJSON(block["input"]),
			})
		case "tool_result":
			result := block["content"]
			if len(result) == 0 {
				result = json.RawMessage("null")
			}
			events = append(events, Event{
				Role: "tool", Kind: EventToolResult,
				CallID: rawString(block["tool_use_id"]), Result: normalizedJSON(result),
			})
		}
	}
	return events, nil
}

func parseChatHistory(raw json.RawMessage) ([]Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil, ErrInvalidProtocol
	}
	events := make([]Event, 0)
	for _, message := range messages {
		role := rawString(message["role"])
		if role == "tool" {
			events = append(events, Event{
				Role: "tool", Kind: EventToolResult,
				CallID: rawString(message["tool_call_id"]), Result: normalizedJSON(message["content"]),
			})
			continue
		}
		if content := contentText(message["content"]); content != "" {
			events = append(events, textEvent(role, content)...)
		}
		calls, err := parseChatToolCalls(message["tool_calls"])
		if err != nil {
			return nil, err
		}
		events = append(events, calls...)
	}
	return events, nil
}

func parseChatToolCalls(raw json.RawMessage) ([]Event, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &calls) != nil {
		return nil, ErrInvalidProtocol
	}
	events := make([]Event, 0, len(calls))
	for _, call := range calls {
		events = append(events, Event{
			Role: "assistant", Kind: EventToolCall, CallID: call.ID, ToolName: call.Function.Name,
			Arguments: jsonStringContent(call.Function.Arguments),
		})
	}
	return events, nil
}

func parseResponsesHistory(raw json.RawMessage) ([]Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var prompt string
	if json.Unmarshal(raw, &prompt) == nil {
		return textEvent("user", prompt), nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, ErrInvalidProtocol
	}
	return parseResponsesItems(items)
}

func parseResponsesItems(items []map[string]json.RawMessage) ([]Event, error) {
	events := make([]Event, 0, len(items))
	for _, item := range items {
		switch rawString(item["type"]) {
		case "function_call":
			events = append(events, Event{
				Role: "assistant", Kind: EventToolCall,
				CallID: rawString(item["call_id"]), ToolName: rawString(item["name"]),
				Arguments: jsonStringContent(rawString(item["arguments"])),
			})
		case "function_call_output":
			events = append(events, Event{
				Role: "tool", Kind: EventToolResult,
				CallID: rawString(item["call_id"]), Result: normalizedJSON(item["output"]),
			})
		case "message", "":
			role := rawString(item["role"])
			if role == "" {
				role = "user"
			}
			if text := contentText(item["content"]); text != "" {
				events = append(events, textEvent(role, text)...)
			}
		}
	}
	return events, nil
}

func parseAnthropicResponse(payloads [][]byte) ([]Event, CompletionState, error) {
	events := make([]Event, 0)
	stopReason := ""
	failed := false
	toolPositions := map[int]int{}
	toolArguments := map[int][]byte{}
	for _, payload := range payloads {
		var root map[string]json.RawMessage
		if decodeJSONObject(payload, &root) != nil {
			return nil, "", ErrInvalidProtocol
		}
		if rawString(root["type"]) == "error" || len(root["error"]) > 0 {
			failed = true
		}
		if len(root["content"]) > 0 {
			parsed, err := parseAnthropicContent("assistant", root["content"])
			if err != nil {
				return nil, "", err
			}
			events = append(events, parsed...)
		}
		if reason := rawString(root["stop_reason"]); reason != "" {
			stopReason = reason
		}
		if rawString(root["type"]) == "content_block_start" {
			var block map[string]json.RawMessage
			if json.Unmarshal(root["content_block"], &block) == nil && rawString(block["type"]) == "tool_use" {
				index := rawInt(root["index"])
				events = append(events, Event{Role: "assistant", Kind: EventToolCall,
					CallID: rawString(block["id"]), ToolName: rawString(block["name"]), Arguments: normalizedJSON(block["input"])})
				toolPositions[index] = len(events) - 1
			}
		}
		if rawString(root["type"]) == "content_block_delta" {
			var delta map[string]json.RawMessage
			if json.Unmarshal(root["delta"], &delta) == nil {
				switch rawString(delta["type"]) {
				case "text_delta":
					appendTextEvent(&events, rawString(delta["text"]))
				case "input_json_delta":
					index := rawInt(root["index"])
					toolArguments[index] = append(toolArguments[index], rawString(delta["partial_json"])...)
				}
			}
		}
		if rawString(root["type"]) == "message_delta" {
			var delta map[string]json.RawMessage
			if json.Unmarshal(root["delta"], &delta) == nil {
				stopReason = rawString(delta["stop_reason"])
			}
		}
	}
	for index, arguments := range toolArguments {
		if position, ok := toolPositions[index]; ok {
			events[position].Arguments = jsonStringContent(string(arguments))
		}
	}
	if failed {
		return events, CompletionFailed, nil
	}
	if hasEventKind(events, EventToolCall) || stopReason == "tool_use" {
		return events, CompletionWaitingForTool, nil
	}
	if stopReason == "end_turn" {
		return events, CompletionComplete, nil
	}
	return events, CompletionFailed, nil
}

func parseChatResponse(payloads [][]byte) ([]Event, CompletionState, error) {
	events := make([]Event, 0)
	finishReason := ""
	failed := false
	streamCalls := map[int]*Event{}
	for _, payload := range payloads {
		var root struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				FinishReason string                     `json:"finish_reason"`
				Message      map[string]json.RawMessage `json:"message"`
				Delta        struct {
					Content   json.RawMessage `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(payload, &root) != nil {
			return nil, "", ErrInvalidProtocol
		}
		if len(root.Error) > 0 && !bytes.Equal(bytes.TrimSpace(root.Error), []byte("null")) {
			failed = true
		}
		for _, choice := range root.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if len(choice.Message) > 0 {
				if text := contentText(choice.Message["content"]); text != "" {
					events = append(events, textEvent("assistant", text)...)
				}
				calls, err := parseChatToolCalls(choice.Message["tool_calls"])
				if err != nil {
					return nil, "", err
				}
				events = append(events, calls...)
			}
			if text := contentText(choice.Delta.Content); text != "" {
				appendTextEvent(&events, text)
			}
			for _, call := range choice.Delta.ToolCalls {
				event := streamCalls[call.Index]
				if event == nil {
					event = &Event{Role: "assistant", Kind: EventToolCall}
					streamCalls[call.Index] = event
				}
				event.CallID += call.ID
				event.ToolName += call.Function.Name
				event.Arguments = append(event.Arguments, call.Function.Arguments...)
			}
		}
	}
	for index := 0; index < len(streamCalls); index++ {
		if event := streamCalls[index]; event != nil {
			event.Arguments = jsonStringContent(string(event.Arguments))
			events = append(events, *event)
		}
	}
	if failed {
		return events, CompletionFailed, nil
	}
	if hasEventKind(events, EventToolCall) || finishReason == "tool_calls" || finishReason == "function_call" {
		return events, CompletionWaitingForTool, nil
	}
	if finishReason == "stop" {
		return events, CompletionComplete, nil
	}
	return events, CompletionFailed, nil
}

func parseResponsesResponse(payloads [][]byte) ([]Event, CompletionState, error) {
	events := make([]Event, 0)
	status := ""
	failed := false
	for _, payload := range payloads {
		var root map[string]json.RawMessage
		if decodeJSONObject(payload, &root) != nil {
			return nil, "", ErrInvalidProtocol
		}
		typeName := rawString(root["type"])
		if typeName == "error" || nonNullJSON(root["error"]) || rawString(root["status"]) == "failed" {
			failed = true
		}
		if value := rawString(root["status"]); value != "" {
			status = value
		}
		if len(root["response"]) > 0 {
			var response map[string]json.RawMessage
			if json.Unmarshal(root["response"], &response) != nil {
				return nil, "", ErrInvalidProtocol
			}
			if value := rawString(response["status"]); value != "" {
				status = value
			}
			if len(response["output"]) > 0 {
				parsed, err := parseRawResponsesItems(response["output"])
				if err != nil {
					return nil, "", err
				}
				events = append(events, parsed...)
			}
		}
		if len(root["output"]) > 0 {
			parsed, err := parseRawResponsesItems(root["output"])
			if err != nil {
				return nil, "", err
			}
			events = append(events, parsed...)
		}
		if typeName == "response.output_text.delta" {
			appendTextEvent(&events, rawString(root["delta"]))
		}
		if typeName == "response.output_item.done" && len(root["item"]) > 0 {
			var item map[string]json.RawMessage
			if json.Unmarshal(root["item"], &item) != nil {
				return nil, "", ErrInvalidProtocol
			}
			parsed, err := parseResponsesItems([]map[string]json.RawMessage{item})
			if err != nil {
				return nil, "", err
			}
			events = append(events, parsed...)
		}
	}
	if failed || status == "failed" || status == "incomplete" || status == "cancelled" {
		return events, CompletionFailed, nil
	}
	if hasEventKind(events, EventToolCall) {
		return events, CompletionWaitingForTool, nil
	}
	if status == "completed" {
		return events, CompletionComplete, nil
	}
	return events, CompletionFailed, nil
}

func parseRawResponsesItems(raw json.RawMessage) ([]Event, error) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, ErrInvalidProtocol
	}
	return parseResponsesItems(items)
}

func protocolPayloads(body []byte) ([][]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, ErrInvalidProtocol
	}
	if trimmed[0] == '{' {
		if !json.Valid(trimmed) {
			return nil, ErrInvalidProtocol
		}
		return [][]byte{trimmed}, nil
	}
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	result := make([][]byte, 0)
	for _, event := range strings.Split(normalized, "\n\n") {
		data := make([]string, 0, 1)
		for line := range strings.SplitSeq(event, "\n") {
			if value, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimSpace(value))
			}
		}
		joined := strings.Join(data, "\n")
		if joined == "" || joined == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(joined)) {
			return nil, ErrInvalidProtocol
		}
		result = append(result, []byte(joined))
	}
	if len(result) == 0 {
		return nil, ErrInvalidProtocol
	}
	return result, nil
}

func decodeJSONObject(body []byte, target *map[string]json.RawMessage) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, target) != nil {
		return ErrInvalidProtocol
	}
	return nil
}

func textEvent(role, text string) []Event {
	if text == "" {
		return nil
	}
	kind := EventAssistantText
	if role != "assistant" {
		kind = EventUserMessage
	}
	return []Event{{Role: role, Kind: kind, Text: text}}
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch rawString(block["type"]) {
		case "text", "input_text", "output_text":
			if value := rawString(block["text"]); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func rawInt(raw json.RawMessage) int {
	var value int
	_ = json.Unmarshal(raw, &value)
	return value
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func normalizedJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(trimmed) {
		var compact bytes.Buffer
		if json.Compact(&compact, trimmed) == nil {
			return append(json.RawMessage(nil), compact.Bytes()...)
		}
		return append(json.RawMessage(nil), trimmed...)
	}
	encoded, _ := json.Marshal(string(trimmed))
	return encoded
}

func jsonStringContent(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return normalizedJSON(json.RawMessage(trimmed))
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func hasEventKind(events []Event, kind EventKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func joinedText(events []Event, kind EventKind) string {
	parts := make([]string, 0)
	for _, event := range events {
		if event.Kind == kind && event.Text != "" {
			parts = append(parts, event.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func appendTextEvent(events *[]Event, text string) {
	if text == "" {
		return
	}
	if len(*events) > 0 {
		last := &(*events)[len(*events)-1]
		if last.Kind == EventAssistantText {
			last.Text += text
			return
		}
	}
	*events = append(*events, Event{Role: "assistant", Kind: EventAssistantText, Text: text})
}
