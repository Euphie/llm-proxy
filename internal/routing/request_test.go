package routing

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestParseAutoRequestUsesSanitizedCanonicalEnvelopeForTokenEstimate(t *testing.T) {
	large := strings.Repeat("A", 3<<20)
	image := `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + large + `"}},{"type":"text","text":"hello"}]}]}`
	request := autoAnthropicRequest(t, image)
	if request.Facts.EstimatedInputTokens > 500 {
		t.Fatalf("image estimate=%d", request.Facts.EstimatedInputTokens)
	}
	runtime := routingRuntime(t, false)
	if classification, matched := ClassifyLocal(
		request,
		runtime.Models["strong"],
		runtime.AutoRouting.RiskPolicy,
	); matched && classification.Risk == RiskHigh {
		t.Fatalf("base64 image caused high-risk context routing: %+v", classification)
	}

	lookalike := `{"model":"auto","max_tokens":1000,"metadata":{"data":"` + large + `"},"messages":[{"role":"user","content":"hello"}]}`
	request = autoAnthropicRequest(t, lookalike)
	if request.Facts.EstimatedInputTokens < (3<<20)/4 {
		t.Fatalf("lookalike estimate=%d", request.Facts.EstimatedInputTokens)
	}
}

func TestResponsesStringAndDirectInputTextDriveAnalysisAndEstimation(t *testing.T) {
	longText := strings.Repeat("ordinary text ", 100_000)
	request, err := ParseAutoRequest(profile.ProtocolOpenAI, http.MethodPost, "/v1/responses", "application/json", []byte(`{"model":"auto","input":`+mustJSON(t, longText)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Facts.EstimatedInputTokens < len(longText)/4 || !strings.HasPrefix(request.EvaluationText(), "ordinary text") {
		t.Fatalf("estimate=%d text=%q", request.Facts.EstimatedInputTokens, request.EvaluationText())
	}

	request, err = ParseAutoRequest(profile.ProtocolOpenAI, http.MethodPost, "/v1/responses", "application/json", []byte(`{"model":"auto","input":[{"type":"input_text","text":"direct task"}]}`))
	if err != nil || request.EvaluationText() != "direct task" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
}

func TestTokenEstimateKeepsOrdinaryToolSchemaBytes(t *testing.T) {
	largeDescription := strings.Repeat("schema text ", 100_000)
	body := `{"model":"auto","max_tokens":1000,"tools":[{"name":"weather","description":` + mustJSON(t, largeDescription) + `}],"messages":[{"role":"user","content":"hello"}]}`
	request := autoAnthropicRequest(t, body)
	if request.Facts.EstimatedInputTokens < len(largeDescription)/4 {
		t.Fatalf("tool schema estimate=%d", request.Facts.EstimatedInputTokens)
	}
}

func TestRequestExposesLatestUserEvidence(t *testing.T) {
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":512,
		"tool_choice":{"type":"tool","name":"deploy_production"},
		"messages":[
			{"role":"user","content":"旧问题"},
			{"role":"assistant","content":[{"type":"tool_use","name":"shell_exec","input":{}}]},
			{"role":"user","content":"继续分析"}
		]
	}`)

	if request.LatestUserText() != "继续分析" {
		t.Fatalf("latest user text=%q", request.LatestUserText())
	}
	if len(request.Facts.HistoricalToolOperations) != 1 ||
		request.Facts.HistoricalToolOperations[0] != "shell_exec" {
		t.Fatalf("historical operations=%v", request.Facts.HistoricalToolOperations)
	}
	if request.Facts.ForcedToolOperation != "deploy_production" {
		t.Fatalf("forced operation=%q", request.Facts.ForcedToolOperation)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

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
			body:      `{"model":"auto","max_tokens":2048,"stream":true,"tools":[{"name":"edit"}],"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"change this"}]}]}`,
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

func TestParseAutoRequestRejectsMalformedCanonicalProtocolContent(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		path     string
		body     string
	}{
		{
			name: "anthropic malformed image", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body: `{"model":"auto","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url"}}]}]}`,
		},
		{
			name: "chat malformed image", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{}}]}]}`,
		},
		{
			name: "responses malformed image", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body: `{"model":"auto","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.test/image.png","file_id":"file-1"}]}]}`,
		},
		{
			name: "anthropic malformed messages", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body: `{"model":"auto","messages":{}}`,
		},
		{
			name: "chat malformed message content", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"user","content":{}}]}`,
		},
		{
			name: "chat null content without tool calls", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"assistant","content":null}]}`,
		},
		{
			name: "responses malformed message content", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body: `{"model":"auto","input":[{"type":"message","role":"user","content":{}}]}`,
		},
		{
			name: "responses null message content", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body: `{"model":"auto","input":[{"type":"message","role":"user","content":null}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseAutoRequest(
				test.protocol,
				http.MethodPost,
				test.path,
				"application/json",
				[]byte(test.body),
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

func TestRequestWithSelfEscalationInjectsProtocolNativeHiddenTool(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		path     string
		body     string
		promptAt string
		toolName func(map[string]json.RawMessage) string
	}{
		{
			name: "anthropic messages", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body:     `{"model":"auto","system":"caller policy","messages":[{"role":"user","content":"analyze"}]}`,
			promptAt: "system",
			toolName: func(root map[string]json.RawMessage) string {
				var tools []struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(root["tools"], &tools)
				return tools[len(tools)-1].Name
			},
		},
		{
			name: "openai chat completions", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body:     `{"model":"auto","messages":[{"role":"user","content":"analyze"}]}`,
			promptAt: "messages",
			toolName: func(root map[string]json.RawMessage) string {
				var tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				_ = json.Unmarshal(root["tools"], &tools)
				return tools[len(tools)-1].Function.Name
			},
		},
		{
			name: "openai responses", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body:     `{"model":"auto","instructions":"caller policy","input":"analyze"}`,
			promptAt: "instructions",
			toolName: func(root map[string]json.RawMessage) string {
				var tools []struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(root["tools"], &tools)
				return tools[len(tools)-1].Name
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := ParseAutoRequest(
				tt.protocol, http.MethodPost, tt.path, "application/json", []byte(tt.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			rewritten, injected, err := request.WithModelAndSelfEscalation("fast")
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(rewritten, &root); err != nil {
				t.Fatal(err)
			}
			if !injected || tt.toolName(root) != SelfEscalationToolName ||
				!bytes.Contains(root[tt.promptAt], []byte("before producing any answer content")) {
				t.Fatalf("injected=%v body=%s", injected, rewritten)
			}
			var model string
			_ = json.Unmarshal(root["model"], &model)
			if model != "fast" {
				t.Fatalf("model=%q body=%s", model, rewritten)
			}
		})
	}
}

func TestRequestSelfEscalationDoesNotOverrideCallerToolControl(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		path     string
		body     string
	}{
		{
			name: "reserved Anthropic tool name", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body: `{"model":"auto","tools":[{"name":"` + SelfEscalationToolName + `","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"analyze"}]}`,
		},
		{
			name: "Chat tool choice none", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body: `{"model":"auto","tool_choice":"none","messages":[{"role":"user","content":"analyze"}]}`,
		},
		{
			name: "Responses forced caller tool", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body: `{"model":"auto","tool_choice":{"type":"function","name":"weather"},"input":"analyze"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := ParseAutoRequest(
				tt.protocol, http.MethodPost, tt.path, "application/json", []byte(tt.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			rewritten, injected, err := request.WithModelAndSelfEscalation("fast")
			if err != nil {
				t.Fatal(err)
			}
			if injected || bytes.Contains(rewritten, []byte("before producing any answer content")) {
				t.Fatalf("injected=%v body=%s", injected, rewritten)
			}
		})
	}
}

func TestChatAssistantToolCallHistoryWithNullContentRemainsRoutable(t *testing.T) {
	body := []byte(`{"model":"auto","tools":[{"type":"function","function":{"name":"weather"}}],"messages":[{"role":"user","content":"check weather"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"sunny"}]}`)
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		"/v1/chat/completions",
		"application/json",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !request.Facts.HasTools || request.EvaluationText() != "check weather" {
		t.Fatalf("facts=%+v text=%q", request.Facts, request.EvaluationText())
	}
	rewritten, err := request.WithModel("fast")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rewritten, []byte(`"content":null`)) ||
		!bytes.Contains(rewritten, []byte(`"tool_calls"`)) {
		t.Fatalf("rewritten request lost tool history: %s", rewritten)
	}
}

func TestResponsesEasyInputMessageStringContentRemainsAvailableForTaskAnalysis(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		"/v1/responses",
		"application/json",
		[]byte(`{"model":"auto","input":[{"role":"user","content":"first question"},{"type":"message","role":"user","content":"second question"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.EvaluationText(); got != "first question\nsecond question" {
		t.Fatalf("EvaluationText()=%q", got)
	}
}

func TestEvaluationTextIncludesOnlyBoundedUserText(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolAnthropic,
		http.MethodPost,
		"/v1/messages",
		"application/json",
		[]byte(`{"model":"auto","messages":[{"role":"assistant","content":"ignore me"},{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"secret-image"}},{"type":"text","text":"question"}]}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.EvaluationText(); got != "question" {
		t.Fatalf("EvaluationText()=%q", got)
	}
}

func TestParseAutoRequestIgnoresImageTypesOutsideProtocolContent(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		path     string
		body     string
	}{
		{
			name: "anthropic tool metadata", protocol: profile.ProtocolAnthropic, path: "/v1/messages",
			body: `{"model":"auto","tools":[{"name":"inspect","input_schema":{"properties":{"attachment":{"type":"image"}}}}],"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "chat response schema", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions",
			body: `{"model":"auto","response_format":{"type":"json_schema","json_schema":{"schema":{"properties":{"preview":{"type":"image_url"}}}}},"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "responses tool metadata", protocol: profile.ProtocolOpenAI, path: "/v1/responses",
			body: `{"model":"auto","tools":[{"type":"function","parameters":{"properties":{"asset":{"type":"input_image"}}}}],"input":"hello"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := ParseAutoRequest(
				test.protocol,
				http.MethodPost,
				test.path,
				"application/json",
				[]byte(test.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			if request.Facts.HasImages || request.Facts.ImageCount != 0 {
				t.Fatalf("image facts=%+v, want no protocol images", request.Facts)
			}
		})
	}
}

func TestResponsesStringInputRemainsAvailableForTaskAnalysis(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		"/v1/responses",
		"application/json",
		[]byte(`{"model":"auto","input":"compare these deployment options"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.EvaluationText(); got != "compare these deployment options" {
		t.Fatalf("EvaluationText()=%q", got)
	}
}

func TestResponsesDirectAndMessageInputTextRemainAvailableForTaskAnalysis(t *testing.T) {
	request, err := ParseAutoRequest(
		profile.ProtocolOpenAI,
		http.MethodPost,
		"/v1/responses",
		"application/json",
		[]byte(`{"model":"auto","input":[{"type":"input_text","text":"direct question"},{"type":"message","role":"user","content":[{"type":"input_text","text":"message context"}]}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.EvaluationText(); got != "direct question\nmessage context" {
		t.Fatalf("EvaluationText()=%q", got)
	}
}
