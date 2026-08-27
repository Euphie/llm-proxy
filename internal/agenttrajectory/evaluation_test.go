package agenttrajectory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestBuildReferenceRequestUsesObservedTrajectoryWithoutTools(t *testing.T) {
	trajectory := Plaintext{Events: []Event{
		{Role: "user", Kind: EventUserMessage, Text: "check weather"},
		{Role: "assistant", Kind: EventToolCall, CallID: "c1", ToolName: "weather", Arguments: json.RawMessage(`{"city":"Paris"}`)},
		{Role: "tool", Kind: EventToolResult, CallID: "c1", Result: json.RawMessage(`"sunny"`)},
	}}
	for _, operation := range []routing.Operation{
		routing.OperationAnthropicMessages,
		routing.OperationOpenAIChatCompletions,
		routing.OperationOpenAIResponses,
	} {
		body, err := BuildReferenceRequest(operation, "strong", trajectory)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(body, &root); err != nil {
			t.Fatal(err)
		}
		if root["model"] != "strong" || root["stream"] != false {
			t.Fatalf("operation=%q request=%s", operation, body)
		}
		encoded := string(body)
		if strings.Contains(encoded, `"tools"`) || strings.Contains(encoded, `"tool_choice"`) ||
			!strings.Contains(encoded, "check weather") || !strings.Contains(encoded, "weather") ||
			!strings.Contains(encoded, "sunny") {
			t.Fatalf("operation=%q request=%s", operation, body)
		}
	}
}

func TestParseTextOnlyEvaluationOutputRejectsAnyToolCall(t *testing.T) {
	tests := []struct {
		operation routing.Operation
		body      string
	}{
		{
			routing.OperationAnthropicMessages,
			`{"content":[{"type":"text","text":"partial"},{"type":"tool_use","id":"t1","name":"weather","input":{}}],"stop_reason":"tool_use"}`,
		},
		{
			routing.OperationOpenAIChatCompletions,
			`{"choices":[{"message":{"content":"partial","tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			routing.OperationOpenAIResponses,
			`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]},{"type":"function_call","call_id":"c1","name":"weather","arguments":"{}"}]}`,
		},
	}
	for _, tt := range tests {
		output, err := ParseTextOnlyEvaluationOutput(tt.operation, []byte(tt.body), routing.RequestFacts{}, 10, 20)
		if !errors.Is(err, ErrEvaluationToolCall) || !output.ToolCallAttempted ||
			!output.DeterministicFailure || !output.SevereError {
			t.Fatalf("operation=%q output=%+v err=%v", tt.operation, output, err)
		}
	}
}

func TestTrajectoryReviewContextIsCompactAndBlinded(t *testing.T) {
	trajectory := Plaintext{Events: []Event{
		{Role: "user", Kind: EventUserMessage, Text: "task"},
		{Role: "assistant", Kind: EventToolCall, CallID: "c1", ToolName: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)},
		{Role: "tool", Kind: EventToolResult, CallID: "c1", Result: json.RawMessage(`{"value":"y"}`)},
	}}
	context := ReviewContext(trajectory, 4096)
	if !strings.Contains(context, "USER") || !strings.Contains(context, "TOOL CALL lookup") ||
		!strings.Contains(context, "TOOL RESULT") {
		t.Fatalf("context=%q", context)
	}
	input := evaluation.ReviewInput{Context: context, Question: "task", A: "candidate", B: "reference"}
	body, err := evaluation.BuildReviewRequest(routing.OperationAnthropicMessages, "judge", input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "candidate_model") || strings.Contains(string(body), "reference_model") {
		t.Fatalf("review request leaked identities: %s", body)
	}
}

func TestEvaluationFailureReasonPreservesReviewProtocolFailure(t *testing.T) {
	invalid := &evaluation.ReviewFailureError{Code: evaluation.ReviewFailureInvalidJSON}
	if got := evaluationFailureReason(invalid); got != "review_invalid_json" {
		t.Fatalf("reason=%q", got)
	}
	repair := evaluation.RepairReviewFailure(invalid)
	if got := evaluationFailureReason(repair); got != "review_repair_invalid_json" {
		t.Fatalf("repair reason=%q", got)
	}
	if got := evaluationFailureReason(errors.New("upstream unavailable")); got != "evaluation_failed" {
		t.Fatalf("generic reason=%q", got)
	}
}
