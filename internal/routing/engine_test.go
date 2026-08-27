package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
)

func TestEngineHistoricalShellDoesNotTriggerHardRisk(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"shell_exec","input":{}}]}]}`)
	plan, classification, err := engine.Route(ctx, nil, request, budget)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || plan.UsesStrongBaseline() || classification.Risk != RiskNormal ||
		classification.Difficulty != DifficultyMedium || calls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, calls.Load())
	}
}

func TestAnalyzerDerivesDifficultyFromComplexitySignals(t *testing.T) {
	config := routingConfig(false)
	analyzer := newAnalyzer(resolveRoutingRuntime(t, config), nil)
	classification, err := analyzer.parseClassification(analyzerClassificationJSON(t,
		analyzerAssessmentForTest("simple", DifficultyHard, RiskNormal, 9100)))
	if err != nil {
		t.Fatal(err)
	}
	if classification.Difficulty != DifficultyHard {
		t.Fatalf("classification=%+v", classification)
	}
}

func TestAnalyzerAcceptsClassificationFromMarkdownJSONFence(t *testing.T) {
	config := routingConfig(false)
	analyzer := newAnalyzer(resolveRoutingRuntime(t, config), nil)
	classification, err := analyzer.parseClassification("```json\n" +
		analyzerClassificationJSON(t, analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)) +
		"\n```")
	if err != nil {
		t.Fatal(err)
	}
	if classification.TaskType != "simple" || classification.Difficulty != DifficultyMedium ||
		classification.Risk != RiskNormal || classification.ConfidenceBPS != 9100 {
		t.Fatalf("classification=%+v", classification)
	}
}

func TestAnalyzerRejectsOversizedResponseInsteadOfParsingTruncatedJSON(t *testing.T) {
	const responseLimit = 64 << 10
	valid := []byte(anthropicClassificationResponse(t,
		analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
	payload := append(valid, bytes.Repeat([]byte(" "), responseLimit-len(valid))...)
	payload = append(payload, 'x')
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	budget, ctx, cancel := NewAttemptBudget(context.Background(), runtime.AutoRouting.Strategy.Budget)
	defer cancel()
	_, err := newAnalyzer(runtime, server.Client()).Analyze(
		ctx,
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"compare"}]}`),
		budget,
	)
	class, ok := provider.FailureClassOf(err)
	if !ok || class != provider.FailureMalformedResponse {
		t.Fatalf("Analyze() error=%v class=%q, want malformed response", err, class)
	}
}

func TestAnalyzerOffersOrdinaryClassificationToolAcrossProtocols(t *testing.T) {
	analyzer := newAnalyzer(routingRuntime(t, false), nil)
	classification := analyzerClassificationJSON(t,
		analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100))
	tests := []struct {
		name       string
		request    Request
		response   string
		tokenField string
	}{
		{
			name:       "anthropic",
			request:    autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"compare"}]}`),
			response:   `{"content":[{"type":"tool_use","name":"llm_proxy_route_classification","input":` + classification + `}]}`,
			tokenField: "max_tokens",
		},
		{
			name:       "chat completions",
			request:    autoOpenAIRequest(t, "/v1/chat/completions", `{"model":"auto","max_completion_tokens":1000,"messages":[{"role":"user","content":"compare"}]}`),
			response:   `{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"llm_proxy_route_classification","arguments":` + mustJSON(t, classification) + `}}]}}]}`,
			tokenField: "max_completion_tokens",
		},
		{
			name:       "responses",
			request:    autoOpenAIRequest(t, "/v1/responses", `{"model":"auto","max_output_tokens":1000,"input":"compare"}`),
			response:   `{"output":[{"type":"function_call","name":"llm_proxy_route_classification","arguments":` + mustJSON(t, classification) + `}]}`,
			tokenField: "max_output_tokens",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := analyzer.buildRequest(tt.request)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(`"llm_proxy_route_classification"`)) {
				t.Fatalf("analyzer request does not offer classification tool: %s", body)
			}
			if bytes.Contains(body, []byte(`"tool_choice"`)) {
				t.Fatalf("analyzer request forces classification tool: %s", body)
			}
			var requestBody map[string]any
			if err := json.Unmarshal(body, &requestBody); err != nil {
				t.Fatal(err)
			}
			if got := int(requestBody[tt.tokenField].(float64)); got != 1024 {
				t.Fatalf("%s=%d want 1024: %s", tt.tokenField, got, body)
			}
			payload, err := analyzerResponseText(tt.request.Operation, []byte(tt.response))
			if err != nil {
				t.Fatal(err)
			}
			got, err := analyzer.parseClassification(payload)
			if err != nil || got.TaskType != "simple" || got.Difficulty != DifficultyMedium ||
				got.Risk != RiskNormal || got.ConfidenceBPS != 9100 {
				t.Fatalf("classification=%+v err=%v payload=%s", got, err, payload)
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
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
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

func TestEngineRoutesAnalyzerRequestWithoutExplicitBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	plan, classification, err := engine.Route(
		context.Background(),
		nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"compare the migration options"}]}`),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || classification.Source != ClassificationSourceAnalyzer {
		t.Fatalf("plan=%+v classification=%+v", plan.Snapshot(), classification)
	}
}

func TestEngineSessionPreferenceReanalyzesOrdinaryAndHighRiskTurns(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		analyzerCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("deploy to production")) {
			_, _ = io.WriteString(w, anthropicClassificationResponse(t,
				analyzerAssessmentForTest("simple", DifficultyMedium, RiskHigh, 9500)))
			return
		}
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
	}))
	defer server.Close()

	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	preference := SessionPreference{
		TaskType: "simple", RouteID: "balanced", Model: "fast",
		MinQualityScoreBPS: 9200, Strategy: runtime.AutoRouting.Strategy.Name,
	}

	t.Run("ordinary follow-up", func(t *testing.T) {
		budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
		defer cancel()
		request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"继续比较迁移方案的第二阶段"}]}`)
		plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, preference)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Model() != "fast" || classification.Source != ClassificationSourceAnalyzer ||
			classification.TaskType != "simple" || analyzerCalls.Load() != 1 {
			t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
		}
	})

	t.Run("semantic high risk on a later turn", func(t *testing.T) {
		budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
		defer cancel()
		request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"tools":[{"name":"deploy"}],"messages":[{"role":"user","content":"deploy to production"}]}`)
		plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, preference)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Model() != "strong" || classification.Source != ClassificationSourceAnalyzer ||
			classification.Risk != RiskHigh || analyzerCalls.Load() != 2 {
			t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
		}
	})
}

func TestEngineReanalyzesComplexSessionFollowUpAndUpgrades(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyHard, RiskNormal, 9500)))
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"在不修改任何外部接口且全程不停机的前提下，要怎么把单地域系统迁移成三地域线性一致架构，要求 RPO=0、RTO<30秒。请给出 quorum 推导、故障模型、状态机不变量、数据校验、流量切换、分阶段回滚和演练验收标准，并证明各项约束不存在冲突？"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "simple", Difficulty: DifficultyMedium, RouteID: "balanced", Model: "fast",
		MinQualityScoreBPS: 9200, Strategy: runtime.AutoRouting.Strategy.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || !plan.UsesStrongBaseline() ||
		classification.Source != ClassificationSourceAnalyzer ||
		classification.Difficulty != DifficultyHard || analyzerCalls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
}

func TestEngineHighestModelSessionSkipsAnalyzer(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, `{}`)
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"继续完成上一轮的方案"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "reasoning", Difficulty: DifficultyHard,
		RouteID: "strong", Model: "strong", MinQualityScoreBPS: 9900,
		Strategy: runtime.AutoRouting.Strategy.Name, ModelLocked: true, TaskContinuation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analyzerCalls.Load() != 0 || plan.Model() != "strong" ||
		plan.Reason() != "Session binding" || classification.Source != ClassificationSourceSession ||
		classification.Difficulty != DifficultyHard {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
	if graph := plan.CallGraph(); graph.ConsumedBeforePlanCalls != 0 || graph.ConsumedBeforePlanMicroUSD != 0 {
		t.Fatalf("call graph consumed analyzer budget: %+v", graph)
	}
}

func TestEngineInvalidHighestModelSessionReanalyzes(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyEasy, RiskNormal, 9500)))
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"new task"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "unknown", Difficulty: DifficultyUnknown,
		RouteID: "balanced", Model: "strong", MinQualityScoreBPS: 9900,
		Strategy: runtime.AutoRouting.Strategy.Name, ModelLocked: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analyzerCalls.Load() != 1 || plan.Model() != "fast" ||
		classification.Source != ClassificationSourceAnalyzer || classification.Difficulty != DifficultyEasy {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
}

func TestEngineSemanticHighRiskOverridesSessionWithoutLiteralPattern(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskHigh, 9500)))
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"请立即把线上客户表删——掉，然后清除备份。"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "simple", Difficulty: DifficultyMedium, RouteID: "balanced", Model: "fast",
		MinQualityScoreBPS: 9200, Strategy: runtime.AutoRouting.Strategy.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || !plan.UsesStrongBaseline() || plan.Reason() != "high risk" ||
		classification.Source != ClassificationSourceAnalyzer || classification.Risk != RiskHigh ||
		analyzerCalls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
}

func TestEngineIgnoresSessionPreferenceFromAnotherStrategy(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
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
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"继续比较迁移方案"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "simple", RouteID: "balanced", Model: "fast",
		MinQualityScoreBPS: 9200, Strategy: "old-strategy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || classification.Source != ClassificationSourceAnalyzer || analyzerCalls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
}

func TestEngineKeepsLockedSessionAcrossAutomaticRouteSplit(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100)))
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	config.AutoRouting.Strategy.TaskRoutes = append(
		config.AutoRouting.Strategy.TaskRoutes,
		profile.TaskRouteConfig{TaskType: "simple", Difficulty: "medium", Route: "strong"},
	)
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"继续当前任务"}]}`)
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, SessionPreference{
		TaskType: "simple", Difficulty: DifficultyMedium, RouteID: "balanced", Model: "strong",
		MinQualityScoreBPS: 9900, Strategy: runtime.AutoRouting.Strategy.Name, ModelLocked: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "strong" || plan.Route() != "strong" ||
		classification.Source != ClassificationSourceAnalyzer || analyzerCalls.Load() != 1 {
		t.Fatalf("plan=%+v classification=%+v analyzer_calls=%d", plan.Snapshot(), classification, analyzerCalls.Load())
	}
}

func TestAnalyzerReservesConstructedPromptCostAndRecordsUsageActual(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, anthropicClassificationResponseWithUsage(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9100), 12, 4))
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
	wantCost := estimateCallCost((len(body)+3)/4, analyzerMaxOutputTokens, analyzer.model)
	oldCost := estimateCallCost(request.Facts.EstimatedInputTokens, analyzerMaxOutputTokens, analyzer.model)
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

func TestEngineGuardsAnalyzerFailureAndLowConfidenceIndependently(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "upstream failure", status: http.StatusBadGateway, body: `{"error":"failed"}`},
		{name: "invalid response", status: http.StatusOK, body: `{"content":[{"type":"text","text":"not json"}]}`},
		{name: "low confidence", status: http.StatusOK, body: anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 6999))},
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
			if tt.name == "low confidence" {
				if plan.Model() != "strong" || !plan.UsesStrongBaseline() ||
					classification.Source != ClassificationSourceAnalyzer ||
					classification.TaskType != "default" || classification.Difficulty != DifficultyMedium ||
					classification.Risk != RiskUnknown || plan.Reason() != "guarded risk baseline" ||
					classification.ConfidenceBPS != 6999 ||
					!slices.Contains(classification.ReasonCodes, "task_type_low_confidence") ||
					!slices.Contains(classification.ReasonCodes, "difficulty_low_confidence") ||
					!slices.Contains(classification.ReasonCodes, "risk_low_confidence") {
					t.Fatalf("low-confidence fallback=%+v", classification)
				}
			} else {
				if plan.Model() != "strong" || !plan.UsesStrongBaseline() ||
					classification.Source != ClassificationSourceFallback ||
					classification.Risk != RiskUnknown || plan.Reason() != "task analyzer fallback" ||
					classification.ConfidenceBPS != 0 || len(classification.ReasonCodes) != 1 ||
					!strings.HasPrefix(classification.ReasonCodes[0], "task_analyzer_") {
					t.Fatalf("failed fallback=%+v", classification)
				}
				if tt.name == "invalid response" &&
					classification.ReasonCodes[0] != "task_analyzer_malformed_response_invalid_json" {
					t.Fatalf("invalid response reason=%v", classification.ReasonCodes)
				}
			}
		})
	}
}

func TestEngineFallsBackOnAnalyzerProtocolCapabilityFailureWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "request", status: http.StatusBadRequest},
		{name: "capability", status: http.StatusUnprocessableEntity},
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
			if err != nil {
				t.Fatal(err)
			}
			if plan.Model() != "strong" || !plan.UsesStrongBaseline() ||
				classification.Source != ClassificationSourceFallback ||
				classification.Risk != RiskUnknown || plan.Reason() != "task analyzer fallback" ||
				!slices.Equal(classification.ReasonCodes, []string{"task_analyzer_request_protocol_capability"}) {
				t.Fatalf("plan=%+v classification=%+v", plan.Snapshot(), classification)
			}
			if calls.Load() != 1 {
				t.Fatalf("analyzer calls=%d, want 1", calls.Load())
			}
		})
	}
}

func TestEngineStopsOnAnalyzerAuthenticationAndRedirectFailureWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "authentication", status: http.StatusUnauthorized},
		{name: "authorization", status: http.StatusForbidden},
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
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("simple", DifficultyMedium, RiskNormal, 9000)))
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

func TestAnalyzerInstructionsCoverHardTaskFamiliesWithoutOverrouting(t *testing.T) {
	for _, phrase := range []string{
		"distributed or concurrent systems",
		"cross-module debugging",
		"security or cryptographic",
		"performance or capacity",
		"multi-source synthesis",
		"Do not set hard signals solely",
		"Coding-specific rubric",
		"root cause is unknown or nondeterministic",
		`"Fix this bug" or "modify this bug" alone`,
		"Risk rubric, independent of difficulty",
		"across languages, punctuation, spacing",
		"quotation, negation, historical discussion",
		"simple destructive action can be high risk",
	} {
		if !strings.Contains(analyzerInstructions, phrase) {
			t.Fatalf("analyzer instructions missing %q", phrase)
		}
	}
}

func TestAnalyzerPromptContainsBoundedRecentConversationContext(t *testing.T) {
	runtime := routingRuntime(t, false)
	analyzer := newAnalyzer(runtime, nil)
	request := autoAnthropicRequest(t, `{
		"model":"auto","max_tokens":1000,
		"messages":[
			{"role":"user","content":"design a safe production data deletion workflow"},
			{"role":"assistant","content":"draft answer"},
			{"role":"user","content":"continue and include rollback safeguards"}
		]
	}`)

	body, _, err := analyzer.buildRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"design a safe production data deletion workflow",
		"continue and include rollback safeguards",
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("analyzer body missing conversation context %q: %s", want, body)
		}
	}
}

func TestAnalyzerPromptDistinguishesAdvertisedToolsFromActualOperations(t *testing.T) {
	runtime := routingRuntime(t, false)
	analyzer := newAnalyzer(runtime, nil)
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"tools":[{"name":"weather"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"read_weather","input":{}}]}]}`)
	body, _, err := analyzer.buildRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"actual_tool_operations", "read_weather", "advertised tool availability alone"} {
		if !strings.Contains(text, want) {
			t.Fatalf("analyzer body missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, `\"name\":\"weather\"`) {
		t.Fatalf("analyzer body included advertised tool definition: %s", text)
	}
}

func TestEngineTaskContinuationSkipsAnalyzer(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"继续修复"}]}`)
	preference := SessionPreference{
		TaskContinuation:   true,
		TaskType:           "simple",
		Difficulty:         DifficultyMedium,
		RouteID:            "balanced",
		Model:              "fast",
		MinQualityScoreBPS: 9200,
		Strategy:           runtime.AutoRouting.Strategy.Name,
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, preference)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model() != "fast" || classification.Source != ClassificationSourceSession ||
		len(classification.ReasonCodes) != 1 || classification.ReasonCodes[0] != "session_task_continuation" {
		t.Fatalf("plan=%+v classification=%+v", plan.Snapshot(), classification)
	}
	if analyzerCalls.Load() != 0 {
		t.Fatalf("analyzer calls=%d", analyzerCalls.Load())
	}
}

func TestEngineLockedModelOnNewTaskReanalyzesRisk(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t,
			analyzerAssessmentForTest("coding", DifficultyHard, RiskHigh, 9500)))
	}))
	defer server.Close()
	config := routingConfig(false)
	config.Upstream = server.URL
	runtime := resolveRoutingRuntime(t, config)
	engine, err := NewEngine(runtime, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"deploy the new production configuration"}]}`)
	preference := SessionPreference{
		ModelLocked:        true,
		TaskType:           "simple",
		Difficulty:         DifficultyMedium,
		RouteID:            "balanced",
		Model:              "fast",
		MinQualityScoreBPS: 9200,
		Strategy:           runtime.AutoRouting.Strategy.Name,
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	plan, classification, err := engine.RouteWithPreference(ctx, nil, request, budget, preference)
	if err != nil {
		t.Fatal(err)
	}
	if analyzerCalls.Load() != 1 || classification.Risk != RiskHigh || plan.Model() != runtime.AutoRouting.StrongBaselineModel {
		t.Fatalf("calls=%d plan=%+v classification=%+v", analyzerCalls.Load(), plan.Snapshot(), classification)
	}
}
