package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
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
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || calls.Load() != 2 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
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
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"按钮与输入框重叠"}}]}`)
		case len(root.Messages) > 0 && root.Messages[0].Role == "system":
			analyzerCalls.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9000}"}}]}`)
		default:
			answerCalls.Add(1)
			if root.Model != "fast" || bytes.Contains(body, []byte(`"type":"image_url"`)) ||
				!bytes.Contains(body, []byte("按钮与输入框重叠")) {
				t.Errorf("main body=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"fast","choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()
	runtime := autoProxyRuntime(t, server.URL, true)
	runtime.Protocol = profile.ProtocolOpenAI
	runtime.Vision.Transport = profile.VisionTransportOpenAIChatCompletions
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"auto","max_completion_tokens":1000,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"比较这两个布局方案"}]}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, server.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK || analyzerCalls.Load() != 1 ||
		visionCalls.Load() != 1 || answerCalls.Load() != 1 {
		t.Fatalf("status=%d analyzer=%d vision=%d answer=%d body=%q",
			response.Code, analyzerCalls.Load(), visionCalls.Load(), answerCalls.Load(), response.Body.String())
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
