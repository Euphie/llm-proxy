package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
