package routing

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestRequestedModelRecognizesOnlyTopLevelString(t *testing.T) {
	tests := []struct {
		body  string
		model string
		ok    bool
	}{
		{body: `{"model":"auto","messages":[]}`, model: "auto", ok: true},
		{body: `{"model":"claude-sonnet"}`, model: "claude-sonnet", ok: true},
		{body: `{"model":12}`, ok: false},
		{body: `{"nested":{"model":"auto"}}`, ok: false},
		{body: `{`, ok: false},
	}

	for _, tt := range tests {
		model, ok := RequestedModel([]byte(tt.body))
		if model != tt.model || ok != tt.ok {
			t.Fatalf("RequestedModel(%q)=(%q,%v), want (%q,%v)", tt.body, model, ok, tt.model, tt.ok)
		}
	}
}

func TestParseAutoRequestSupportedOperations(t *testing.T) {
	tests := []struct {
		name      string
		protocol  profile.Protocol
		path      string
		body      string
		operation Operation
		facts     RequestFacts
	}{
		{
			name: "anthropic messages", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body:      `{"model":"auto","max_tokens":2048,"stream":true,"tools":[{"name":"edit"}],"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"aW1hZ2U="}},{"type":"text","text":"change this"}]}]}`,
			operation: OperationAnthropicMessages,
			facts:     RequestFacts{RequestedOutputTokens: 2048, ImageCount: 1, HasImages: true, HasTools: true, RequiresStructuredOutput: true, Stream: true},
		},
		{
			name: "openai chat completions", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body:      `{"model":"auto","max_completion_tokens":1024,"stream":true,"tools":[{"type":"function"}],"response_format":{"type":"json_schema"},"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"change this"}]}]}`,
			operation: OperationOpenAIChatCompletions,
			facts:     RequestFacts{RequestedOutputTokens: 1024, ImageCount: 1, HasImages: true, HasTools: true, RequiresStructuredOutput: true, Stream: true},
		},
		{
			name: "openai responses", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body:      `{"model":"auto","max_output_tokens":4096,"stream":false,"tools":[{"type":"web_search"}],"text":{"format":{"type":"json_schema"}},"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="},{"type":"input_text","text":"explain"}]}]}`,
			operation: OperationOpenAIResponses,
			facts:     RequestFacts{RequestedOutputTokens: 4096, ImageCount: 1, HasImages: true, HasTools: true, RequiresStructuredOutput: true, Stream: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := ParseAutoRequest(
				tt.protocol,
				http.MethodPost,
				tt.path,
				"application/json; charset=utf-8",
				[]byte(tt.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			if request.Operation != tt.operation || request.Model != AutoModel {
				t.Fatalf("request=%+v", request)
			}
			if request.Facts.RequestedOutputTokens != tt.facts.RequestedOutputTokens ||
				request.Facts.ImageCount != tt.facts.ImageCount ||
				request.Facts.HasImages != tt.facts.HasImages ||
				request.Facts.HasTools != tt.facts.HasTools ||
				request.Facts.RequiresStructuredOutput != tt.facts.RequiresStructuredOutput ||
				request.Facts.Stream != tt.facts.Stream {
				t.Fatalf("facts=%+v, want %+v", request.Facts, tt.facts)
			}
			if request.Facts.EstimatedInputTokens <= 0 {
				t.Fatalf("estimated input tokens=%d", request.Facts.EstimatedInputTokens)
			}
		})
	}
}

func TestParseAutoRequestRejectsUnsupportedOperation(t *testing.T) {
	tests := []struct {
		protocol profile.Protocol
		method   string
		path     string
	}{
		{protocol: profile.ProtocolAnthropic, method: http.MethodGet, path: "/v1/messages"},
		{protocol: profile.ProtocolAnthropic, method: http.MethodPost, path: "/v1/complete"},
		{protocol: profile.ProtocolOpenAI, method: http.MethodPost, path: "/v1/embeddings"},
	}

	for _, tt := range tests {
		_, err := ParseAutoRequest(
			tt.protocol,
			tt.method,
			tt.path,
			"application/json",
			[]byte(`{"model":"auto"}`),
		)
		if !errors.Is(err, ErrUnsupportedOperation) {
			t.Fatalf("ParseAutoRequest(%s %s) error=%v, want ErrUnsupportedOperation", tt.method, tt.path, err)
		}
	}
}

func TestParseAutoRequestRejectsInvalidMediaAndBody(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "wrong media type", contentType: "text/plain", body: `{"model":"auto"}`},
		{name: "invalid json", contentType: "application/json", body: `{`},
		{name: "wrong model", contentType: "application/json", body: `{"model":"strong"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAutoRequest(
				profile.ProtocolAnthropic,
				http.MethodPost,
				"/v1/messages",
				tt.contentType,
				[]byte(tt.body),
			)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestRequestWithModelPreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"auto","messages":[],"vendor_extension":{"enabled":true},"number":7}`)
	request, err := ParseAutoRequest(
		profile.ProtocolAnthropic,
		http.MethodPost,
		"/v1/messages",
		"application/json",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}

	rewritten, err := request.WithModel("selected-model")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(decoded["model"], &model); err != nil {
		t.Fatal(err)
	}
	if model != "selected-model" || string(decoded["vendor_extension"]) != `{"enabled":true}` ||
		string(decoded["number"]) != "7" {
		t.Fatalf("rewritten=%s", rewritten)
	}
	if got, _ := RequestedModel(body); got != AutoModel {
		t.Fatalf("original body was mutated, model=%q", got)
	}
}

func TestRequestWithModelNonStreamingKeepsOriginalImmutable(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		"/v1/chat/completions",
		"application/json",
		[]byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	rewritten, err := request.WithModelNonStreaming("fast")
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(rewritten, &root); err != nil {
		t.Fatal(err)
	}
	if root.Model != "fast" || root.Stream {
		t.Fatalf("rewritten=%s", rewritten)
	}
	if !request.Facts.Stream || request.Model != AutoModel {
		t.Fatalf("original request changed: %+v", request)
	}
}

func TestEvaluationTextIncludesOnlyBoundedUserText(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolAnthropic,
		http.MethodPost,
		"/v1/messages",
		"application/json",
		[]byte(`{"model":"auto","messages":[{"role":"assistant","content":"ignore me"},{"role":"user","content":[{"type":"image","source":{"data":"secret-image"}},{"type":"text","text":"question"}]}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.EvaluationText(); got != "question" {
		t.Fatalf("EvaluationText()=%q", got)
	}
}
