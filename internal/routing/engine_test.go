package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/provider"
)

func TestClassifyLocalUsesOnlyHighConfidenceRules(t *testing.T) {
	baseline := routingRuntime(t, false).Models["strong"]
	tests := []struct {
		name     string
		body     string
		matched  bool
		taskType string
		risk     Risk
	}{
		{
			name:    "tool use is high risk",
			body:    `{"model":"auto","max_tokens":1000,"tools":[{"name":"edit"}],"messages":[{"role":"user","content":"edit the file"}]}`,
			matched: true, taskType: "high_risk", risk: RiskHigh,
		},
		{
			name:    "obvious greeting",
			body:    `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好，请简单介绍一下自己"}]}`,
			matched: true, taskType: "simple", risk: RiskNormal,
		},
		{
			name:    "ambiguous analysis",
			body:    `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`,
			matched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification, matched := ClassifyLocal(autoAnthropicRequest(t, tt.body), baseline)
			if matched != tt.matched || classification.TaskType != tt.taskType || classification.Risk != tt.risk {
				t.Fatalf("classification=%+v matched=%v", classification, matched)
			}
		})
	}
}

func TestEngineCallsAnalyzerOnlyForUncertainRequests(t *testing.T) {
	var analyzerCalls atomic.Int32
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		analyzerCalls.Add(1)
		receivedHeaders = r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Model != "fast" || request.Stream {
			t.Errorf("analyzer request=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9100}"}]}`)
	}))
	defer server.Close()

	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	headers := http.Header{
		"Authorization":     {"Bearer secret"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"must-not-forward"},
		"Cookie":            {"must-not-forward"},
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`)

	plan, classification, err := engine.Route(ctx, headers, request, budget)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || classification.Source != ClassificationSourceAnalyzer {
		t.Fatalf("plan=%+v classification=%+v", plan.Snapshot(), classification)
	}
	if analyzerCalls.Load() != 1 {
		t.Fatalf("analyzer calls=%d", analyzerCalls.Load())
	}
	if graph := plan.CallGraph(); graph.ConsumedBeforePlanCalls != 1 ||
		graph.ConsumedBeforePlanMicroUSD <= 0 {
		t.Fatalf("call graph did not freeze analyzer spend: %+v", graph)
	}
	if receivedHeaders.Get("Authorization") != "Bearer secret" ||
		receivedHeaders.Get("Anthropic-Version") != "2023-06-01" ||
		receivedHeaders.Get("Anthropic-Beta") != "" || receivedHeaders.Get("Cookie") != "" {
		t.Fatalf("analyzer headers=%v", receivedHeaders)
	}
}

func TestAnalyzerReservesConstructedPromptCostAndRecordsUsageActual(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9100}"}],"usage":{"input_tokens":12,"output_tokens":4}}`)
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	analyzer := newAnalyzer(runtime, server.Client())
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1,"messages":[{"role":"user","content":"compare"}]}`)
	body, _, err := analyzer.buildRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	wantCost := estimateCallCost((len(body)+3)/4, 256, analyzer.model)
	oldCost := estimateCallCost(request.Facts.EstimatedInputTokens, 256, analyzer.model)
	if wantCost <= oldCost {
		t.Fatalf("constructed cost=%d old=%d body_bytes=%d", wantCost, oldCost, len(body))
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), runtime.AutoRouting.Strategy.Budget)
	defer cancel()
	ledger := NewCallLedger("analyzer-cost")
	ctx = WithCallLedger(ctx, ledger)
	if _, err := analyzer.Analyze(ctx, nil, request, budget); err != nil {
		t.Fatal(err)
	}
	if got := budget.Snapshot().WorstCaseCostMicroUSD; got != wantCost {
		t.Fatalf("reserved=%d want=%d", got, wantCost)
	}
	row := ledger.Snapshot()[0]
	if !row.ActualCostKnown || row.ActualMicroUSD <= 0 {
		t.Fatalf("ledger=%+v", row)
	}
}

func TestEngineFallsBackToStrongBaselineOnAnalyzerFailureOrLowConfidence(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "upstream failure", status: http.StatusBadGateway, body: `{"error":"failed"}`},
		{name: "invalid response", status: http.StatusOK, body: `{"content":[{"type":"text","text":"not json"}]}`},
		{name: "low confidence", status: http.StatusOK, body: `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":6999}"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			config := routingConfig(false)
			config.Upstream = server.URL
			runtime := resolveRoutingRuntime(t, config)
			engine, err := NewEngine(runtime, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
			defer cancel()
			request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`)

			plan, classification, err := engine.Route(ctx, nil, request, budget)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Model() != "strong" || !plan.UsesStrongBaseline() ||
				classification.Source != ClassificationSourceFallback {
				t.Fatalf("plan=%+v classification=%+v", plan.Snapshot(), classification)
			}
		})
	}
}

func TestEngineStopsOnHardAnalyzerHTTPFailureWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "authentication", status: http.StatusUnauthorized},
		{name: "authorization", status: http.StatusForbidden},
		{name: "request", status: http.StatusBadRequest},
		{name: "capability", status: http.StatusUnprocessableEntity},
		{name: "redirect protocol", status: http.StatusTemporaryRedirect},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const secretBody = "UPSTREAM_PRIVATE_ERROR_BODY"
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, secretBody)
			}))
			defer server.Close()
			config := routingConfig(false)
			config.Upstream = server.URL
			engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
			defer cancel()
			request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`)

			plan, classification, err := engine.Route(ctx, nil, request, budget)
			if err == nil {
				t.Fatalf("Route() plan=%+v classification=%+v, want hard failure", plan.Snapshot(), classification)
			}
			if strings.Contains(err.Error(), secretBody) {
				t.Fatalf("Route() leaked upstream body: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("analyzer calls=%d, want 1", calls.Load())
			}
		})
	}
}

func TestEngineParentRequestDeadlineStopsWithoutFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	config.AutoRouting.AnalyzerTimeout = "5s"
	engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	parent, parentCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer parentCancel()
	budget, ctx, cancel := engine.NewAttemptBudget(parent)
	defer cancel()

	plan, classification, err := engine.Route(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
		budget,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Route() error=%v, want parent deadline", err)
	}
	if plan.Model() != "" || classification.Source != "" || calls.Load() > 1 {
		t.Fatalf("plan=%+v classification=%+v calls=%d", plan.Snapshot(), classification, calls.Load())
	}
}

func TestEngineAttemptBudgetExhaustionStopsBeforeAnalyzer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	for range 2 {
		if err := budget.ReserveCall(ctx, CallVision, 0); err != nil {
			t.Fatal(err)
		}
	}

	plan, classification, err := engine.Route(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
		budget,
	)
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("Route() error=%v, want ErrAttemptBudgetExceeded", err)
	}
	if plan.Model() != "" || classification.Source != "" || calls.Load() != 0 {
		t.Fatalf("plan=%+v classification=%+v calls=%d", plan.Snapshot(), classification, calls.Load())
	}
}

func TestEngineAnalyzerChildTimeoutFallsBackToStrongBaseline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	config.AutoRouting.AnalyzerTimeout = "20ms"
	engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()

	plan, classification, err := engine.Route(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
		budget,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || classification.Source != ClassificationSourceFallback || calls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v calls=%d", plan.Snapshot(), classification, calls.Load())
	}
}

func TestEngineHTTPClientTimeoutFallsBackToStrongBaseline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	client := server.Client()
	client.Timeout = 20 * time.Millisecond
	engine, err := NewEngine(resolveRoutingRuntime(t, config), client)
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()

	plan, classification, err := engine.Route(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
		budget,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || classification.Source != ClassificationSourceFallback || calls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v calls=%d", plan.Snapshot(), classification, calls.Load())
	}
}

func TestEngineUnknownFutureFailureClassStopsWithoutFallback(t *testing.T) {
	config := routingConfig(false)
	runtime := resolveRoutingRuntime(t, config)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, provider.NewFailure(
			provider.FailureClass("future_transport_failure"),
			0,
			errors.New("future failure"),
		)
	})}
	engine, err := NewEngine(runtime, client)
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()

	plan, classification, err := engine.Route(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较两种分布式架构的取舍并给出迁移方案"}]}`),
		budget,
	)
	if err == nil {
		t.Fatalf("Route() plan=%+v classification=%+v, want hard stop", plan.Snapshot(), classification)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAnalyzerPromptContainsFactsButNotCredentials(t *testing.T) {
	var analyzerBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		analyzerBody = string(body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"task_type\":\"simple\",\"risk\":\"normal\",\"confidence_bps\":9000}"}]}`)
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"比较缓存方案 SECRET_IN_PROMPT"}]}`)
	_, _, err = engine.Route(ctx, http.Header{"Authorization": {"Bearer TOP_SECRET"}}, request, budget)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(analyzerBody, "SECRET_IN_PROMPT") || strings.Contains(analyzerBody, "TOP_SECRET") {
		t.Fatalf("analyzer body=%s", analyzerBody)
	}
}
