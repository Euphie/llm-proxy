package evaluation

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestParseModelOutputExtractsComparableTextAcrossSupportedProtocols(t *testing.T) {
	tests := []struct {
		name      string
		operation routing.Operation
		body      string
		want      string
	}{
		{
			name: "anthropic json", operation: routing.OperationAnthropicMessages,
			body: `{"content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}`,
			want: "hello\nworld",
		},
		{
			name: "anthropic stream", operation: routing.OperationAnthropicMessages,
			body: "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n",
			want: "hello",
		},
		{
			name: "openai chat json", operation: routing.OperationOpenAIChatCompletions,
			body: `{"choices":[{"message":{"content":"hello"}}]}`,
			want: "hello",
		},
		{
			name: "openai responses stream", operation: routing.OperationOpenAIResponses,
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n",
			want: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := ParseModelOutput(tt.operation, []byte(tt.body), routing.RequestFacts{}, 12, 34)
			if output.Text != tt.want || output.CostMicroUSD != 12 || output.LatencyMS != 34 ||
				output.DeterministicFailure || output.SevereError {
				t.Fatalf("output=%+v", output)
			}
		})
	}
}

func TestParseModelOutputMarksEmptyAndMalformedStructuredResultsDeterministically(t *testing.T) {
	empty := ParseModelOutput(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[]}`),
		routing.RequestFacts{}, 1, 2,
	)
	if !empty.DeterministicFailure || !empty.SevereError {
		t.Fatalf("empty output=%+v", empty)
	}
	malformed := ParseModelOutput(
		routing.OperationOpenAIChatCompletions,
		[]byte(`{"choices":[{"message":{"content":"not json"}}]}`),
		routing.RequestFacts{RequiresStructuredOutput: true}, 1, 2,
	)
	if !malformed.DeterministicFailure || !malformed.SevereError {
		t.Fatalf("structured output=%+v", malformed)
	}
	valid := ParseModelOutput(
		routing.OperationOpenAIChatCompletions,
		[]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`),
		routing.RequestFacts{RequiresStructuredOutput: true}, 1, 2,
	)
	if valid.DeterministicFailure {
		t.Fatalf("valid structured output=%+v", valid)
	}
}

func TestBuildReviewRequestUsesBlindJSONDataAndStrictSchema(t *testing.T) {
	input := ReviewInput{Context: "tool lookup returned sunny", Question: "question", A: "answer a", B: "answer b"}
	for _, operation := range []routing.Operation{
		routing.OperationAnthropicMessages,
		routing.OperationOpenAIChatCompletions,
		routing.OperationOpenAIResponses,
	} {
		body, err := BuildReviewRequest(operation, "judge-model", input)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(body, &root); err != nil {
			t.Fatal(err)
		}
		if root["model"] != "judge-model" || root["stream"] != false {
			t.Fatalf("request=%s", body)
		}
		encoded := string(body)
		if !strings.Contains(encoded, "tool lookup returned sunny") ||
			!strings.Contains(encoded, "answer a") || !strings.Contains(encoded, "answer b") ||
			strings.Contains(encoded, "candidate_model") || strings.Contains(encoded, "reference_model") {
			t.Fatalf("review request is not blind: %s", body)
		}
		if !strings.Contains(encoded, `"submit_routing_review"`) || strings.Contains(encoded, `"tool_choice"`) {
			t.Fatalf("review request must offer an ordinary verdict tool without forcing it: %s", body)
		}
	}
}

func TestBuildReviewRepairRequestUsesLargerOutputBudget(t *testing.T) {
	input := ReviewInput{Question: "question", A: "answer a", B: "answer b"}
	initial, err := BuildReviewRequest(routing.OperationAnthropicMessages, "judge-model", input)
	if err != nil {
		t.Fatal(err)
	}
	repair, err := BuildReviewRepairRequest(routing.OperationAnthropicMessages, "judge-model", input)
	if err != nil {
		t.Fatal(err)
	}
	var initialRequest, repairRequest struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(initial, &initialRequest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(repair, &repairRequest); err != nil {
		t.Fatal(err)
	}
	if initialRequest.MaxTokens != 512 || repairRequest.MaxTokens != 2048 {
		t.Fatalf("initial=%d repair=%d", initialRequest.MaxTokens, repairRequest.MaxTokens)
	}
}

func TestParseReviewVerdictAcceptsOrdinaryVerdictToolAcrossSupportedProtocols(t *testing.T) {
	arguments := `{"dimensions":{"correctness":"b","completeness":"tie","instruction_following":"a","format_tool_safety":"b","task_completion":"b"},"severe_a":false,"severe_b":true}`
	tests := []struct {
		operation routing.Operation
		body      string
	}{
		{
			operation: routing.OperationAnthropicMessages,
			body:      `{"content":[{"type":"tool_use","id":"t1","name":"submit_routing_review","input":` + arguments + `}],"stop_reason":"tool_use"}`,
		},
		{
			operation: routing.OperationOpenAIChatCompletions,
			body:      `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_routing_review","arguments":` + strconv.Quote(arguments) + `}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			operation: routing.OperationOpenAIResponses,
			body:      `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"submit_routing_review","arguments":` + strconv.Quote(arguments) + `}]}`,
		},
	}
	for _, tt := range tests {
		verdict, err := ParseReviewVerdict(tt.operation, []byte(tt.body))
		if err != nil {
			t.Fatalf("operation=%q err=%v", tt.operation, err)
		}
		if verdict.Dimensions[DimensionCorrectness] != WinnerB ||
			verdict.Dimensions[DimensionCompleteness] != WinnerTie ||
			verdict.Dimensions[DimensionInstructionFollowing] != WinnerA ||
			verdict.SevereA || !verdict.SevereB {
			t.Fatalf("operation=%q verdict=%+v", tt.operation, verdict)
		}
	}
}

func TestParseReviewVerdictReturnsSpecificProtocolFailure(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ReviewFailureCode
	}{
		{name: "empty", body: `{"content":[]}`, want: ReviewFailureEmpty},
		{
			name: "output exhausted before verdict",
			body: `{"content":[{"type":"thinking","thinking":"still reasoning"}],"stop_reason":"max_tokens"}`,
			want: ReviewFailureOutputExhausted,
		},
		{
			name: "wrong tool",
			body: `{"content":[{"type":"tool_use","id":"t1","name":"other_tool","input":{}}],"stop_reason":"tool_use"}`,
			want: ReviewFailureWrongTool,
		},
		{
			name: "invalid json",
			body: `{"content":[{"type":"text","text":"not-json"}]}`,
			want: ReviewFailureInvalidJSON,
		},
		{
			name: "missing dimensions",
			body: `{"content":[{"type":"text","text":"{\"dimensions\":{\"correctness\":\"a\"},\"severe_a\":false,\"severe_b\":false}"}]}`,
			want: ReviewFailureMissingFields,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseReviewVerdict(routing.OperationAnthropicMessages, []byte(tt.body))
			if ReviewFailureCodeOf(err) != tt.want {
				t.Fatalf("err=%v code=%q want=%q", err, ReviewFailureCodeOf(err), tt.want)
			}
		})
	}
}

func TestParseModelOutputAndReviewVerdictRejectToolCallAttempts(t *testing.T) {
	output := ParseModelOutput(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[{"type":"text","text":"partial"},{"type":"tool_use","id":"t1","name":"danger","input":{}}],"stop_reason":"tool_use"}`),
		routing.RequestFacts{}, 12, 34,
	)
	if !output.ToolCallAttempted || !output.DeterministicFailure || !output.SevereError {
		t.Fatalf("output=%+v", output)
	}
	if _, err := ParseReviewVerdict(
		routing.OperationOpenAIChatCompletions,
		[]byte(`{"choices":[{"message":{"content":"{\"dimensions\":{}}","tool_calls":[{"id":"c1","type":"function","function":{"name":"danger","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
	); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("review tool-call err=%v", err)
	}
}

func TestReviewVerdictRequiresAllDimensions(t *testing.T) {
	verdict, err := ParseReviewVerdict(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[{"type":"text","text":"{\"dimensions\":{\"correctness\":\"b\",\"completeness\":\"tie\",\"instruction_following\":\"a\",\"format_tool_safety\":\"b\",\"task_completion\":\"b\"},\"severe_a\":false,\"severe_b\":true}"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Dimensions[DimensionCorrectness] != WinnerB ||
		verdict.Dimensions[DimensionCompleteness] != WinnerTie ||
		verdict.Dimensions[DimensionInstructionFollowing] != WinnerA ||
		verdict.SevereA || !verdict.SevereB {
		t.Fatalf("verdict=%+v", verdict)
	}
	if _, err := ParseReviewVerdict(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[{"type":"text","text":"{\"dimensions\":{\"correctness\":\"a\"},\"severe_a\":false,\"severe_b\":false}"}]}`),
	); err == nil {
		t.Fatal("accepted an incomplete dimension verdict")
	}
}
