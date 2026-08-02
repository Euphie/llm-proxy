package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/routing"
	"github.com/Euphie/llm-proxy/internal/stats"
)

func TestAutoStreamingRetriesOnlyBeforeClientCommit(t *testing.T) {
	validEvent := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"second\"}}\n\n"
	transport := &sequenceTransport{responses: []transportResponse{
		{
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"text/event-stream"}},
			body:   &readErrorBody{data: []byte("event: message_start\ndata: {"), err: errors.New("connection reset")},
		},
		{
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"text/event-stream"}},
			body:   io.NopCloser(strings.NewReader(validEvent)),
		},
	}}
	runtime := autoProxyRuntime(t, "https://upstream.test", false)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, &http.Client{Transport: transport}, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || transport.calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, transport.calls.Load(), response.Body.String())
	}
	if response.Body.String() != validEvent || strings.Contains(response.Body.String(), "connection reset") {
		t.Fatalf("client received pre-commit bytes: %q", response.Body.String())
	}
}

func TestAutoStreamingNeverRetriesAfterClientCommit(t *testing.T) {
	validEvent := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"first\"}}\n\n"
	transport := &sequenceTransport{responses: []transportResponse{
		{
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"text/event-stream"}},
			body:   &readErrorBody{data: []byte(validEvent), err: errors.New("connection reset")},
		},
		{
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"text/event-stream"}},
			body:   io.NopCloser(strings.NewReader("must-not-send")),
		},
	}}
	runtime := autoProxyRuntime(t, "https://upstream.test", false)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, &http.Client{Transport: transport}, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || transport.calls.Load() != 1 || response.Body.String() != validEvent {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, transport.calls.Load(), response.Body.String())
	}
}

func TestAutoStreamingAnthropicErrorEventFailsHardBeforeClientCommit(t *testing.T) {
	invalidEvent := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n"
	response, calls := serveAutoStreamSequence(
		t,
		profile.ProtocolAnthropic,
		"/v1/messages",
		`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
		invalidEvent,
	)

	assertAutoStreamProtocolFailure(t, response, calls, invalidEvent)
}

func TestAutoStreamingRejectsNonHeartbeatEventWithoutDataBeforeClientCommit(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "error event", event: "event: error\n\n"},
		{name: "message start", event: "event: message_start\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolAnthropic,
				"/v1/messages",
				`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
				test.event,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.event)
		})
	}
}

func TestAutoStreamingOversizedIncompleteEventFailsHardBeforeClientCommit(t *testing.T) {
	upstreamBody := strings.Repeat("x", maxPrecommitStreamBytes+1)
	response, calls := serveAutoStreamSequence(
		t,
		profile.ProtocolAnthropic,
		"/v1/messages",
		`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
		upstreamBody,
	)

	if response.Code != http.StatusBadGateway || calls != 1 ||
		strings.Contains(response.Body.String(), strings.Repeat("x", 64)) {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
	}
}

func TestAutoStreamingAnthropicRequiresMessageStartAsFirstCommitEvent(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "message stop", event: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{name: "message delta", event: "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{}}\n\n"},
		{name: "content block delta", event: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{}}\n\n"},
		{name: "message omitted", event: "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"},
		{name: "message null", event: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":null}\n\n"},
		{name: "message array", event: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":[]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolAnthropic,
				"/v1/messages",
				`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
				test.event,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.event)
		})
	}
}

func TestAutoStreamingChatRequiresNonEmptyChoicesInFirstChunk(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "null choices", event: "data: {\"object\":\"chat.completion.chunk\",\"choices\":null}\n\n"},
		{name: "empty choices", event: "data: {\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolOpenAI,
				"/v1/chat/completions",
				`{"model":"auto","max_completion_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
				test.event,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.event)
		})
	}
}

func TestAutoStreamingResponsesRequiresCreatedOrQueuedFirstEvent(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "in progress", event: "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{}}\n\n"},
		{name: "output delta", event: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"},
		{name: "response omitted", event: "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"},
		{name: "response null", event: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":null}\n\n"},
		{name: "response array", event: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":[]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolOpenAI,
				"/v1/responses",
				`{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好"}`,
				test.event,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.event)
		})
	}
}

func TestAutoStreamingAcceptsMinimalProtocolStartEvents(t *testing.T) {
	tests := []struct {
		name        string
		protocol    profile.Protocol
		path        string
		requestBody string
		event       string
	}{
		{
			name:        "anthropic message start",
			protocol:    profile.ProtocolAnthropic,
			path:        "/v1/messages",
			requestBody: `{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
			event:       "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n",
		},
		{
			name:        "chat completion chunk",
			protocol:    profile.ProtocolOpenAI,
			path:        "/v1/chat/completions",
			requestBody: `{"model":"auto","max_completion_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
			event:       "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{}]}\n\n",
		},
		{
			name:        "responses created",
			protocol:    profile.ProtocolOpenAI,
			path:        "/v1/responses",
			requestBody: `{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好"}`,
			event:       "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n",
		},
		{
			name:        "responses queued",
			protocol:    profile.ProtocolOpenAI,
			path:        "/v1/responses",
			requestBody: `{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好"}`,
			event:       "event: response.queued\ndata: {\"type\":\"response.queued\",\"response\":{}}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				test.protocol,
				test.path,
				test.requestBody,
				test.event,
			)

			if response.Code != http.StatusOK || calls != 1 || response.Body.String() != test.event {
				t.Fatalf("status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestAutoStreamingChatRejectsResponsesEventBeforeClientCommit(t *testing.T) {
	invalidEvent := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"wrong-protocol\"}}\n\n"
	response, calls := serveAutoStreamSequence(
		t,
		profile.ProtocolOpenAI,
		"/v1/chat/completions",
		`{"model":"auto","max_completion_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
		invalidEvent,
	)

	assertAutoStreamProtocolFailure(t, response, calls, invalidEvent)
}

func TestAutoStreamingResponsesRejectsChatChunkBeforeClientCommit(t *testing.T) {
	invalidEvent := "data: {\"id\":\"chatcmpl-wrong\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"
	response, calls := serveAutoStreamSequence(
		t,
		profile.ProtocolOpenAI,
		"/v1/responses",
		`{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好"}`,
		invalidEvent,
	)

	assertAutoStreamProtocolFailure(t, response, calls, invalidEvent)
}

func TestAutoStreamingOpenAIErrorObjectsFailHardBeforeClientCommit(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		requestBody  string
		invalidEvent string
	}{
		{
			name:         "chat completions",
			path:         "/v1/chat/completions",
			requestBody:  `{"model":"auto","max_completion_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
			invalidEvent: "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"error\":{\"message\":\"bad\"}}\n\n",
		},
		{
			name:         "responses",
			path:         "/v1/responses",
			requestBody:  `{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好"}`,
			invalidEvent: "event: response.created\ndata: {\"type\":\"response.created\",\"error\":{\"message\":\"bad\"}}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolOpenAI,
				test.path,
				test.requestBody,
				test.invalidEvent,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.invalidEvent)
		})
	}
}

func TestAutoStreamingAnthropicRejectsInvalidCompleteEventsBeforeClientCommit(t *testing.T) {
	tests := []struct {
		name  string
		first string
	}{
		{name: "arbitrary object", first: "data: {}\n\n"},
		{name: "done sentinel", first: "data: [DONE]\n\n"},
		{name: "empty data", first: "event: message_start\ndata:\n\n"},
		{name: "unknown event", first: "event: mystery\ndata: {\"type\":\"mystery\"}\n\n"},
		{name: "event type mismatch", first: "event: message_start\ndata: {\"type\":\"message_delta\"}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, calls := serveAutoStreamSequence(
				t,
				profile.ProtocolAnthropic,
				"/v1/messages",
				`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
				test.first,
			)

			assertAutoStreamProtocolFailure(t, response, calls, test.first)
		})
	}
}

func assertAutoStreamProtocolFailure(
	t *testing.T,
	response *httptest.ResponseRecorder,
	calls int32,
	upstreamBody string,
) {
	t.Helper()
	if response.Code != http.StatusBadGateway || calls != 1 ||
		strings.Contains(response.Body.String(), strings.TrimSpace(upstreamBody)) {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
	}
}

func TestAutoStreamingAcceptsCRLFHeartbeatAndMultilineData(t *testing.T) {
	validEvent := ": heartbeat\r\n\r\nevent: message_start\r\ndata: {\"type\":\r\ndata: \"message_start\",\"message\":{\"id\":\"first\"}}\r\n\r\n"
	response, calls := serveAutoStreamSequence(
		t,
		profile.ProtocolAnthropic,
		"/v1/messages",
		`{"model":"auto","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"你好"}]}`,
		validEvent,
	)

	if response.Code != http.StatusOK || calls != 1 || response.Body.String() != validEvent {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
	}
}

func serveAutoStreamSequence(
	t *testing.T,
	protocol profile.Protocol,
	path string,
	requestBody string,
	upstreamBodies ...string,
) (*httptest.ResponseRecorder, int32) {
	t.Helper()
	responses := make([]transportResponse, len(upstreamBodies))
	for index, body := range upstreamBodies {
		responses[index] = transportResponse{
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"text/event-stream"}},
			body:   io.NopCloser(strings.NewReader(body)),
		}
	}
	transport := &sequenceTransport{responses: responses}
	runtime := autoProxyRuntime(t, "https://upstream.test", false)
	runtime.Protocol = protocol
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, &http.Client{Transport: transport}, nil).ServeHTTP(response, request)
	return response, transport.calls.Load()
}

type transportResponse struct {
	status int
	header http.Header
	body   io.ReadCloser
}

type sequenceTransport struct {
	calls     atomic.Int32
	responses []transportResponse
}

func (t *sequenceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	index := int(t.calls.Add(1)) - 1
	if index >= len(t.responses) {
		return nil, errors.New("unexpected extra request")
	}
	response := t.responses[index]
	return &http.Response{
		StatusCode: response.status,
		Status:     http.StatusText(response.status),
		Header:     response.header.Clone(),
		Body:       response.body,
		Request:    request,
	}, nil
}

type readErrorBody struct {
	data []byte
	err  error
	done bool
}

func (b *readErrorBody) Read(target []byte) (int, error) {
	if b.done {
		return 0, b.err
	}
	b.done = true
	return copy(target, b.data), b.err
}

func (b *readErrorBody) Close() error { return nil }

func TestAutoRoutingAnalyzesUncertainRequestAndRewritesModel(t *testing.T) {
	var analyzerCalls atomic.Int32
	var answerCalls atomic.Int32
	var answerModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9100}"}]}`)
			return
		}
		answerCalls.Add(1)
		answerModel = model
		_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	handler := New(autoProxyRuntime(t, server.URL, false), server.Client(), nil)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if analyzerCalls.Load() != 1 || answerCalls.Load() != 1 || answerModel != "fast" {
		t.Fatalf("analyzer=%d answer=%d model=%q", analyzerCalls.Load(), answerCalls.Load(), answerModel)
	}
}

func TestAutoRoutingStopsAfterAnalyzerAuthenticationFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"private":"upstream diagnostic"}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"must not answer"}]}`)
	}))
	defer server.Close()

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(autoProxyRuntime(t, server.URL, false), server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "upstream diagnostic") {
		t.Fatalf("response leaked analyzer body: %q", response.Body.String())
	}
}

func TestSuccessfulAutoRequestOnlyEnqueuesAsyncBlindComparison(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "caller-secret" {
			t.Errorf("async request lost forwarded credential")
		}
		body, _ := io.ReadAll(r.Body)
		var root struct {
			Model  string          `json:"model"`
			Stream bool            `json:"stream"`
			System json.RawMessage `json:"system"`
		}
		_ = json.Unmarshal(body, &root)
		calls.Add(1)
		switch {
		case len(root.System) > 0:
			if bytes.Contains(body, []byte("fast")) {
				t.Errorf("blind review leaked candidate model: %s", body)
			}
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"winner\":\"tie\",\"severe_a\":false,\"severe_b\":false}"}]}`)
		case root.Model == "fast":
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"online answer"}]}`)
		case root.Model == "strong":
			if root.Stream {
				t.Error("comparison request unexpectedly enabled streaming")
			}
			_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"reference answer"}]}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	handler := NewWithEvaluation(runtime, server.Client(), nil, nil, submitter)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "caller-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || calls.Load() != 1 || submitter.calls != 1 {
		t.Fatalf("status=%d upstream_calls=%d submissions=%d body=%q",
			response.Code, calls.Load(), submitter.calls, response.Body.String())
	}
	result, err := submitter.job.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || result.Evidence == nil || result.Evidence.Outcome != evaluation.OutcomeTie ||
		result.Evidence.CandidateModel != "fast" || result.Evidence.ReferenceModel != "strong" ||
		result.Evidence.ReviewerModel != "strong" || result.SpentMicroUSD <= 0 ||
		result.SpentMicroUSD > submitter.job.EstimatedCostMicroUSD {
		t.Fatalf("upstream_calls=%d job=%+v result=%+v evidence=%+v",
			calls.Load(), submitter.job, result, result.Evidence)
	}
}

func TestAsyncEvaluationReservesAndChargesEveryPhysicalVisionRetry(t *testing.T) {
	var analyzerCalls atomic.Int32
	var onlineCalls atomic.Int32
	var answerCalls atomic.Int32
	var reviewerCalls atomic.Int32
	var visionCalls atomic.Int32
	var visionMu sync.Mutex
	visionAttempts := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case model == "vision":
			visionCalls.Add(1)
			image := "one"
			if bytes.Contains(body, []byte("image-two")) {
				image = "two"
			}
			visionMu.Lock()
			visionAttempts[image]++
			attempt := visionAttempts[image]
			visionMu.Unlock()
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"vision overloaded"}`)
				return
			}
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"visual evidence"}]}`)
		case model == "fast" && root["system"] != nil:
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
		case model == "strong" && root["system"] != nil:
			reviewerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"winner\":\"tie\",\"severe_a\":false,\"severe_b\":false}"}]}`)
		case model == "strong":
			onlineCalls.Add(1)
			_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"online answer"}]}`)
		case model == "fast":
			answerCalls.Add(1)
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"generated answer"}]}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, true)
	route := runtime.AutoRouting.Strategy.Routes["balanced"]
	route.MinQualityBPS = 9500
	runtime.AutoRouting.Strategy.Routes["balanced"] = route
	runtime.OverloadRules[0].MaxRetries = 2
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	body := `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image-one.png"}},{"type":"image","source":{"type":"url","url":"https://example.test/image-two.png"}},{"type":"text","text":"compare these layouts"}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	NewWithEvaluation(runtime, server.Client(), nil, nil, submitter).ServeHTTP(response, request)

	if response.Code != http.StatusOK || submitter.calls != 1 ||
		analyzerCalls.Load() != 1 || onlineCalls.Load() != 1 {
		t.Fatalf("status=%d submissions=%d analyzer=%d online=%d body=%q",
			response.Code, submitter.calls, analyzerCalls.Load(), onlineCalls.Load(), response.Body.String())
	}
	routeRequest, err := routing.ParseAutoRequest(
		runtime.Protocol, http.MethodPost, "/v1/messages", "application/json", []byte(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := routing.NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	classification := routing.Classification{
		TaskType: "simple", Risk: routing.RiskNormal, ConfidenceBPS: 9200,
		Source: routing.ClassificationSourceAnalyzer,
	}
	pair, ok := planner.EvaluationPair(routeRequest, classification, "strong")
	if !ok {
		t.Fatal("evaluation pair unavailable")
	}
	reviewerInputTokens := routeRequest.Facts.EstimatedInputTokens +
		routeRequest.Facts.RequestedOutputTokens*2 + 512
	reviewerCost := estimateEvaluationCallCost(reviewerInputTokens, 256, runtime.Models["strong"])
	visionCost := pair.Candidate.VisionCallCostMicroUSD()
	generatedCost := addEvaluationCost(
		pair.Candidate.AnswerCallCostMicroUSD(),
		multiplyEvaluationCost(visionCost, routeRequest.Facts.ImageCount*2),
	)
	worstCaseGeneratedCost := addEvaluationCost(
		pair.Candidate.AnswerCallCostMicroUSD(),
		multiplyEvaluationCost(visionCost, routeRequest.Facts.ImageCount*3),
	)
	worstCaseCost := addEvaluationCost(worstCaseGeneratedCost, reviewerCost)
	actualSpent := addEvaluationCost(generatedCost, reviewerCost)
	if submitter.job.EstimatedCostMicroUSD != worstCaseCost {
		t.Fatalf("reserved=%d want worst-case=%d nominal=%d",
			submitter.job.EstimatedCostMicroUSD, worstCaseCost,
			addEvaluationCost(evaluationAttemptCost(pair.Candidate, routeRequest.Facts.ImageCount), reviewerCost))
	}

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (?, 'auto', 'Auto', 1, '{}', ?, ?)
	`, runtime.ID, now, now); err != nil {
		t.Fatal(err)
	}
	store := evaluation.NewStore(db, time.Now)
	service, err := evaluation.NewService(store, evaluation.ServiceOptions{
		Sample: func(int) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := service.Submit(submitter.job); got != evaluation.SubmitAccepted {
		t.Fatalf("evaluation Submit()=%q", got)
	}
	finished := make(chan struct{})
	if got := service.Submit(evaluation.Job{
		ProfileID: runtime.ID, SampleRateBPS: 10_000,
		DailyBudgetMicroUSD:   submitter.job.DailyBudgetMicroUSD,
		EstimatedCostMicroUSD: 1, MaxConcurrency: 1, QueueCapacity: 4,
		Timeout: time.Minute, ExpiresAt: time.Now().Add(time.Minute),
		Run: func(context.Context) (evaluation.Result, error) {
			close(finished)
			return evaluation.Result{}, nil
		},
	}); got != evaluation.SubmitAccepted {
		t.Fatalf("sentinel Submit()=%q", got)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not finish")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	budget, err := store.Budget(context.Background(), runtime.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.ListEvidence(context.Background(), runtime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if visionCalls.Load() != 4 || answerCalls.Load() != 1 || reviewerCalls.Load() != 1 ||
		budget.SpentMicroUSD != actualSpent || budget.SpentMicroUSD >= worstCaseCost ||
		budget.ReservedMicroUSD != 0 ||
		len(evidence) != 1 || evidence[0].CandidateCostMicroUSD != generatedCost ||
		evidence[0].ReviewerCostMicroUSD != reviewerCost {
		t.Fatalf("vision=%d answer=%d reviewer=%d budget=%+v evidence=%+v",
			visionCalls.Load(), answerCalls.Load(), reviewerCalls.Load(), budget, evidence)
	}
}

func TestAsyncEvaluationFailureChargesVisionCallsWithoutUnsentAnswer(t *testing.T) {
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	var reviewerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case model == "vision":
			visionCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"vision overloaded"}`)
		case model == "fast" && root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
		case model == "strong" && root["system"] != nil:
			reviewerCalls.Add(1)
			http.Error(w, "reviewer must not run", http.StatusInternalServerError)
		case model == "strong":
			_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"online answer"}]}`)
		case model == "fast":
			answerCalls.Add(1)
			http.Error(w, "answer must not run", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, true)
	route := runtime.AutoRouting.Strategy.Routes["balanced"]
	route.MinQualityBPS = 9500
	runtime.AutoRouting.Strategy.Routes["balanced"] = route
	runtime.OverloadRules[0].MaxRetries = 1
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"compare this layout"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewWithEvaluation(runtime, server.Client(), nil, nil, submitter).ServeHTTP(response, request)
	if response.Code != http.StatusOK || submitter.calls != 1 {
		t.Fatalf("status=%d submissions=%d body=%q", response.Code, submitter.calls, response.Body.String())
	}

	result, err := submitter.job.Run(context.Background())
	visionCallCost := estimateEvaluationCallCost(
		profile.VisionImagePromptReserveTokens,
		runtime.Vision.MaxTokens,
		runtime.Models[runtime.Vision.Model],
	)
	if err == nil || visionCalls.Load() != 2 || answerCalls.Load() != 0 || reviewerCalls.Load() != 0 ||
		result.SpentMicroUSD != multiplyEvaluationCost(visionCallCost, 2) || result.Evidence != nil {
		t.Fatalf("err=%v vision=%d answer=%d reviewer=%d result=%+v",
			err, visionCalls.Load(), answerCalls.Load(), reviewerCalls.Load(), result)
	}
}

func TestAsyncEvaluationPreCanceledAnswerDoesNotChargeOrEnterTransport(t *testing.T) {
	var asyncCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case model == "fast" && root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
		case model == "fast":
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"online answer"}]}`)
		default:
			asyncCalls.Add(1)
			http.Error(w, "canceled evaluation reached upstream", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewWithEvaluation(runtime, server.Client(), nil, nil, submitter).ServeHTTP(response, request)
	if response.Code != http.StatusOK || submitter.calls != 1 {
		t.Fatalf("status=%d submissions=%d body=%q", response.Code, submitter.calls, response.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := submitter.job.Run(ctx)
	if !errors.Is(err, context.Canceled) || asyncCalls.Load() != 0 ||
		result.SpentMicroUSD != 0 || result.Evidence != nil {
		t.Fatalf("err=%v async_calls=%d result=%+v", err, asyncCalls.Load(), result)
	}
}

func TestAsyncEvaluationCanceledBeforeReviewerChargesOnlyAnswerTransport(t *testing.T) {
	var cancelEvaluation context.CancelFunc
	var answerCalls atomic.Int32
	var reviewerCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		responseBody := ""
		switch {
		case model == "fast" && root["system"] != nil:
			responseBody = `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`
		case model == "fast":
			responseBody = `{"model":"fast","content":[{"type":"text","text":"online answer"}]}`
		case model == "strong" && root["system"] != nil:
			reviewerCalls.Add(1)
			responseBody = `{"content":[{"type":"text","text":"{\"winner\":\"tie\",\"severe_a\":false,\"severe_b\":false}"}]}`
		case model == "strong":
			answerCalls.Add(1)
			if cancelEvaluation != nil {
				cancelEvaluation()
			}
			responseBody = `{"model":"strong","content":[{"type":"text","text":"generated answer"}]}`
		default:
			return nil, fmt.Errorf("unexpected model %q", model)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    request,
		}, nil
	})}
	runtime := autoProxyRuntime(t, "https://upstream.test", false)
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	body := `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewWithEvaluation(runtime, client, nil, nil, submitter).ServeHTTP(response, request)
	if response.Code != http.StatusOK || submitter.calls != 1 || answerCalls.Load() != 0 {
		t.Fatalf("status=%d submissions=%d answers=%d body=%q",
			response.Code, submitter.calls, answerCalls.Load(), response.Body.String())
	}
	routeRequest, err := routing.ParseAutoRequest(
		runtime.Protocol, http.MethodPost, "/v1/messages", "application/json", []byte(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := routing.NewPlanner(runtime)
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := planner.EvaluationPair(routeRequest, routing.Classification{
		TaskType: "simple", Risk: routing.RiskNormal, ConfidenceBPS: 9200,
		Source: routing.ClassificationSourceAnalyzer,
	}, "fast")
	if !ok {
		t.Fatal("evaluation pair unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelEvaluation = cancel
	result, err := submitter.job.Run(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errEvaluationCostInvariant) ||
		answerCalls.Load() != 1 || reviewerCalls.Load() != 0 ||
		result.SpentMicroUSD != pair.Reference.AnswerCallCostMicroUSD() || result.Evidence != nil {
		t.Fatalf("err=%v answer=%d reviewer=%d result=%+v",
			err, answerCalls.Load(), reviewerCalls.Load(), result)
	}
}

func TestEvaluationTransportDefinesTheChargeBoundary(t *testing.T) {
	t.Run("canceled before transport", func(t *testing.T) {
		var charges atomic.Int32
		var transportCalls atomic.Int32
		h := &handler{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, errors.New("must not enter base transport")
		})}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := h.doEvaluation(ctx, http.MethodPost, "https://upstream.test/v1/messages", nil, nil, func() error {
			charges.Add(1)
			return nil
		})
		if !errors.Is(err, context.Canceled) || charges.Load() != 0 || transportCalls.Load() != 0 {
			t.Fatalf("err=%v charges=%d transport=%d", err, charges.Load(), transportCalls.Load())
		}
	})

	t.Run("charge rejection stops base transport", func(t *testing.T) {
		chargeErr := errors.New("budget exhausted")
		var transportCalls atomic.Int32
		h := &handler{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, errors.New("must not enter base transport")
		})}}
		_, err := h.doEvaluation(
			context.Background(), http.MethodPost, "https://upstream.test/v1/messages", nil, nil,
			func() error { return chargeErr },
		)
		if !errors.Is(err, chargeErr) || transportCalls.Load() != 0 {
			t.Fatalf("err=%v transport=%d", err, transportCalls.Load())
		}
	})

	t.Run("network failure after transport entry remains charged", func(t *testing.T) {
		networkErr := errors.New("connection reset")
		var charges atomic.Int32
		var transportCalls atomic.Int32
		h := &handler{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, networkErr
		})}}
		_, err := h.doEvaluation(
			context.Background(), http.MethodPost, "https://upstream.test/v1/messages", nil, nil,
			func() error {
				charges.Add(1)
				return nil
			},
		)
		if !errors.Is(err, networkErr) || charges.Load() != 1 || transportCalls.Load() != 1 {
			t.Fatalf("err=%v charges=%d transport=%d", err, charges.Load(), transportCalls.Load())
		}
	})
}

// Break caught: an asynchronous comparison redirect can replay caller credentials to another Profile path.
func TestAsyncEvaluationReturnsRedirectWithoutCallingOtherProfile(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var redirectTargetCalls atomic.Int32
			var redirectTargetHeaders http.Header
			var redirectTargetBody []byte
			var upstream *httptest.Server
			upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/other-profile/v1/messages" {
					redirectTargetCalls.Add(1)
					redirectTargetHeaders = r.Header.Clone()
					redirectTargetBody, _ = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusOK)
					return
				}
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				switch request.Model {
				case "fast":
					_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"online answer"}]}`)
				case "strong":
					w.Header().Set("Location", upstream.URL+"/other-profile/v1/messages")
					w.WriteHeader(status)
				default:
					http.Error(w, "unexpected model", http.StatusBadRequest)
				}
			}))
			defer upstream.Close()

			runtime := autoProxyRuntime(t, upstream.URL, false)
			runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
				Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
				ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
				TaskTimeout: time.Minute,
			}
			submitter := &capturingEvaluationSubmitter{}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/messages",
				strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Api-Key", "caller-api-key")
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set("Cookie", "session=caller-cookie")
			request.Header.Set("X-Caller-Metadata", "caller-metadata")
			response := httptest.NewRecorder()

			NewWithEvaluation(runtime, upstream.Client(), nil, nil, submitter).ServeHTTP(response, request)

			if response.Code != http.StatusOK || submitter.calls != 1 {
				t.Fatalf("status=%d submissions=%d body=%q", response.Code, submitter.calls, response.Body.String())
			}
			_, err := submitter.job.Run(context.Background())
			if redirectTargetCalls.Load() != 0 {
				t.Fatalf("other Profile redirect target calls=%d headers=%v body=%q",
					redirectTargetCalls.Load(), redirectTargetHeaders, redirectTargetBody)
			}
			if err == nil {
				t.Fatal("asynchronous comparison unexpectedly succeeded after redirect")
			}
		})
	}
}

func TestFailedAsyncComparisonStillChargesItsPlannedAttemptCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"online answer"}]}`)
	}))
	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	NewWithEvaluation(runtime, server.Client(), nil, nil, submitter).ServeHTTP(response, request)
	if response.Code != http.StatusOK || submitter.calls != 1 {
		t.Fatalf("status=%d submissions=%d body=%q", response.Code, submitter.calls, response.Body.String())
	}
	server.Close()

	result, err := submitter.job.Run(context.Background())
	if err == nil || result.SpentMicroUSD <= 0 {
		t.Fatalf("err=%v spent=%d estimated=%d", err, result.SpentMicroUSD, submitter.job.EstimatedCostMicroUSD)
	}
}

func TestAsyncComparisonIsNotSubmittedWhenDisabledOrHighRisk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()
	for _, tt := range []struct {
		name    string
		enabled bool
		body    string
	}{
		{name: "disabled", body: `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`},
		{name: "high risk", enabled: true, body: `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit the file"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := autoProxyRuntime(t, server.URL, false)
			if tt.enabled {
				runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
					Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
					ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
					TaskTimeout: time.Minute,
				}
			}
			submitter := &capturingEvaluationSubmitter{}
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			NewWithEvaluation(runtime, server.Client(), nil, nil, submitter).ServeHTTP(response, request)
			if response.Code != http.StatusOK || submitter.calls != 0 {
				t.Fatalf("status=%d submissions=%d body=%q", response.Code, submitter.calls, response.Body.String())
			}
		})
	}
}

type capturingEvaluationSubmitter struct {
	job   evaluation.Job
	calls int
}

func (s *capturingEvaluationSubmitter) Submit(job evaluation.Job) evaluation.SubmitResult {
	s.calls++
	s.job = job
	return evaluation.SubmitAccepted
}

func TestAutoRoutingSessionStaysOnItsModelAndOnlyUpgrades(t *testing.T) {
	models := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(routing.SessionIDHeader) != "" {
			t.Error("internal Session header reached upstream")
		}
		var request struct {
			Model  string          `json:"model"`
			System json.RawMessage `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.System) > 0 {
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9000}"}]}`)
			return
		}
		models = append(models, request.Model)
		_, _ = io.WriteString(w, `{"model":"`+request.Model+`","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (8, 'auto', 'Auto', 1, '{}', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	sessions, err := routing.NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	runtime := autoProxyRuntime(t, server.URL, false)
	handler := NewWithSessionStore(runtime, server.Client(), nil, sessions)

	send := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Api-Key", "caller-secret")
		request.Header.Set(routing.SessionIDHeader, "agent-session-42")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
		}
		return response
	}

	send(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`)
	send(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"比较这个布局"}]}]}`)
	send(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`)

	if len(models) != 3 || models[0] != "fast" || models[1] != "strong" || models[2] != "strong" {
		t.Fatalf("models=%v", models)
	}
	var model string
	var quality int
	if err := db.QueryRow(`
		SELECT model, quality_score_bps FROM routing_session_bindings
	`).Scan(&model, &quality); err != nil {
		t.Fatal(err)
	}
	if model != "strong" || quality != 9900 {
		t.Fatalf("binding model=%q quality=%d", model, quality)
	}
}

func TestAutoRoutingTransientModelSwitchDoesNotChangeSessionBinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Model == "fast" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (8, 'auto', 'Auto', 1, '{}', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	sessions, err := routing.NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Content-Type": {"application/json"},
		"X-Api-Key":    {"caller-secret"},
	}
	key, ok := sessions.Key(headers, "agent-session-42", 8, "balanced", routing.SessionPurposeLLM)
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}
	if err := sessions.Bind(context.Background(), key, routing.SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: routing.SessionPurposeLLM,
		Model: "fast", QualityScoreBPS: 9200, Strategy: "20260802-001",
	}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.OverloadRules[0].MaxRetries = 0
	handler := NewWithSessionStore(runtime, server.Client(), nil, sessions)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header = headers.Clone()
	request.Header.Set(routing.SessionIDHeader, "agent-session-42")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}

	binding, found, err := sessions.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "fast" {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestAutoRoutingSwitchesToOrderedBackupTargetBeforeChangingModel(t *testing.T) {
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"overloaded"}`)
	}))
	defer primary.Close()
	var backupCalls atomic.Int32
	var backupModel string
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		backupModel = body.Model
		_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"backup answer"}]}`)
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, false)
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast"},
	})
	runtime.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	runtime.AutoRouting.Strategy.Budget.MaxTargetSwitches = 1
	runtime.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	runtime.AutoRouting.DynamicOptimization = profile.DynamicOptimizationRuntime{
		Enabled: true, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100_000,
		ReviewerModel: "strong", MaxConcurrency: 1, QueueCapacity: 4,
		TaskTimeout: time.Minute,
	}
	submitter := &capturingEvaluationSubmitter{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	NewWithEvaluation(runtime, primary.Client(), nil, nil, submitter).ServeHTTP(response, request)

	if response.Code != http.StatusOK || primaryCalls.Load() != 1 ||
		backupCalls.Load() != 1 || backupModel != "fast" ||
		!strings.Contains(response.Body.String(), "backup answer") {
		t.Fatalf("status=%d primary=%d backup=%d model=%q body=%q",
			response.Code, primaryCalls.Load(), backupCalls.Load(), backupModel, response.Body.String())
	}
	if submitter.calls != 1 {
		t.Fatalf("submissions=%d", submitter.calls)
	}
	if _, err := submitter.job.Run(context.Background()); err == nil {
		t.Fatal("comparison unexpectedly succeeded through a Target that does not serve its model")
	}
	if backupCalls.Load() != 1 {
		t.Fatalf("comparison sent another model to the fast-only backup: calls=%d", backupCalls.Load())
	}
}

func TestExplicitModelDoesNotUseAutoBackupTargets(t *testing.T) {
	var backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backup.Close()
	runtime := autoProxyRuntime(t, primary.URL, false)
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast"},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"fast","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || backupCalls.Load() != 0 {
		t.Fatalf("status=%d backup=%d body=%q", response.Code, backupCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingDoesNotSwitchTargetForNonRetryableResponse(t *testing.T) {
	var backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid request"}`)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, false)
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast"},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || backupCalls.Load() != 0 {
		t.Fatalf("status=%d backup=%d body=%q", response.Code, backupCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingIgnoresInvalidHardFailureRetryRule(t *testing.T) {
	var primaryCalls atomic.Int32
	var backupCalls atomic.Int32
	const upstreamBody = `{"error":"invalid credential","private":"do-not-log"}`
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, false)
	runtime.OverloadRules = []provider.Rule{{
		Status: http.StatusUnauthorized, MaxRetries: 1,
	}}
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast"},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || response.Body.String() != upstreamBody ||
		primaryCalls.Load() != 1 || backupCalls.Load() != 0 {
		t.Fatalf("status=%d primary=%d backup=%d body=%q",
			response.Code, primaryCalls.Load(), backupCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingExplicitModelNeverCallsRouter(t *testing.T) {
	var calls atomic.Int32
	var bodyReceived []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		bodyReceived, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"explicit","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	body := `{"model":"explicit","max_tokens":1000,"messages":[{"role":"user","content":"比较架构"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(autoProxyRuntime(t, server.URL, false), server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || calls.Load() != 1 || string(bodyReceived) != body {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), bodyReceived)
	}
}

func TestAutoRoutingRejectsUnsupportedOperationBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"auto","input":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(autoProxyRuntime(t, server.URL, false), server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAutoRoutingDisabledRejectsReservedAutoModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	runtime := resolvedRuntime(t, 1, "plain", profile.ProtocolAnthropic, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto","messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAutoRoutingHighRiskUsesBaselineWithoutAnalyzer(t *testing.T) {
	var analyzerCalls atomic.Int32
	var answerModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
		}
		_ = json.Unmarshal(root["model"], &answerModel)
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit the file"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(autoProxyRuntime(t, server.URL, false), server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || analyzerCalls.Load() != 0 || answerModel != "strong" {
		t.Fatalf("status=%d analyzer=%d model=%q body=%q", response.Code, analyzerCalls.Load(), answerModel, response.Body.String())
	}
}

func TestAutoRoutingRetryBudgetPreventsExtraFinalRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"overloaded"}`)
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.OverloadRules[0].MaxRetries = 5
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var rows []routing.CallLedgerEntry
	var snapshot routing.AttemptBudgetSnapshot
	proxyHandler.callLedgerSink = func(got []routing.CallLedgerEntry) { rows = got }
	proxyHandler.budgetSnapshotSink = func(got routing.AttemptBudgetSnapshot) { snapshot = got }
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	proxyHandler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
	if len(rows) != 2 || rows[0].Kind != routing.CallAnswer || rows[0].RetryIndex != 0 ||
		rows[1].Kind != routing.CallAnswer || rows[1].RetryIndex != 1 ||
		snapshot.AnswerAttempts != 2 || snapshot.TotalOutboundCalls != 2 ||
		snapshot.RetriesByTarget[profile.PrimaryTargetID] != 1 {
		t.Fatalf("rows=%+v snapshot=%+v", rows, snapshot)
	}
}

func TestAutoRoutingDoesNotRecoverHardBudgetDeadlineFailure(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, provider.NewFailure(
			provider.FailureBudgetDeadline,
			0,
			routing.ErrAttemptBudgetExceeded,
		)
	})}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(autoProxyRuntime(t, "https://upstream.test", false), client, nil).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAutoRoutingRecoversNetworkTimeoutBeforeClientCommit(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"fast","content":[{"type":"text","text":"done"}]}`)),
			Request:    request,
		}, nil
	})}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(autoProxyRuntime(t, "https://upstream.test", false), client, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || calls.Load() != 2 || !strings.Contains(response.Body.String(), "done") {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAutoRoutingSwitchesToPlannedStrongModelBeforeClientCommit(t *testing.T) {
	var calls atomic.Int32
	models := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		models <- request.Model
		calls.Add(1)
		if request.Model == "fast" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.OverloadRules[0].MaxRetries = 0
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	close(models)
	gotModels := make([]string, 0, 2)
	for model := range models {
		gotModels = append(gotModels, model)
	}
	if response.Code != http.StatusOK || calls.Load() != 2 ||
		len(gotModels) != 2 || gotModels[0] != "fast" || gotModels[1] != "strong" {
		t.Fatalf("status=%d calls=%d models=%v body=%q", response.Code, calls.Load(), gotModels, response.Body.String())
	}
}

func TestAutoRoutingReplansCompositeVisionForSwitchedModel(t *testing.T) {
	var analyzerCalls atomic.Int32
	var visionCalls atomic.Int32
	var fastCalls atomic.Int32
	var strongCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		switch model {
		case "vision":
			visionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"界面中按钮发生重叠"}]}`)
		case "fast":
			fastCalls.Add(1)
			if !bytes.Contains(body, []byte(`"type":"image"`)) {
				t.Errorf("native fast request lost image: %s", body)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
		case "strong":
			strongCalls.Add(1)
			if bytes.Contains(body, []byte(`"type":"image"`)) ||
				!bytes.Contains(body, []byte("界面中按钮发生重叠")) {
				t.Errorf("composite strong request=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, true)
	fast := runtime.Models["fast"]
	fast.SupportsVision = true
	runtime.Models["fast"] = fast
	strong := runtime.Models["strong"]
	strong.SupportsVision = false
	runtime.Models["strong"] = strong
	runtime.OverloadRules[0].MaxRetries = 0
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"比较界面布局"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || analyzerCalls.Load() != 1 ||
		visionCalls.Load() != 1 || fastCalls.Load() != 1 || strongCalls.Load() != 1 {
		t.Fatalf("status=%d analyzer=%d vision=%d fast=%d strong=%d body=%q",
			response.Code, analyzerCalls.Load(), visionCalls.Load(), fastCalls.Load(), strongCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingFallsBackTargetAfterTransientVisionFailureBeforeAnswer(t *testing.T) {
	var primaryAnalyzerCalls atomic.Int32
	var primaryVisionCalls atomic.Int32
	var primaryAnswerCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			primaryAnalyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		if model == "vision" {
			primaryVisionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[]}`)
			return
		}
		primaryAnswerCalls.Add(1)
		http.Error(w, "answer must not use primary", http.StatusInternalServerError)
	}))
	defer primary.Close()

	var backupVisionCalls atomic.Int32
	var backupAnswerCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		switch request.Model {
		case "vision":
			backupVisionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"backup visual evidence"}]}`)
		case "fast":
			backupAnswerCalls.Add(1)
			if bytes.Contains(body, []byte(`"type":"image"`)) ||
				!bytes.Contains(body, []byte("backup visual evidence")) {
				t.Errorf("backup answer body=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"backup answer"}]}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, true)
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast", "vision"},
	})
	runtime.OverloadRules[0].MaxRetries = 2
	runtime.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 3
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 7
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 10
	runtime.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 2
	runtime.AutoRouting.Strategy.Budget.MaxTargetSwitches = 1
	proxyHandler := New(runtime, primary.Client(), nil).(*handler)
	var rows []routing.CallLedgerEntry
	var snapshot routing.AttemptBudgetSnapshot
	proxyHandler.callLedgerSink = func(got []routing.CallLedgerEntry) { rows = got }
	proxyHandler.budgetSnapshotSink = func(got routing.AttemptBudgetSnapshot) { snapshot = got }
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"比较这个界面布局"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	proxyHandler.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		primaryAnalyzerCalls.Load() != 1 || primaryVisionCalls.Load() != 1 ||
		primaryAnswerCalls.Load() != 0 || backupVisionCalls.Load() != 1 ||
		backupAnswerCalls.Load() != 1 || !strings.Contains(response.Body.String(), "backup answer") {
		t.Fatalf(
			"status=%d primary(analyzer=%d vision=%d answer=%d) backup(vision=%d answer=%d) body=%q",
			response.Code,
			primaryAnalyzerCalls.Load(), primaryVisionCalls.Load(), primaryAnswerCalls.Load(),
			backupVisionCalls.Load(), backupAnswerCalls.Load(), response.Body.String(),
		)
	}
	if len(rows) != 4 ||
		rows[0].Sequence != 1 || rows[0].Kind != routing.CallAnalyzer ||
		rows[0].Target != profile.PrimaryTargetID || rows[0].Outcome != "success" ||
		rows[1].Sequence != 2 || rows[1].Kind != routing.CallVision ||
		rows[1].Target != profile.PrimaryTargetID || rows[1].RetryIndex != 0 ||
		rows[1].Outcome != "malformed" ||
		rows[2].Sequence != 3 || rows[2].Kind != routing.CallVision ||
		rows[2].Target != "region_b" || rows[2].RetryIndex != 0 ||
		rows[2].Outcome != "success" ||
		rows[3].Sequence != 4 || rows[3].Kind != routing.CallAnswer ||
		rows[3].Target != "region_b" || rows[3].RetryIndex != 0 ||
		rows[3].Outcome != "success" {
		t.Fatalf("ledger=%+v", rows)
	}
	if snapshot.AnswerAttempts != 1 || snapshot.AuxiliaryCalls != 3 ||
		snapshot.TotalOutboundCalls != 4 || snapshot.TargetSwitches != 1 ||
		snapshot.HeldAnswerAttempts != 0 || snapshot.HeldAuxiliaryCalls != 0 ||
		snapshot.HeldOutboundCalls != 0 || snapshot.HeldCostMicroUSD != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAutoRoutingParallelVisionHardFailurePreventsFallback(t *testing.T) {
	secondStarted := make(chan struct{})
	transientFinished := make(chan struct{})
	var analyzerCalls atomic.Int32
	var primaryVisionCalls atomic.Int32
	var primaryAnswerCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		if model != "vision" {
			primaryAnswerCalls.Add(1)
			http.Error(w, "answer must not run", http.StatusInternalServerError)
			return
		}
		primaryVisionCalls.Add(1)
		switch {
		case bytes.Contains(body, []byte("transient-image")):
			<-secondStarted
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"vision overloaded"}`)
			close(transientFinished)
		case bytes.Contains(body, []byte("auth-image")):
			close(secondStarted)
			<-transientFinished
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid credential"}`)
		default:
			http.Error(w, "unexpected image", http.StatusBadRequest)
		}
	}))
	defer primary.Close()

	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Model == "vision" {
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"backup evidence"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"backup answer"}]}`)
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, true)
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"fast", "strong", "vision"},
	})
	runtime.OverloadRules[0].MaxRetries = 0
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 6
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 10
	runtime.AutoRouting.Strategy.Budget.MaxTargetSwitches = 1
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/transient-image.png"}},{"type":"image","source":{"type":"url","url":"https://example.test/auth-image.png"}},{"type":"text","text":"比较这两张图片"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway || primaryVisionCalls.Load() != 2 ||
		primaryAnswerCalls.Load() != 0 || backupCalls.Load() != 0 {
		t.Fatalf(
			"status=%d analyzer=%d primary_vision=%d primary_answer=%d backup=%d body=%q",
			response.Code, analyzerCalls.Load(), primaryVisionCalls.Load(),
			primaryAnswerCalls.Load(), backupCalls.Load(), response.Body.String(),
		)
	}
}

func TestAutoRoutingFallsBackTargetWhilePreparingSwitchedModel(t *testing.T) {
	var analyzerCalls atomic.Int32
	var fastAnswerCalls atomic.Int32
	var primaryVisionCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		switch model {
		case "fast":
			fastAnswerCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"answer overloaded"}`)
		case "vision":
			primaryVisionCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"vision overloaded"}`)
		default:
			http.Error(w, "unexpected primary model", http.StatusBadRequest)
		}
	}))
	defer primary.Close()

	var backupVisionCalls atomic.Int32
	var strongAnswerCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		switch request.Model {
		case "vision":
			backupVisionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"switched visual evidence"}]}`)
		case "strong":
			strongAnswerCalls.Add(1)
			if !bytes.Contains(body, []byte("switched visual evidence")) {
				t.Errorf("strong answer body=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"strong backup answer"}]}`)
		default:
			http.Error(w, "unexpected backup model", http.StatusBadRequest)
		}
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, true)
	fast := runtime.Models["fast"]
	fast.SupportsVision = true
	runtime.Models["fast"] = fast
	strong := runtime.Models["strong"]
	strong.SupportsVision = false
	runtime.Models["strong"] = strong
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"strong", "vision"},
	})
	runtime.OverloadRules[0].MaxRetries = 0
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 3
	runtime.AutoRouting.Strategy.Budget.MaxTargetSwitches = 1
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"比较这个界面布局"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || analyzerCalls.Load() != 1 ||
		fastAnswerCalls.Load() != 1 || primaryVisionCalls.Load() != 1 ||
		backupVisionCalls.Load() != 1 || strongAnswerCalls.Load() != 1 ||
		!strings.Contains(response.Body.String(), "strong backup answer") {
		t.Fatalf(
			"status=%d analyzer=%d fast=%d primary_vision=%d backup_vision=%d strong=%d body=%q",
			response.Code, analyzerCalls.Load(), fastAnswerCalls.Load(),
			primaryVisionCalls.Load(), backupVisionCalls.Load(), strongAnswerCalls.Load(),
			response.Body.String(),
		)
	}
}

func TestAutoRoutingRejectsCompositeNodeBeforeVisionWhenAnswerCannotFit(t *testing.T) {
	var analyzerCalls atomic.Int32
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		if model == "vision" {
			visionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"must not run"}]}`)
			return
		}
		answerCalls.Add(1)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"must not answer"}]}`)
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, true)
	strong := runtime.Models["strong"]
	strong.SupportsVision = false
	runtime.Models["strong"] = strong
	runtime.AutoRouting.Strategy.Budget.MaxWorstCaseCostMicroUSD = 2_700
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"比较这个布局并解释取舍"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || analyzerCalls.Load() != 1 ||
		visionCalls.Load() != 0 || answerCalls.Load() != 0 {
		t.Fatalf("status=%d analyzer=%d vision=%d answer=%d body=%q",
			response.Code, analyzerCalls.Load(), visionCalls.Load(), answerCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingDoesNotEnterBackupAfterFinalAnswerSlotWasConsumed(t *testing.T) {
	var analyzerCalls atomic.Int32
	var visionCalls atomic.Int32
	var fastAnswerCalls atomic.Int32
	var strongCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		if _, analyzer := root["system"]; analyzer {
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
			return
		}
		switch model {
		case "vision":
			visionCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"visual evidence"}]}`)
		case "fast":
			fastAnswerCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"answer overloaded"}`)
		default:
			strongCalls.Add(1)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"must not run"}]}`)
		}
	}))
	defer primary.Close()

	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"must not run"}]}`)
	}))
	defer backup.Close()

	runtime := autoProxyRuntime(t, primary.URL, true)
	fast := runtime.Models["fast"]
	fast.SupportsVision = false
	runtime.Models["fast"] = fast
	strong := runtime.Models["strong"]
	strong.SupportsVision = false
	runtime.Models["strong"] = strong
	runtime.Targets = append(runtime.Targets, profile.TargetRuntime{
		ID: "region_b", Upstream: backup.URL, Models: []string{"strong", "vision"},
	})
	runtime.OverloadRules[0].MaxRetries = 0
	runtime.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 1
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 4
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 5
	runtime.AutoRouting.Strategy.Budget.MaxRetriesPerTarget = 0
	runtime.AutoRouting.Strategy.Budget.MaxTargetSwitches = 1
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},{"type":"text","text":"比较这个布局并解释取舍"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, primary.Client(), nil).ServeHTTP(response, request)

	if analyzerCalls.Load() != 1 || visionCalls.Load() != 1 ||
		fastAnswerCalls.Load() != 1 || strongCalls.Load() != 0 || backupCalls.Load() != 0 {
		t.Fatalf("status=%d analyzer=%d vision=%d fast=%d strong=%d backup=%d body=%q",
			response.Code, analyzerCalls.Load(), visionCalls.Load(), fastAnswerCalls.Load(),
			strongCalls.Load(), backupCalls.Load(), response.Body.String())
	}
}

func TestAutoRoutingPersistsStructuredRouteTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Model == "fast" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (8, 'auto', 'Auto', 1, '{}', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	store := stats.New(db)

	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.OverloadRules[0].MaxRetries = 0
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(runtime, server.Client(), store).ServeHTTP(response, request)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var strategy, route, source, initialModel, finalModel, visionMode string
	var statusCode, committed, attempts, switches, totalCalls int
	if err := db.QueryRow(`
		SELECT strategy_name, route_id, classification_source,
		       initial_model, final_model, vision_mode, status_code,
		       client_committed, answer_attempts, model_switches,
		       total_outbound_calls
		FROM routing_traces
	`).Scan(
		&strategy, &route, &source, &initialModel, &finalModel,
		&visionMode, &statusCode, &committed, &attempts, &switches,
		&totalCalls,
	); err != nil {
		t.Fatal(err)
	}
	if strategy != "20260802-001" || route != "balanced" || source != "rule" ||
		initialModel != "fast" || finalModel != "strong" || visionMode != "none" ||
		statusCode != http.StatusOK || committed != 1 || attempts != 2 ||
		switches != 1 || totalCalls != 2 {
		t.Fatalf("trace=%q %q %q %q %q %q %d %d %d %d %d",
			strategy, route, source, initialModel, finalModel, visionMode,
			statusCode, committed, attempts, switches, totalCalls)
	}
}

func TestAutoRoutingPersistsAnalyzerWhenPlanIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}],"usage":{"input_tokens":12,"output_tokens":5}}`)
	}))
	defer server.Close()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := stats.New(db)
	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 1
	runtime.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 1
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 1
	request := httptest.NewRequest(
		http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两个分布式方案并说明取舍"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(runtime, server.Client(), store).ServeHTTP(response, request)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var traceID int64
	var strategy string
	if err := db.QueryRow(`SELECT id, strategy_name FROM routing_traces`).Scan(&traceID, &strategy); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryRoutingCalls(context.Background(), stats.RoutingCallFilter{TraceID: &traceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if response.Code == http.StatusOK || strategy != "" || len(rows) != 1 ||
		rows[0].Kind != string(routing.CallAnalyzer) || !rows[0].ActualCostKnown {
		t.Fatalf("status=%d strategy=%q calls=%+v body=%q", response.Code, strategy, rows, response.Body.String())
	}
}

func TestAutoRoutingPersistsEveryRetriedPhysicalCallAndRouteAggregate(t *testing.T) {
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}],"usage":{"input_tokens":12,"output_tokens":5}}`)
		case model == "vision":
			if visionCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"overloaded"}`)
				return
			}
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"visual evidence"}],"usage":{"input_tokens":9,"output_tokens":3}}`)
		default:
			if answerCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"overloaded"}`)
				return
			}
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":20,"output_tokens":4}}`)
		}
	}))
	defer server.Close()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := stats.New(db)
	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.OverloadRules[0].MaxRetries = 1
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 3
	runtime.AutoRouting.Strategy.Budget.MaxAnswerAttempts = 2
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 5
	request := httptest.NewRequest(
		http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/private.png"}},{"type":"text","text":"比较布局取舍"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(runtime, server.Client(), store).ServeHTTP(response, request)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var traceID int64
	var correlation string
	var consumed, held, knownActual int64
	var allKnown, totalCalls int
	if err := db.QueryRow(`
		SELECT id, correlation_id, consumed_estimated_cost_micro_usd,
		       held_cost_micro_usd, known_actual_cost_micro_usd,
		       all_actual_costs_known, total_outbound_calls
		FROM routing_traces
	`).Scan(&traceID, &correlation, &consumed, &held, &knownActual, &allKnown, &totalCalls); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryRoutingCalls(context.Background(), stats.RoutingCallFilter{TraceID: &traceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"analyzer", "vision", "vision", "answer", "answer"}
	wantRetries := []int{0, 0, 1, 0, 1}
	var wantConsumed, wantKnownActual int64
	for _, row := range rows {
		wantConsumed += row.EstimatedMicroUSD
		if row.ActualCostKnown {
			wantKnownActual += row.ActualMicroUSD
		}
	}
	if response.Code != http.StatusOK || correlation == "" || len(rows) != len(wantKinds) ||
		consumed != wantConsumed || held != 0 || knownActual != wantKnownActual ||
		allKnown != 0 || totalCalls != len(rows) {
		t.Fatalf("status=%d correlation=%q consumed=%d/%d held=%d known=%d/%d all=%d total=%d rows=%+v",
			response.Code, correlation, consumed, wantConsumed, held,
			knownActual, wantKnownActual, allKnown, totalCalls, rows)
	}
	for index, row := range rows {
		if row.Sequence != index+1 || row.Kind != wantKinds[index] || row.RetryIndex != wantRetries[index] ||
			row.CorrelationID != correlation {
			t.Fatalf("row[%d]=%+v", index, row)
		}
	}
	if rows[1].ActualCostKnown || !rows[2].ActualCostKnown ||
		rows[3].ActualCostKnown || !rows[4].ActualCostKnown {
		t.Fatalf("actual flags=%+v", rows)
	}
}

func TestOpenAIChatAutoRoutingUsesCompositeVisionAfterModelSelection(t *testing.T) {
	var analyzerCalls atomic.Int32
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var root struct {
			Model    string `json:"model"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &root)
		switch {
		case root.Model == "vision":
			visionCalls.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"按钮与输入框重叠"}}],"usage":{"prompt_tokens":8,"completion_tokens":4}}`)
		case len(root.Messages) > 0 && root.Messages[0].Role == "system":
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9000}"}}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`)
		default:
			answerCalls.Add(1)
			if root.Model != "fast" || bytes.Contains(body, []byte(`"type":"image_url"`)) ||
				!bytes.Contains(body, []byte("按钮与输入框重叠")) {
				t.Errorf("main body=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"fast","choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":20,"completion_tokens":2}}`)
		}
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.Protocol = profile.ProtocolOpenAI
	runtime.Vision.Transport = profile.VisionTransportOpenAIChatCompletions
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 3
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"auto","max_completion_tokens":1000,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"比较这两个布局方案"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var ledger []routing.CallLedgerEntry
	proxyHandler.callLedgerSink = func(rows []routing.CallLedgerEntry) { ledger = rows }
	proxyHandler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || analyzerCalls.Load() != 1 ||
		visionCalls.Load() != 1 || answerCalls.Load() != 1 {
		t.Fatalf("status=%d analyzer=%d vision=%d answer=%d body=%q",
			response.Code, analyzerCalls.Load(), visionCalls.Load(), answerCalls.Load(), response.Body.String())
	}
	if len(ledger) != 3 || !ledger[0].ActualCostKnown ||
		!ledger[1].ActualCostKnown || !ledger[2].ActualCostKnown {
		t.Fatalf("ledger=%+v", ledger)
	}
}

func TestOpenAIResponsesAutoRoutingRecordsStreamingUsageActual(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: response.created\n"+
				`data: {"type":"response.created","response":{"model":"fast","usage":null}}`+"\n\n"+
				"event: response.completed\n"+
				`data: {"type":"response.completed","response":{"model":"fast","usage":{"input_tokens":31,"output_tokens":19}}}`+"\n\n",
		)
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.Protocol = profile.ProtocolOpenAI
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var ledger []routing.CallLedgerEntry
	proxyHandler.callLedgerSink = func(rows []routing.CallLedgerEntry) { ledger = rows }
	request := httptest.NewRequest(
		http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"auto","max_output_tokens":1000,"stream":true,"input":"你好，请简单回答"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	proxyHandler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(ledger) != 1 ||
		ledger[0].Kind != routing.CallAnswer || !ledger[0].ActualCostKnown ||
		ledger[0].ActualMicroUSD <= 0 {
		t.Fatalf("status=%d ledger=%+v body=%q", response.Code, ledger, response.Body.String())
	}
}

func TestAutoRoutingCallLedgerMatchesPhysicalCallSequenceWithoutContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}],"usage":{"input_tokens":12,"output_tokens":6}}`)
		case model == "vision":
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"private visual description"}],"usage":{"input_tokens":8,"output_tokens":4}}`)
		default:
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"private answer"}],"usage":{"input_tokens":20,"output_tokens":3}}`)
		}
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.OverloadRules[0].MaxRetries = 0
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 2
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var rows []routing.CallLedgerEntry
	proxyHandler.callLedgerSink = func(snapshot []routing.CallLedgerEntry) {
		rows = snapshot
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/private-image.png"}},{"type":"text","text":"private prompt"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	proxyHandler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(rows) != 3 {
		t.Fatalf("status=%d rows=%+v body=%q", response.Code, rows, response.Body.String())
	}
	wantKinds := []routing.CallKind{routing.CallAnalyzer, routing.CallVision, routing.CallAnswer}
	for index, row := range rows {
		if row.Sequence != index+1 || row.Kind != wantKinds[index] ||
			row.EstimatedMicroUSD <= 0 || !row.ActualCostKnown || row.ActualMicroUSD <= 0 ||
			row.StatusCode != http.StatusOK || row.Outcome != "success" ||
			row.CorrelationID == "" {
			t.Fatalf("row[%d]=%+v", index, row)
		}
	}
	serialized := fmt.Sprintf("%+v", rows)
	for _, secret := range []string{"private prompt", "private-image", "private visual description", "private answer"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("ledger leaked %q: %s", secret, serialized)
		}
	}
}

func TestAutoVisionUsesLedgerCorrelationAsRequestTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
		case model == "vision":
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"visual evidence"}]}`)
		default:
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"done"}]}`)
		}
	}))
	defer server.Close()

	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 4
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 7
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var rows []routing.CallLedgerEntry
	proxyHandler.callLedgerSink = func(snapshot []routing.CallLedgerEntry) { rows = snapshot }
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/trace.png"}},{"type":"text","text":"比较这个界面布局"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	proxyHandler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(rows) == 0 {
		t.Fatalf("status=%d rows=%+v body=%q", response.Code, rows, response.Body.String())
	}

	correlation := rows[0].CorrelationID
	var cacheRecord map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "vision.image.cache" {
			cacheRecord = record
			break
		}
	}
	if correlation == "" || cacheRecord["request_trace_id"] != correlation ||
		cacheRecord["owner_trace_id"] != correlation || cacheRecord["call_id"] == "" {
		t.Fatalf("correlation=%q cache trace metadata=%v", correlation, cacheRecord)
	}
}

func TestAutoVisionSuccessfulSharingChargesOnlyPhysicalOwner(t *testing.T) {
	visionStarted := make(chan struct{})
	releaseVision := make(chan struct{})
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Model == "vision" {
			if visionCalls.Add(1) == 1 {
				close(visionStarted)
			}
			<-releaseVision
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"shared visual evidence"}]}`)
			return
		}
		answerCalls.Add(1)
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	runtime := autoCompositeSharingRuntime(t, server.URL)
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	ledgers := make(chan []routing.CallLedgerEntry, 2)
	proxyHandler.callLedgerSink = func(rows []routing.CallLedgerEntry) { ledgers <- rows }
	logs := newWaiterJoinedLogBuffer()
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })
	body := autoCompositeSharingBody()
	serve := func(result chan<- *httptest.ResponseRecorder) {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		proxyHandler.ServeHTTP(response, request)
		result <- response
	}
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	secondResult := make(chan *httptest.ResponseRecorder, 1)
	go serve(firstResult)
	<-visionStarted
	go serve(secondResult)
	select {
	case <-logs.waiterJoined:
	case <-time.After(time.Second):
		close(releaseVision)
		t.Fatal("second request did not reach the shared vision flight")
	}
	if visionCalls.Load() != 1 {
		close(releaseVision)
		t.Fatalf("physical vision calls before release=%d, want 1", visionCalls.Load())
	}
	close(releaseVision)
	for _, result := range []<-chan *httptest.ResponseRecorder{firstResult, secondResult} {
		response := <-result
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
		}
	}

	firstLedger, secondLedger := <-ledgers, <-ledgers
	visionRows := 0
	answerRows := 0
	correlations := map[string]bool{}
	visionSucceeded := false
	for _, rows := range [][]routing.CallLedgerEntry{firstLedger, secondLedger} {
		for _, row := range rows {
			if row.CorrelationID == "" {
				t.Fatalf("ledger row has empty correlation: %+v", row)
			}
			correlations[row.CorrelationID] = true
			switch row.Kind {
			case routing.CallVision:
				visionRows++
				visionSucceeded = row.Outcome == "success"
			case routing.CallAnswer:
				answerRows++
			}
		}
	}
	if visionCalls.Load() != 1 || answerCalls.Load() != 2 || visionRows != 1 ||
		answerRows != 2 || len(correlations) != 2 || !visionSucceeded {
		t.Fatalf("physical=%d/%d ledger=%+v %+v correlations=%v",
			visionCalls.Load(), answerCalls.Load(), firstLedger, secondLedger, correlations)
	}
	cacheRecords := visionCacheLogRecords(t, logs.String())
	if len(cacheRecords) != 2 || cacheRecords[0]["call_id"] == "" ||
		cacheRecords[0]["call_id"] != cacheRecords[1]["call_id"] ||
		cacheRecords[0]["owner_trace_id"] != cacheRecords[1]["owner_trace_id"] ||
		cacheRecords[0]["request_trace_id"] == cacheRecords[1]["request_trace_id"] {
		t.Fatalf("shared cache trace metadata=%v", cacheRecords)
	}
	for _, record := range cacheRecords {
		traceID, _ := record["request_trace_id"].(string)
		if !correlations[traceID] {
			t.Fatalf("request trace %q is not a ledger correlation: %v", traceID, correlations)
		}
	}
}

func TestAutoVisionCanceledOwnerReplacementChargesBothOwners(t *testing.T) {
	firstVisionStarted := make(chan struct{})
	firstVisionCanceled := make(chan struct{})
	replacementStarted := make(chan struct{})
	var visionCalls atomic.Int32
	var answerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Model == "vision" {
			if visionCalls.Add(1) == 1 {
				close(firstVisionStarted)
				<-r.Context().Done()
				close(firstVisionCanceled)
				return
			}
			close(replacementStarted)
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"replacement visual evidence"}]}`)
			return
		}
		answerCalls.Add(1)
		_, _ = io.WriteString(w, `{"model":"strong","content":[{"type":"text","text":"done"}]}`)
	}))
	defer server.Close()

	runtime := autoCompositeSharingRuntime(t, server.URL)
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	ledgers := make(chan []routing.CallLedgerEntry, 2)
	proxyHandler.callLedgerSink = func(rows []routing.CallLedgerEntry) { ledgers <- rows }
	logs := newWaiterJoinedLogBuffer()
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })
	body := autoCompositeSharingBody()
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	serve := func(ctx context.Context, result chan<- *httptest.ResponseRecorder) {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		proxyHandler.ServeHTTP(response, request)
		result <- response
	}
	ownerResult := make(chan *httptest.ResponseRecorder, 1)
	waiterResult := make(chan *httptest.ResponseRecorder, 1)
	go serve(ownerCtx, ownerResult)
	<-firstVisionStarted
	go serve(context.Background(), waiterResult)
	select {
	case <-logs.waiterJoined:
	case <-time.After(time.Second):
		cancelOwner()
		t.Fatal("second request did not reach the shared vision flight")
	}
	cancelOwner()
	select {
	case <-firstVisionCanceled:
	case <-time.After(time.Second):
		t.Fatal("owner physical call did not observe request cancellation")
	}
	select {
	case <-replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("waiter did not become replacement physical owner")
	}
	<-ownerResult
	response := <-waiterResult
	if response.Code != http.StatusOK {
		t.Fatalf("replacement status=%d body=%q", response.Code, response.Body.String())
	}

	firstLedger, secondLedger := <-ledgers, <-ledgers
	visionRows := make([]routing.CallLedgerEntry, 0, 2)
	answerRows := 0
	correlations := map[string]bool{}
	visionOutcomes := map[string]bool{}
	for _, rows := range [][]routing.CallLedgerEntry{firstLedger, secondLedger} {
		for _, row := range rows {
			if row.CorrelationID == "" {
				t.Fatalf("ledger row has empty correlation: %+v", row)
			}
			correlations[row.CorrelationID] = true
			if row.Kind == routing.CallVision {
				visionRows = append(visionRows, row)
				visionOutcomes[row.Outcome] = true
			}
			if row.Kind == routing.CallAnswer {
				answerRows++
			}
		}
	}
	if visionCalls.Load() != 2 || answerCalls.Load() != 1 || len(visionRows) != 2 ||
		answerRows != 1 || len(correlations) != 2 ||
		visionRows[0].CorrelationID == visionRows[1].CorrelationID ||
		!visionOutcomes["canceled"] || !visionOutcomes["success"] {
		t.Fatalf("physical=%d/%d vision=%+v ledger=%+v %+v correlations=%v",
			visionCalls.Load(), answerCalls.Load(), visionRows, firstLedger, secondLedger, correlations)
	}
	cacheRecords := visionCacheLogRecords(t, logs.String())
	if len(cacheRecords) != 2 || cacheRecords[0]["call_id"] == "" ||
		cacheRecords[1]["call_id"] == "" || cacheRecords[0]["call_id"] == cacheRecords[1]["call_id"] {
		t.Fatalf("replacement cache trace metadata=%v", cacheRecords)
	}
	for _, record := range cacheRecords {
		traceID, _ := record["request_trace_id"].(string)
		if traceID == "" || record["owner_trace_id"] != traceID || !correlations[traceID] {
			t.Fatalf("replacement trace metadata=%v correlations=%v", record, correlations)
		}
	}
}

type waiterJoinedLogBuffer struct {
	mu           sync.Mutex
	buffer       bytes.Buffer
	waiterJoined chan struct{}
	once         sync.Once
}

func newWaiterJoinedLogBuffer() *waiterJoinedLogBuffer {
	return &waiterJoinedLogBuffer{waiterJoined: make(chan struct{})}
}

func (b *waiterJoinedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buffer.Write(data)
	joined := bytes.Contains(b.buffer.Bytes(), []byte(`"msg":"vision.cache.waiter_joined"`))
	b.mu.Unlock()
	if joined {
		b.once.Do(func() { close(b.waiterJoined) })
	}
	return n, err
}

func (b *waiterJoinedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func visionCacheLogRecords(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "vision.image.cache" {
			records = append(records, record)
		}
	}
	return records
}

func autoCompositeSharingRuntime(t *testing.T, upstream string) profile.Runtime {
	runtime := autoProxyRuntime(t, upstream, true)
	strong := runtime.Models["strong"]
	strong.SupportsVision = false
	runtime.Models["strong"] = strong
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 3
	runtime.AutoRouting.Strategy.Budget.MaxTotalOutboundCalls = 5
	return runtime
}

func autoCompositeSharingBody() string {
	return `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/shared.png"}},{"type":"text","text":"edit the file"}]}]}`
}

func TestAutoRoutingVisionCacheHitConsumesOnlyAnalyzerAndAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var root map[string]json.RawMessage
		_ = json.Unmarshal(body, &root)
		var model string
		_ = json.Unmarshal(root["model"], &model)
		switch {
		case root["system"] != nil:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9200}"}]}`)
		case model == "vision":
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"cached visual evidence"}]}`)
		default:
			_, _ = io.WriteString(w, `{"model":"fast","content":[{"type":"text","text":"answer"}]}`)
		}
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.OverloadRules[0].MaxRetries = 0
	runtime.AutoRouting.Strategy.Budget.MaxAuxiliaryCalls = 2
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := stats.New(db)
	proxyHandler := New(runtime, server.Client(), store).(*handler)
	var rows []routing.CallLedgerEntry
	var snapshot routing.AttemptBudgetSnapshot
	proxyHandler.callLedgerSink = func(got []routing.CallLedgerEntry) { rows = got }
	proxyHandler.budgetSnapshotSink = func(got routing.AttemptBudgetSnapshot) { snapshot = got }
	body := `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/cache.png"}},{"type":"text","text":"compare this layout"}]}]}`
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		proxyHandler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Kind != routing.CallAnalyzer ||
		rows[1].Kind != routing.CallAnswer {
		t.Fatalf("cache-hit ledger=%+v", rows)
	}
	if snapshot.AuxiliaryCalls != 1 || snapshot.AnswerAttempts != 1 ||
		snapshot.TotalOutboundCalls != 2 || snapshot.HeldAuxiliaryCalls != 0 ||
		snapshot.HeldAnswerAttempts != 0 || snapshot.HeldOutboundCalls != 0 ||
		snapshot.HeldCostMicroUSD != 0 {
		t.Fatalf("cache-hit snapshot=%+v", snapshot)
	}
	traces, err := store.QueryRoutingTraces(context.Background(), stats.RoutingTraceFilter{Limit: 2})
	if err != nil || len(traces) != 2 {
		t.Fatalf("traces=%+v error=%v", traces, err)
	}
	latestID := traces[0].ID
	physical, err := store.QueryRoutingCalls(context.Background(), stats.RoutingCallFilter{TraceID: &latestID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(physical) != 2 || physical[0].Kind != "analyzer" || physical[1].Kind != "answer" {
		t.Fatalf("cache-hit physical rows=%+v", physical)
	}
}

func TestAutoRoutingDeadlineAfterNodeLeasePreventsAnswerNetworkCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"must not run"}]}`)
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, false)
	runtime.AutoRouting.Strategy.Budget.Deadline = 5 * time.Millisecond
	proxyHandler := New(runtime, server.Client(), nil).(*handler)
	var snapshot routing.AttemptBudgetSnapshot
	proxyHandler.budgetSnapshotSink = func(got routing.AttemptBudgetSnapshot) { snapshot = got }
	proxyHandler.beforeAutoAnswer = func(ctx context.Context) { <-ctx.Done() }
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"delete production data"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	proxyHandler.ServeHTTP(response, request)

	if calls.Load() != 0 {
		t.Fatalf("answer calls=%d status=%d body=%q", calls.Load(), response.Code, response.Body.String())
	}
	if snapshot.TotalOutboundCalls != 0 || snapshot.HeldOutboundCalls != 0 ||
		snapshot.HeldAnswerAttempts != 0 || snapshot.HeldCostMicroUSD != 0 {
		t.Fatalf("deadline snapshot=%+v", snapshot)
	}
}

func autoProxyRuntime(t *testing.T, upstream string, vision bool) profile.Runtime {
	t.Helper()
	no := false
	yes := true
	contextWindow := 200_000
	maxOutput := 16_000
	fastInput := int64(100_000)
	fastOutput := int64(400_000)
	strongInput := int64(3_000_000)
	strongOutput := int64(15_000_000)
	visionInput := int64(200_000)
	visionOutput := int64(800_000)
	config := profile.NewConfig(profile.ProtocolAnthropic, upstream)
	config.Models = []profile.ModelCapabilityConfig{
		{
			ID: "fast", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &no, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &fastInput, OutputPriceMicroUSDPerMillion: &fastOutput,
		},
		{
			ID: "strong", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision: &yes, SupportsTools: &yes, SupportsStructuredOutput: &yes,
			InputPriceMicroUSDPerMillion: &strongInput, OutputPriceMicroUSDPerMillion: &strongOutput,
		},
	}
	if vision {
		config.Vision.Enabled = true
		config.Vision.Model = "vision"
		config.Models = append(config.Models, profile.ModelCapabilityConfig{
			ID: "vision", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutput,
			SupportsVision:               &yes,
			InputPriceMicroUSDPerMillion: &visionInput, OutputPriceMicroUSDPerMillion: &visionOutput,
		})
	}
	config.OverloadRules = []profile.RetryRule{{
		Status: http.StatusServiceUnavailable, MaxRetries: 1, Delay: "0s", Jitter: "0s",
	}}
	config.AutoRouting = profile.AutoRoutingConfig{
		Enabled: true, Participants: []string{"fast", "strong"},
		StrongBaselineModel: "strong", TaskAnalyzerModel: "fast",
		AnalyzerTimeout: "5s", AnalyzerMinConfidenceBPS: 7000,
		Strategy: profile.RoutingStrategyConfig{
			Name: "20260802-001", DefaultRoute: "balanced",
			TaskRoutes: []profile.TaskRouteConfig{{TaskType: "simple", Route: "balanced"}},
			Routes: []profile.RouteConfig{{
				ID: "balanced", MinQualityBPS: 9000, MaxSevereErrorRateBPS: 100,
				Candidates: []profile.RouteCandidateConfig{
					{Model: "fast", QualityScoreBPS: 9200, SevereErrorRateBPS: 50},
					{Model: "strong", QualityScoreBPS: 9900, SevereErrorRateBPS: 10},
				},
			}},
			Budget: profile.AttemptBudgetConfig{
				MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 5,
				MaxRetriesPerTarget: 1, MaxModelSwitches: 1, Deadline: "2m",
				MaxWorstCaseCostMicroUSD: 500_000,
			},
		},
	}
	runtime, err := (profile.Record{
		ID: 8, Slug: "auto", DisplayName: "Auto", Enabled: true, Config: config,
	}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
