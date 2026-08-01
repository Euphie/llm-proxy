package evaluation

import (
	"encoding/json"
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
	input := ReviewInput{Question: "question", A: "answer a", B: "answer b"}
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
		if !strings.Contains(encoded, "answer a") || !strings.Contains(encoded, "answer b") ||
			strings.Contains(encoded, "candidate_model") || strings.Contains(encoded, "reference_model") {
			t.Fatalf("review request is not blind: %s", body)
		}
	}
}

func TestParseReviewVerdictRequiresExactMachineReadableDecision(t *testing.T) {
	verdict, err := ParseReviewVerdict(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[{"type":"text","text":"{\"winner\":\"b\",\"severe_a\":false,\"severe_b\":true}"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Winner != WinnerB || verdict.SevereA || !verdict.SevereB {
		t.Fatalf("verdict=%+v", verdict)
	}
	if _, err := ParseReviewVerdict(
		routing.OperationAnthropicMessages,
		[]byte(`{"content":[{"type":"text","text":"winner: a"}]}`),
	); err == nil {
		t.Fatal("accepted a non-JSON review verdict")
	}
}
