package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

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
