package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
	if receivedHeaders.Get("Authorization") != "Bearer secret" ||
		receivedHeaders.Get("Anthropic-Version") != "2023-06-01" ||
		receivedHeaders.Get("Anthropic-Beta") != "" || receivedHeaders.Get("Cookie") != "" {
		t.Fatalf("analyzer headers=%v", receivedHeaders)
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
