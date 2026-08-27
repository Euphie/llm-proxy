package agenttrajectory

import (
	"errors"
	"testing"

	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestParseTurnNormalizesSupportedAgentProtocols(t *testing.T) {
	tests := []struct {
		name       string
		operation  routing.Operation
		request    string
		response   string
		completion CompletionState
		toolCalls  int
		finalText  string
		want       []Event
	}{
		{
			name:      "anthropic final turn",
			operation: routing.OperationAnthropicMessages,
			request: `{"model":"auto","messages":[
				{"role":"user","content":"check Paris weather"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}
			]}`,
			response:   `{"content":[{"type":"text","text":"Paris is sunny."}],"stop_reason":"end_turn"}`,
			completion: CompletionComplete,
			toolCalls:  1,
			finalText:  "Paris is sunny.",
			want: []Event{
				{Role: "user", Kind: EventUserMessage, Text: "check Paris weather"},
				{Role: "assistant", Kind: EventToolCall, CallID: "toolu_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
				{Role: "tool", Kind: EventToolResult, CallID: "toolu_1", Result: []byte(`"sunny"`)},
				{Role: "assistant", Kind: EventAssistantText, Text: "Paris is sunny."},
			},
		},
		{
			name:      "chat completions final turn",
			operation: routing.OperationOpenAIChatCompletions,
			request: `{"model":"auto","messages":[
				{"role":"user","content":"check Paris weather"},
				{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":"sunny"}
			]}`,
			response:   `{"choices":[{"message":{"role":"assistant","content":"Paris is sunny."},"finish_reason":"stop"}]}`,
			completion: CompletionComplete,
			toolCalls:  1,
			finalText:  "Paris is sunny.",
			want: []Event{
				{Role: "user", Kind: EventUserMessage, Text: "check Paris weather"},
				{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
				{Role: "tool", Kind: EventToolResult, CallID: "call_1", Result: []byte(`"sunny"`)},
				{Role: "assistant", Kind: EventAssistantText, Text: "Paris is sunny."},
			},
		},
		{
			name:      "responses final turn",
			operation: routing.OperationOpenAIResponses,
			request: `{"model":"auto","input":[
				{"role":"user","content":[{"type":"input_text","text":"check Paris weather"}]},
				{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"},
				{"type":"function_call_output","call_id":"call_1","output":"sunny"}
			]}`,
			response:   `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Paris is sunny."}]}]}`,
			completion: CompletionComplete,
			toolCalls:  1,
			finalText:  "Paris is sunny.",
			want: []Event{
				{Role: "user", Kind: EventUserMessage, Text: "check Paris weather"},
				{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
				{Role: "tool", Kind: EventToolResult, CallID: "call_1", Result: []byte(`"sunny"`)},
				{Role: "assistant", Kind: EventAssistantText, Text: "Paris is sunny."},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, err := ParseTurn(tt.operation, []byte(tt.request), []byte(tt.response))
			if err != nil {
				t.Fatal(err)
			}
			if turn.Completion != tt.completion || turn.ToolCalls != tt.toolCalls || turn.FinalText != tt.finalText {
				t.Fatalf("turn=%+v", turn)
			}
			got := append(append([]Event{}, turn.History...), turn.Response...)
			assertEvents(t, got, tt.want)
		})
	}
}

func TestParseTurnKeepsToolCallsOpen(t *testing.T) {
	tests := []struct {
		name      string
		operation routing.Operation
		request   string
		response  string
		want      Event
	}{
		{
			name: "anthropic", operation: routing.OperationAnthropicMessages,
			request:  `{"messages":[{"role":"user","content":"check weather"}]}`,
			response: `{"content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use"}`,
			want:     Event{Role: "assistant", Kind: EventToolCall, CallID: "toolu_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
		{
			name: "chat completions", operation: routing.OperationOpenAIChatCompletions,
			request:  `{"messages":[{"role":"user","content":"check weather"}]}`,
			response: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
			want:     Event{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
		{
			name: "responses", operation: routing.OperationOpenAIResponses,
			request:  `{"input":"check weather"}`,
			response: `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"}]}`,
			want:     Event{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, err := ParseTurn(tt.operation, []byte(tt.request), []byte(tt.response))
			if err != nil {
				t.Fatal(err)
			}
			if turn.Completion != CompletionWaitingForTool || turn.FinalText != "" || turn.ToolCalls != 1 {
				t.Fatalf("tool response treated as final: %+v", turn)
			}
			assertEvents(t, turn.Response, []Event{tt.want})
		})
	}
}

func TestParseTurnNormalizesStreamingToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		operation routing.Operation
		response  string
		want      Event
	}{
		{
			name: "anthropic", operation: routing.OperationAnthropicMessages,
			response: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"weather\",\"input\":{}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Paris\\\"}\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n",
			want: Event{Role: "assistant", Kind: EventToolCall, CallID: "toolu_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
		{
			name: "chat completions", operation: routing.OperationOpenAIChatCompletions,
			response: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Paris\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
			want: Event{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
		{
			name: "responses", operation: routing.OperationOpenAIResponses,
			response: "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			want: Event{Role: "assistant", Kind: EventToolCall, CallID: "call_1", ToolName: "weather", Arguments: []byte(`{"city":"Paris"}`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, err := ParseTurn(tt.operation, []byte(`{"input":"check weather","messages":[{"role":"user","content":"check weather"}]}`), []byte(tt.response))
			if err != nil {
				t.Fatal(err)
			}
			if turn.Completion != CompletionWaitingForTool || turn.ToolCalls != 1 || turn.FinalText != "" {
				t.Fatalf("turn=%+v", turn)
			}
			assertEvents(t, turn.Response, []Event{tt.want})
		})
	}
}

func TestParseTurnCompletesStreamingFinalText(t *testing.T) {
	tests := []struct {
		operation routing.Operation
		response  string
	}{
		{
			routing.OperationAnthropicMessages,
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		},
		{
			routing.OperationOpenAIChatCompletions,
			"data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		},
		{
			routing.OperationOpenAIResponses,
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
		},
	}
	for _, tt := range tests {
		turn, err := ParseTurn(tt.operation, []byte(`{}`), []byte(tt.response))
		if err != nil {
			t.Fatal(err)
		}
		if turn.Completion != CompletionComplete || turn.FinalText != "done" {
			t.Fatalf("operation=%q turn=%+v", tt.operation, turn)
		}
	}
}

func TestParseTurnMarksExplicitProtocolFailure(t *testing.T) {
	tests := []struct {
		operation routing.Operation
		response  string
	}{
		{routing.OperationAnthropicMessages, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`},
		{routing.OperationOpenAIChatCompletions, `{"error":{"message":"busy","type":"server_error"}}`},
		{routing.OperationOpenAIResponses, `{"status":"failed","error":{"message":"busy"}}`},
	}
	for _, tt := range tests {
		turn, err := ParseTurn(tt.operation, []byte(`{}`), []byte(tt.response))
		if err != nil {
			t.Fatal(err)
		}
		if turn.Completion != CompletionFailed {
			t.Fatalf("response=%s completion=%q", tt.response, turn.Completion)
		}
	}
}

func TestParseTurnRejectsMalformedAndUnsupportedPayloads(t *testing.T) {
	for _, test := range []struct {
		operation routing.Operation
		request   string
		response  string
	}{
		{routing.OperationAnthropicMessages, `{`, `{}`},
		{routing.OperationOpenAIChatCompletions, `{}`, `not json`},
		{routing.Operation("unknown"), `{}`, `{}`},
	} {
		_, err := ParseTurn(test.operation, []byte(test.request), []byte(test.response))
		if !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("operation=%q err=%v", test.operation, err)
		}
	}
}

func assertEvents(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events %+v, want %d %+v", len(got), got, len(want), want)
	}
	for index := range want {
		if got[index].Role != want[index].Role || got[index].Kind != want[index].Kind ||
			got[index].CallID != want[index].CallID || got[index].ToolName != want[index].ToolName ||
			got[index].Text != want[index].Text || string(got[index].Arguments) != string(want[index].Arguments) ||
			string(got[index].Result) != string(want[index].Result) {
			t.Fatalf("event %d=%+v, want %+v", index, got[index], want[index])
		}
	}
}
