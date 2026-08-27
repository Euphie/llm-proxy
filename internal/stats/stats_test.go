package stats

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
)

func TestDBDoesNotExposeLegacyHTTPHandlers(t *testing.T) {
	dbType := reflect.TypeOf((*DB)(nil))
	for _, name := range []string{"UIHandler", "Handler"} {
		if _, exists := dbType.MethodByName(name); exists {
			t.Fatalf("legacy public method %s remains exposed", name)
		}
	}
}

func TestRecordAsyncStoresProfileMetadata(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (?, ?, ?, 1, '{}', ?, ?)
	`, 9, "coding", "Coding", now, now); err != nil {
		t.Fatal(err)
	}

	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	store.RecordAsync(RequestMeta{
		ProfileID:   9,
		ProfileSlug: "coding",
		Protocol:    "anthropic",
		Kind:        "vision",
		Path:        "/v1/messages",
	}, []byte(`{"model":"Sonnet","usage":{"input_tokens":10,"output_tokens":20}}`), AnthropicParser{})
	waitForUsageWrite(t, done)

	var profileID sql.NullInt64
	var slug, protocol, kind, path, model string
	var input, output int
	err = db.QueryRow(`
		SELECT profile_id, profile_slug, protocol, request_kind, path, model,
		       input_tokens, output_tokens
		FROM usage
	`).Scan(&profileID, &slug, &protocol, &kind, &path, &model, &input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !profileID.Valid || profileID.Int64 != 9 ||
		slug != "coding" || protocol != "anthropic" || kind != "vision" ||
		path != "/v1/messages" || model != "sonnet" || input != 10 || output != 20 {
		t.Fatalf(
			"row=%v %q %q %q %q %q %d %d",
			profileID, slug, protocol, kind, path, model, input, output,
		)
	}
}

func TestRecordAsyncPreservesSlugWhenProfileNoLongerExists(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	store.RecordAsync(RequestMeta{
		ProfileID:   77,
		ProfileSlug: "deleted-profile",
		Protocol:    "openai",
		Kind:        "main",
		Path:        "/v1/chat/completions",
	}, []byte(`{"model":"gpt","usage":{"prompt_tokens":3,"completion_tokens":4}}`), OpenAIParser{})
	waitForUsageWrite(t, done)

	var profileID sql.NullInt64
	var slug string
	if err := db.QueryRow(`SELECT profile_id, profile_slug FROM usage`).
		Scan(&profileID, &slug); err != nil {
		t.Fatal(err)
	}
	if profileID.Valid || slug != "deleted-profile" {
		t.Fatalf("profile_id=%v slug=%q", profileID, slug)
	}
}

func TestRecordRoutingTraceAsyncStoresOnlyStructuredMetadata(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 9)

	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	store.RecordRoutingTraceAsync(RoutingTrace{
		ProfileID: 9, ProfileSlug: "coding", Protocol: "anthropic",
		Path: "/v1/messages", Strategy: "20260802-001", Route: "balanced",
		TaskType: "simple", Risk: "normal", ClassificationSource: "rule",
		InitialModel: "fast", FinalModel: "strong",
		VisionMode: "native",
		StatusCode: 200, ClientCommitted: true, AnswerAttempts: 2,
		AuxiliaryCalls: 1, TotalOutboundCalls: 3, ModelSwitches: 1,
		SelfEscalations: 1, SelfEscalationReason: "insufficient_reasoning",
		PlannedWorstCaseCostMicroUSD:  30126,
		ConsumedEstimatedCostMicroUSD: 15500, ElapsedMilliseconds: 42,
		RuntimeRevision: 4, PolicyVersionID: 10, ModelCatalogRevision: 3,
	})
	waitForUsageWrite(t, done)

	var got RoutingTrace
	var committed int
	err = db.QueryRow(`
		SELECT profile_id, profile_slug, protocol, path, strategy_name, route_id,
		       task_type, risk, classification_source, initial_model, final_model,
		       vision_mode, status_code, client_committed, answer_attempts,
		       auxiliary_calls, total_outbound_calls, model_switches,
		       self_escalations, self_escalation_reason,
		       planned_worst_case_cost_micro_usd,
		       consumed_estimated_cost_micro_usd, elapsed_ms,
		       runtime_revision, policy_version_id, model_catalog_revision
		FROM routing_traces
	`).Scan(
		&got.ProfileID, &got.ProfileSlug, &got.Protocol, &got.Path,
		&got.Strategy, &got.Route, &got.TaskType, &got.Risk,
		&got.ClassificationSource, &got.InitialModel, &got.FinalModel,
		&got.VisionMode, &got.StatusCode, &committed, &got.AnswerAttempts,
		&got.AuxiliaryCalls, &got.TotalOutboundCalls, &got.ModelSwitches,
		&got.SelfEscalations, &got.SelfEscalationReason,
		&got.PlannedWorstCaseCostMicroUSD,
		&got.ConsumedEstimatedCostMicroUSD, &got.ElapsedMilliseconds,
		&got.RuntimeRevision, &got.PolicyVersionID, &got.ModelCatalogRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	got.ClientCommitted = committed == 1
	if got.ProfileID != 9 || got.Strategy != "20260802-001" ||
		got.InitialModel != "fast" || got.FinalModel != "strong" ||
		!got.ClientCommitted || got.TotalOutboundCalls != 3 ||
		got.PlannedWorstCaseCostMicroUSD != 30126 ||
		got.SelfEscalations != 1 || got.SelfEscalationReason != "insufficient_reasoning" ||
		got.ConsumedEstimatedCostMicroUSD != 15500 || got.ElapsedMilliseconds != 42 ||
		got.RuntimeRevision != 4 || got.PolicyVersionID != 10 || got.ModelCatalogRevision != 3 {
		t.Fatalf("trace=%+v", got)
	}

	rows, err := store.QueryRoutingTraces(context.Background(), RoutingTraceFilter{
		ProfileID: &got.ProfileID,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID <= 0 || rows[0].ProfileSlug != "coding" ||
		rows[0].Strategy != "20260802-001" || rows[0].FinalModel != "strong" ||
		rows[0].SelfEscalations != 1 || rows[0].SelfEscalationReason != "insufficient_reasoning" ||
		rows[0].ConsumedEstimatedCostMicroUSD != 15500 {
		t.Fatalf("queried traces=%+v", rows)
	}
}

func TestRoutingTraceHookRunsAfterSuccessfulCommit(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 9)

	store := New(db)
	done := make(chan error, 1)
	store.SetRoutingTraceRecordedHook(func(ctx context.Context, profileID int64) error {
		var count int
		err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM routing_traces WHERE profile_id = ?
		`, profileID).Scan(&count)
		if err == nil && count != 1 {
			err = fmt.Errorf("committed traces=%d", count)
		}
		done <- err
		return err
	})
	store.RecordRoutingTraceAsync(RoutingTrace{ProfileID: 9, CorrelationID: "hooked"})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("routing trace hook did not run")
	}
}

func TestRecordRoutingTraceCopiesAndValidatesPrivateSessionKey(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db)
	done := make(chan struct{}, 2)
	store.afterWrite = func() { done <- struct{}{} }
	sessionKey := bytes.Repeat([]byte{0x3a}, 32)
	store.RecordRoutingTraceAsync(RoutingTrace{
		CorrelationID: "session-copy", SessionKey: sessionKey,
	})
	for index := range sessionKey {
		sessionKey[index] = 0xff
	}
	waitForUsageWrite(t, done)

	var stored []byte
	if err := db.QueryRow(`
		SELECT session_key FROM routing_traces WHERE correlation_id = 'session-copy'
	`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, bytes.Repeat([]byte{0x3a}, 32)) {
		t.Fatalf("stored session key changed: %x", stored)
	}

	store.RecordRoutingTraceAsync(RoutingTrace{
		CorrelationID: "invalid-session", SessionKey: bytes.Repeat([]byte{1}, 31),
	})
	waitForUsageWrite(t, done)
	var invalid int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM routing_traces WHERE correlation_id = 'invalid-session'
	`).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Fatalf("invalid session key persisted %d traces", invalid)
	}
}

func TestRecordRoutingTraceWithCallsAsyncPersistsExactPhysicalSequenceAndPrivacy(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 9)

	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	trace := RoutingTrace{
		CorrelationID: "route-abc", ProfileID: 9, ProfileSlug: "coding",
		Protocol: "anthropic", Path: "/v1/messages", Strategy: "20260802-001",
		Route: "balanced", TaskType: "simple", Risk: "normal",
		ClassificationSource: "analyzer", InitialModel: "fast", FinalModel: "fast",
		VisionMode: "composite",
		StatusCode: 502, AnswerAttempts: 2, AuxiliaryCalls: 3, TotalOutboundCalls: 5,
		PlannedWorstCaseCostMicroUSD: 100, ConsumedEstimatedCostMicroUSD: 73,
		HeldCostMicroUSD: 0, KnownActualCostMicroUSD: 11, AllActualCostsKnown: false,
	}
	calls := []PhysicalCall{
		{Sequence: 1, Kind: "analyzer", Model: "classifier", ImageIndex: -1, EstimatedMicroUSD: 5, ActualCostKnown: true, ActualMicroUSD: 2, StatusCode: 200, Outcome: "success"},
		{Sequence: 2, Kind: "vision", Model: "vision", ImageIndex: 0, RetryIndex: 0, EstimatedMicroUSD: 17, StatusCode: 503, Outcome: "overload"},
		{Sequence: 3, Kind: "vision", Model: "vision", ImageIndex: 0, RetryIndex: 1, EstimatedMicroUSD: 17, ActualCostKnown: true, ActualMicroUSD: 4, StatusCode: 200, Outcome: "success"},
		{Sequence: 4, Kind: "answer", Model: "fast", ImageIndex: -1, RetryIndex: 0, EstimatedMicroUSD: 17, StatusCode: 503, Outcome: "upstream"},
		{
			Sequence: 5, Kind: "answer", Model: "fast",
			ImageIndex: -1, RetryIndex: 1, EstimatedMicroUSD: 17,
			ActualCostKnown: true, ActualMicroUSD: 5,
			UsagePresent: true, InputTokens: 1000, OutputTokens: 50,
			CacheReadTokens: 600, CacheWriteTokens: 100, InputIncludesCache: true,
			StatusCode: 200, Outcome: "success",
		},
	}
	store.RecordRoutingTraceWithCallsAsync(trace, calls)
	waitForUsageWrite(t, done)

	var traceID int64
	if err := db.QueryRow(`SELECT id FROM routing_traces WHERE correlation_id = ?`, "route-abc").Scan(&traceID); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryRoutingCalls(context.Background(), RoutingCallFilter{
		CorrelationID: "route-abc", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows=%+v", rows)
	}
	for index, row := range rows {
		if row.TraceID != traceID || row.CorrelationID != "route-abc" || row.Sequence != index+1 {
			t.Fatalf("row[%d]=%+v", index, row)
		}
	}
	if rows[1].Outcome != "overload" || rows[1].ActualCostKnown ||
		!rows[2].ActualCostKnown || rows[2].ActualMicroUSD != 4 {
		t.Fatalf("vision rows=%+v", rows[1:3])
	}
	if !rows[4].UsagePresent || rows[4].InputTokens != 1000 ||
		rows[4].CacheReadTokens != 600 || rows[4].CacheWriteTokens != 100 ||
		!rows[4].InputIncludesCache {
		t.Fatalf("answer usage=%+v", rows[4])
	}
	metrics, err := store.QueryModelCacheMetrics(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if got := metrics["fast"]; got.Samples != 1 || got.UncachedInputTokens != 300 ||
		got.CacheReadTokens != 600 || got.CacheWriteTokens != 100 {
		t.Fatalf("cache metrics=%+v", metrics)
	}
	var serialized string
	if err := db.QueryRow(`SELECT group_concat(quote(value), '|') FROM (
		SELECT correlation_id AS value FROM routing_calls
		UNION ALL SELECT logical_model FROM routing_calls
		UNION ALL SELECT target FROM routing_calls
		UNION ALL SELECT outcome FROM routing_calls
	)`).Scan(&serialized); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture prompt secret", "fixture image secret", "Authorization: secret", "fixture response secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("physical accounting leaked %q: %s", secret, serialized)
		}
	}
}

func TestRecordRoutingTraceWithCallsAndCandidatesIsAtomic(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 2)
	store.afterWrite = func() { done <- struct{}{} }
	trace := RoutingTrace{
		CorrelationID: "decision-1", TaskType: "coding", Difficulty: "hard", Risk: "normal",
		ClassificationSource: "analyzer", ClassificationConfidenceBPS: 9100,
		TaskTypeConfidenceBPS: 9300, DifficultyConfidenceBPS: 9100, RiskConfidenceBPS: 9700,
		ClassificationUnderspecified: true,
		ComplexitySignals: map[string]bool{
			"multiple_interacting_constraints": true,
			"cross_system_or_layer":            true,
		},
		ClassificationReasonCodes: []string{"task_analyzer"}, EstimatedInputTokens: 1200,
		RequestedOutputTokens: 800, DecisionReason: "lowest expected cost",
	}
	calls := []PhysicalCall{{Sequence: 1, Kind: "answer", Model: "fast", ImageIndex: -1}}
	candidates := []CandidateDecision{{
		Model: "fast", Decision: "selected", ReasonCode: "lowest_expected_cost",
		QualityScoreBPS: 9200, StabilityScoreBPS: 9300, SevereErrorRateBPS: 50,
		ExpectedLatencyMS: 250, CostEfficiencyScoreBPS: 10_000,
		PerformanceScoreBPS: 10_000, RoutingScoreBPS: 9500, ExpectedCostMicroUSD: 400,
		AnswerWorstCostMicroUSD: 400, VisionMode: "none",
	}}
	store.RecordRoutingTraceWithCallsAndCandidatesAsync(trace, calls, candidates)
	waitForUsageWrite(t, done)
	var traces, physicalCalls, decisions int
	for table, target := range map[string]*int{
		"routing_traces": &traces, "routing_calls": &physicalCalls,
		"routing_candidate_decisions": &decisions,
	} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if traces != 1 || physicalCalls != 1 || decisions != 1 {
		t.Fatalf("traces=%d calls=%d decisions=%d", traces, physicalCalls, decisions)
	}
	store.RecordRoutingTraceWithCallsAndCandidatesAsync(
		RoutingTrace{CorrelationID: "invalid-candidate"}, calls,
		[]CandidateDecision{{Decision: "selected", ReasonCode: "missing_model"}},
	)
	waitForUsageWrite(t, done)
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_traces`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if traces != 1 {
		t.Fatalf("invalid candidate partially persisted trace count=%d", traces)
	}
	detail, err := store.QueryRoutingTraceDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Trace.Difficulty != "hard" || detail.Trace.ClassificationConfidenceBPS != 9100 ||
		detail.Trace.TaskTypeConfidenceBPS != 9300 || detail.Trace.DifficultyConfidenceBPS != 9100 ||
		detail.Trace.RiskConfidenceBPS != 9700 || !detail.Trace.ClassificationUnderspecified ||
		!detail.Trace.ComplexitySignals["multiple_interacting_constraints"] ||
		!detail.Trace.ComplexitySignals["cross_system_or_layer"] ||
		len(detail.Trace.ClassificationReasonCodes) != 1 || len(detail.Candidates) != 1 ||
		detail.Candidates[0].Model != "fast" || detail.Candidates[0].StabilityScoreBPS != 9300 ||
		detail.Candidates[0].ExpectedLatencyMS != 250 ||
		detail.Candidates[0].CostEfficiencyScoreBPS != 10_000 ||
		detail.Candidates[0].PerformanceScoreBPS != 10_000 ||
		detail.Candidates[0].RoutingScoreBPS != 9500 || len(detail.Calls) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestRoutingSessionFlowGroupsOnlyTheSelectedPrivateSessionInOrder(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 5)
	store.afterWrite = func() { done <- struct{}{} }
	started := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	sessionKey := bytes.Repeat([]byte{0x3a}, 32)
	otherKey := bytes.Repeat([]byte{0x7b}, 32)
	for index, correlation := range []string{"turn-1", "turn-2", "turn-3"} {
		store.RecordRoutingTraceWithCallsAndCandidatesAsync(RoutingTrace{
			CreatedAt:     started.Add(time.Duration(index) * time.Minute),
			CorrelationID: correlation, SessionKey: sessionKey,
			TaskType: "coding", Difficulty: "medium", InitialModel: "fast", FinalModel: "fast",
		}, []PhysicalCall{{
			Sequence: 1, Kind: "answer", Model: "fast", ImageIndex: -1,
			StatusCode: 200, Outcome: "success",
		}}, []CandidateDecision{{
			Model: "fast", Decision: "selected", ReasonCode: "lowest_expected_cost",
			VisionMode: "none",
		}})
	}
	store.RecordRoutingTraceAsync(RoutingTrace{
		CreatedAt: started.Add(90 * time.Second), CorrelationID: "other-session", SessionKey: otherKey,
	})
	store.RecordRoutingTraceAsync(RoutingTrace{
		CreatedAt: started.Add(2 * time.Minute), CorrelationID: "no-session",
	})
	for range 5 {
		waitForUsageWrite(t, done)
	}

	var selectedID int64
	if err := db.QueryRow(`
		SELECT id FROM routing_traces WHERE correlation_id = 'turn-3'
	`).Scan(&selectedID); err != nil {
		t.Fatal(err)
	}
	flow, err := store.QueryRoutingSessionFlow(context.Background(), selectedID)
	if err != nil {
		t.Fatal(err)
	}
	if flow.Scope != "session" || flow.SessionRef != "3a3a3a3a3a3a" ||
		flow.SelectedTraceID != selectedID || flow.Truncated || len(flow.Requests) != 3 {
		t.Fatalf("flow=%+v", flow)
	}
	for index, want := range []string{"turn-1", "turn-2", "turn-3"} {
		request := flow.Requests[index]
		if request.Trace.CorrelationID != want || len(request.Calls) != 1 ||
			len(request.Candidates) != 1 {
			t.Fatalf("request[%d]=%+v", index, request)
		}
	}

	var noSessionID int64
	if err := db.QueryRow(`
		SELECT id FROM routing_traces WHERE correlation_id = 'no-session'
	`).Scan(&noSessionID); err != nil {
		t.Fatal(err)
	}
	requestFlow, err := store.QueryRoutingSessionFlow(context.Background(), noSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if requestFlow.Scope != "request" || requestFlow.SessionRef != "" ||
		len(requestFlow.Requests) != 1 || requestFlow.Requests[0].Trace.ID != noSessionID {
		t.Fatalf("request flow=%+v", requestFlow)
	}
}

func TestRoutingSessionFlowKeepsTheLatestTwentyFiveTurnsThroughTheSelection(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 27)
	store.afterWrite = func() { done <- struct{}{} }
	started := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{0x5c}, 32)
	for index := range 27 {
		store.RecordRoutingTraceAsync(RoutingTrace{
			CreatedAt:     started.Add(time.Duration(index) * time.Second),
			CorrelationID: fmt.Sprintf("turn-%02d", index+1), SessionKey: key,
		})
	}
	for range 27 {
		waitForUsageWrite(t, done)
	}
	var selectedID int64
	if err := db.QueryRow(`
		SELECT id FROM routing_traces WHERE correlation_id = 'turn-27'
	`).Scan(&selectedID); err != nil {
		t.Fatal(err)
	}
	flow, err := store.QueryRoutingSessionFlow(context.Background(), selectedID)
	if err != nil {
		t.Fatal(err)
	}
	if !flow.Truncated || len(flow.Requests) != 25 ||
		flow.Requests[0].Trace.CorrelationID != "turn-03" ||
		flow.Requests[24].Trace.CorrelationID != "turn-27" {
		t.Fatalf("flow=%+v", flow)
	}
}

func TestRoutingTracePaginationAndCategories(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 8)
	store.afterWrite = func() { done <- struct{}{} }
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	traces := []RoutingTrace{
		{CorrelationID: "normal", CreatedAt: now, Risk: "normal", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "fast"},
		{CorrelationID: "high-1", CreatedAt: now.Add(time.Second), Risk: "high", StatusCode: 200, ClientCommitted: true, InitialModel: "strong", FinalModel: "strong"},
		{CorrelationID: "high-2", CreatedAt: now.Add(2 * time.Second), Risk: "high", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "strong", ModelSwitches: 1},
		{CorrelationID: "changed", CreatedAt: now.Add(3 * time.Second), Risk: "normal", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "strong", SelfEscalations: 1},
		{CorrelationID: "failed", CreatedAt: now.Add(4 * time.Second), Risk: "normal", StatusCode: 500, InitialModel: "fast", FinalModel: "fast"},
		{CorrelationID: "cost", CreatedAt: now.Add(5 * time.Second), Risk: "normal", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "fast", PlannedWorstCaseCostMicroUSD: 10, KnownActualCostMicroUSD: 11},
		{CorrelationID: "fallback", CreatedAt: now.Add(6 * time.Second), Risk: "unknown", ClassificationSource: "fallback", StatusCode: 200, ClientCommitted: true, InitialModel: "strong", FinalModel: "strong"},
		{CorrelationID: "no-plan", CreatedAt: now.Add(7 * time.Second), Risk: "unknown", ClassificationSource: "fallback", StatusCode: 429, InitialModel: "strong", FinalModel: "strong", ConsumedEstimatedCostMicroUSD: 3},
		{CorrelationID: "risk-uncertain", CreatedAt: now.Add(8 * time.Second), Risk: "unknown", ClassificationSource: "analyzer", StatusCode: 200, ClientCommitted: true, InitialModel: "strong", FinalModel: "strong"},
	}
	for _, trace := range traces {
		store.RecordRoutingTraceAsync(trace)
	}
	for range traces {
		waitForUsageWrite(t, done)
	}
	page, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 9 || page.TotalPages != 5 || len(page.Items) != 2 ||
		page.CategoryCounts.HighRisk != 2 || page.CategoryCounts.Failed != 2 ||
		page.CategoryCounts.Fallback != 3 || page.CategoryCounts.Changed != 2 ||
		page.CategoryCounts.CostAnomaly != 1 {
		t.Fatalf("page=%+v", page)
	}
	fallback, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{
		Page: 1, PageSize: 25, Category: "fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Total != 3 || len(fallback.Items) != 3 || fallback.Items[0].Risk != "unknown" {
		t.Fatalf("fallback=%+v", fallback)
	}
	failed, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{
		Page: 1, PageSize: 25, Category: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 2 || len(failed.Items) != 2 || failed.CategoryCounts.All != 9 {
		t.Fatalf("failed page=%+v", failed)
	}
	cost, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{
		Page: 1, PageSize: 25, Category: "cost_anomaly",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cost.Total != 1 || len(cost.Items) != 1 || cost.Items[0].CorrelationID != "cost" {
		t.Fatalf("cost anomaly page=%+v", cost)
	}
}

func TestRoutingTraceGroupPageCollapsesSessionRequestsWithoutChangingStoredTraces(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 4)
	store.afterWrite = func() { done <- struct{}{} }
	now := time.Date(2026, 8, 12, 3, 44, 0, 0, time.UTC)
	firstSession := bytes.Repeat([]byte{0xca}, 32)
	secondSession := bytes.Repeat([]byte{0xdb}, 32)
	traces := []RoutingTrace{
		{CorrelationID: "session-a-1", SessionKey: firstSession, CreatedAt: now,
			Risk: "normal", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "fast",
			KnownActualCostMicroUSD: 10},
		{CorrelationID: "session-a-2", SessionKey: firstSession, CreatedAt: now.Add(time.Second),
			Risk: "unknown", ClassificationSource: "fallback", StatusCode: 200, ClientCommitted: true,
			InitialModel: "strong", FinalModel: "strong", KnownActualCostMicroUSD: 20},
		{CorrelationID: "session-b-1", SessionKey: secondSession, CreatedAt: now.Add(2 * time.Second),
			Risk: "normal", StatusCode: 500, InitialModel: "fast", FinalModel: "fast", KnownActualCostMicroUSD: 30},
		{CorrelationID: "request-only", CreatedAt: now.Add(3 * time.Second),
			Risk: "normal", StatusCode: 200, ClientCommitted: true, InitialModel: "fast", FinalModel: "fast",
			KnownActualCostMicroUSD: 40},
	}
	for _, trace := range traces {
		store.RecordRoutingTraceAsync(trace)
	}
	for range traces {
		waitForUsageWrite(t, done)
	}

	page, err := store.QueryRoutingTraceGroupPage(context.Background(), RoutingTraceFilter{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.TotalPages != 1 || len(page.Items) != 3 ||
		page.CategoryCounts.All != 3 || page.CategoryCounts.Fallback != 1 || page.CategoryCounts.Failed != 1 {
		t.Fatalf("page=%+v", page)
	}
	var grouped RoutingTraceGroupRow
	for _, item := range page.Items {
		if item.SessionRef == "cacacacacaca" {
			grouped = item
		}
	}
	if grouped.Scope != "session" || grouped.RequestCount != 2 || grouped.CorrelationID != "session-a-2" ||
		grouped.FallbackRequests != 1 || grouped.FailedRequests != 0 || grouped.TotalKnownActualCostMicroUSD != 30 {
		t.Fatalf("grouped=%+v", grouped)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_traces`).Scan(&stored); err != nil || stored != 4 {
		t.Fatalf("stored=%d err=%v", stored, err)
	}
}

func TestQueryRoutingCallsRequiresBoundedTraceOrCorrelationFilter(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	if _, err := store.QueryRoutingCalls(context.Background(), RoutingCallFilter{Limit: 10}); err == nil {
		t.Fatal("unfiltered call query succeeded")
	}
	if _, err := store.QueryRoutingCalls(context.Background(), RoutingCallFilter{CorrelationID: "x", Limit: 501}); err == nil {
		t.Fatal("oversized call query succeeded")
	}
}

func TestRecordRoutingTraceWithCallsRejectsNonContiguousSequenceAtomically(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	store.RecordRoutingTraceWithCallsAsync(RoutingTrace{CorrelationID: "bad-sequence"}, []PhysicalCall{{
		Sequence: 2, Kind: "answer", ImageIndex: -1,
	}})
	waitForUsageWrite(t, done)
	var traces, calls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_traces`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_calls`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if traces != 0 || calls != 0 {
		t.Fatalf("traces=%d calls=%d", traces, calls)
	}
}

func TestRoutingTraceOrderUsesCapturedCompletionTimeInsteadOfAsyncCommitOrder(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	workers := make(chan func(), 2)
	store.launchWorker = func(worker func()) { workers <- worker }
	firstAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Second)
	store.RecordRoutingTraceAsync(RoutingTrace{CorrelationID: "first", CreatedAt: firstAt})
	store.RecordRoutingTraceAsync(RoutingTrace{CorrelationID: "second", CreatedAt: secondAt})
	firstWorker := <-workers
	secondWorker := <-workers
	secondWorker()
	firstWorker()

	rows, err := store.QueryRoutingTraces(context.Background(), RoutingTraceFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].CorrelationID != "second" || rows[1].CorrelationID != "first" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestRecordAsyncSkipsUnparseableUsage(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }
	store.RecordAsync(RequestMeta{
		ProfileSlug: "temporary",
		Protocol:    "anthropic",
		Kind:        "main",
		Path:        "/v1/messages",
	}, []byte(`not usage`), parserFunc(func([]byte) (Usage, bool) {
		return Usage{}, false
	}))
	waitForUsageWrite(t, done)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("usage rows=%d, want 0", count)
	}
}

func TestNewCloseLeavesSharedDatabaseOpen(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := New(db).Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("shared database was closed: %v", err)
	}
	var profiles int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles); err != nil {
		t.Fatalf("shared database is unavailable: %v", err)
	}
}

func TestCloseWaitsForRecordAsync(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)

	workers := make(chan func(), 1)
	store.launchWorker = func(worker func()) {
		workers <- worker
	}
	store.RecordAsync(RequestMeta{
		ProfileSlug: "temporary",
		Protocol:    "anthropic",
		Kind:        "main",
		Path:        "/v1/messages",
	}, nil, parserFunc(func([]byte) (Usage, bool) {
		return Usage{Model: "sonnet", InputTokens: 10, OutputTokens: 20}, true
	}))

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- store.Close()
	}()
	<-store.closingStarted

	worker := <-workers
	worker()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close error: %v", err)
	}

	var requests, input, output int
	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		FROM usage
	`).Scan(&requests, &input, &output); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || input != 10 || output != 20 {
		t.Fatalf("usage=%d %d %d", requests, input, output)
	}
}

func TestRecordAsyncDoesNotStartWhileCloseDrains(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)

	workers := make(chan func(), 2)
	store.launchWorker = func(worker func()) {
		workers <- worker
	}

	store.RecordAsync(RequestMeta{
		ProfileSlug: "temporary",
		Protocol:    "anthropic",
		Kind:        "main",
		Path:        "/v1/messages",
	}, nil, parserFunc(func([]byte) (Usage, bool) {
		return Usage{}, false
	}))
	firstWorker := <-workers

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- store.Close()
	}()
	<-store.closingStarted

	store.RecordAsync(RequestMeta{
		ProfileSlug: "rejected",
		Protocol:    "anthropic",
		Kind:        "main",
		Path:        "/v1/messages",
	}, nil, parserFunc(func([]byte) (Usage, bool) {
		t.Error("parser was called after closing started")
		return Usage{}, false
	}))
	if len(workers) != 0 {
		t.Fatal("worker was launched after closing started")
	}

	firstWorker()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestQueryFiltersUsageAndPreservesAggregates(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1, 2)

	to := time.Now().UTC().Truncate(time.Hour)
	from := to.Add(-24 * time.Hour)
	insertUsage(t, db, from.Add(-time.Nanosecond), 2, "research", "openai", "main", "other", 70, 80, 7, 8)
	insertUsage(t, db, from, 1, "coding", "anthropic", "main", "sonnet", 10, 20, 1, 2)
	insertUsage(t, db, to, 1, "coding", "openai", "vision", "gpt", 30, 40, 3, 4)
	insertUsage(t, db, to.Add(time.Nanosecond), 2, "research", "anthropic", "main", "sonnet", 50, 60, 5, 6)

	profileID := int64(1)
	tests := []struct {
		name   string
		filter Filter
		want   UsageRow
	}{
		{
			name:   "profile",
			filter: Filter{ProfileID: &profileID},
			want: UsageRow{
				Key: "total", Requests: 2, InputTokens: 40, OutputTokens: 60,
				CacheReadTokens: 4, CacheCreationTokens: 6, TotalTokens: 100,
			},
		},
		{
			name:   "protocol",
			filter: Filter{Protocol: "anthropic"},
			want: UsageRow{
				Key: "total", Requests: 2, InputTokens: 60, OutputTokens: 80,
				CacheReadTokens: 6, CacheCreationTokens: 8, TotalTokens: 140,
			},
		},
		{
			name:   "kind",
			filter: Filter{Kind: "vision"},
			want: UsageRow{
				Key: "total", Requests: 1, InputTokens: 30, OutputTokens: 40,
				CacheReadTokens: 3, CacheCreationTokens: 4, TotalTokens: 70,
			},
		},
		{
			name:   "model",
			filter: Filter{Model: "sonnet"},
			want: UsageRow{
				Key: "total", Requests: 2, InputTokens: 60, OutputTokens: 80,
				CacheReadTokens: 6, CacheCreationTokens: 8, TotalTokens: 140,
			},
		},
		{
			name:   "inclusive UTC time bounds",
			filter: Filter{From: from, To: to},
			want: UsageRow{
				Key: "total", Requests: 2, InputTokens: 40, OutputTokens: 60,
				CacheReadTokens: 4, CacheCreationTokens: 6, TotalTokens: 100,
			},
		},
	}

	store := New(db)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Query(context.Background(), tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			assertUsageRow(t, got.Summary, tt.want)
		})
	}

	got, err := store.Query(context.Background(), Filter{ProfileID: &profileID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ByDay) != 2 ||
		got.ByDay[0].Key != to.Format("2006-01-02") || got.ByDay[0].Requests != 1 ||
		got.ByDay[1].Key != from.Format("2006-01-02") || got.ByDay[1].Requests != 1 {
		t.Fatalf("by_day=%+v", got.ByDay)
	}
	if len(got.ByModel) != 2 ||
		got.ByModel[0].Key != "gpt" || got.ByModel[0].TotalTokens != 70 ||
		got.ByModel[1].Key != "sonnet" || got.ByModel[1].TotalTokens != 30 {
		t.Fatalf("by_model=%+v", got.ByModel)
	}
}

func TestQueryBindsFilterValues(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1)
	insertUsage(
		t, db, time.Now().UTC(), 1, "coding", "anthropic", "main", "sonnet",
		10, 20, 0, 0,
	)

	got, err := New(db).Query(context.Background(), Filter{
		Protocol: `anthropic' OR 1=1 --`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Requests != 0 {
		t.Fatalf("requests=%d, want 0", got.Summary.Requests)
	}
}

func TestQueryPreservesThirtyDayByDayWindow(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1)

	now := time.Now().UTC()
	insertUsage(t, db, now.Add(-31*24*time.Hour), 1, "coding", "anthropic", "main", "old", 1, 2, 0, 0)
	insertUsage(t, db, now, 1, "coding", "anthropic", "main", "current", 3, 4, 0, 0)

	got, err := New(db).Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Requests != 2 || len(got.ByModel) != 2 {
		t.Fatalf("summary=%+v by_model=%+v", got.Summary, got.ByModel)
	}
	if len(got.ByDay) != 1 || got.ByDay[0].Requests != 1 {
		t.Fatalf("by_day=%+v", got.ByDay)
	}
}

// Break caught: silently dropping historical rows from by_day while summary and by_model honor explicit time bounds.
func TestQueryHistoricalTimeBoundsApplyToEveryAggregate(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1)

	from := time.Date(2020, time.January, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2020, time.January, 11, 23, 59, 59, 0, time.UTC)
	insertUsage(t, db, from.Add(-time.Nanosecond), 1, "coding", "anthropic", "main", "before", 100, 200, 0, 0)
	insertUsage(t, db, from, 1, "coding", "anthropic", "main", "sonnet", 10, 20, 1, 2)
	insertUsage(t, db, to, 1, "coding", "anthropic", "main", "gpt", 30, 40, 3, 4)
	insertUsage(t, db, to.Add(time.Nanosecond), 1, "coding", "anthropic", "main", "after", 300, 400, 0, 0)

	got, err := New(db).Query(context.Background(), Filter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	assertUsageRow(t, got.Summary, UsageRow{
		Key: "total", Requests: 2, InputTokens: 40, OutputTokens: 60,
		CacheReadTokens: 4, CacheCreationTokens: 6, TotalTokens: 100,
	})
	if len(got.ByDay) != 2 ||
		got.ByDay[0].Key != "2020-01-11" || got.ByDay[0].Requests != 1 ||
		got.ByDay[1].Key != "2020-01-10" || got.ByDay[1].Requests != 1 {
		t.Fatalf("by_day=%+v", got.ByDay)
	}
	if len(got.ByModel) != 2 ||
		got.ByModel[0].Key != "gpt" || got.ByModel[0].TotalTokens != 70 ||
		got.ByModel[1].Key != "sonnet" || got.ByModel[1].TotalTokens != 30 {
		t.Fatalf("by_model=%+v", got.ByModel)
	}
}

func TestQueryUsesOneSnapshotAcrossAllAggregates(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1)

	now := time.Now().UTC()
	insertUsage(
		t, db, now, 1, "coding", "anthropic", "main", "sonnet",
		10, 20, 1, 2,
	)

	store := New(db)
	var inserted bool
	store.beginQueryTx = func(ctx context.Context) (queryTx, error) {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		return &hookedQueryTx{
			Tx: tx,
			beforeRowsQuery: func() {
				insertUsage(
					t, db, now.Add(time.Second), 1, "coding", "openai", "vision", "gpt",
					100, 200, 10, 20,
				)
				inserted = true
			},
		}, nil
	}

	got, err := store.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("concurrent write was not committed between aggregate queries")
	}
	assertUsageRow(t, got.Summary, UsageRow{
		Key: "total", Requests: 1, InputTokens: 10, OutputTokens: 20,
		CacheReadTokens: 1, CacheCreationTokens: 2, TotalTokens: 30,
	})
	if len(got.ByDay) != 1 || got.ByDay[0].Requests != 1 || got.ByDay[0].TotalTokens != 30 {
		t.Fatalf("by_day=%+v", got.ByDay)
	}
	if len(got.ByModel) != 1 ||
		got.ByModel[0].Key != "sonnet" ||
		got.ByModel[0].Requests != 1 ||
		got.ByModel[0].TotalTokens != 30 {
		t.Fatalf("by_model=%+v", got.ByModel)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("usage rows=%d, want 2", rows)
	}
}

func TestProfileSummariesAggregatesSinceInclusive(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertProfiles(t, db, 1, 2)

	local := time.FixedZone("UTC+8", 8*60*60)
	since := time.Date(2026, 7, 1, 8, 0, 0, 0, local)
	insertUsage(t, db, since.UTC().Add(-time.Nanosecond), 1, "coding", "anthropic", "main", "old", 100, 200, 0, 0)
	insertUsage(t, db, since.UTC(), 1, "coding", "anthropic", "main", "sonnet", 10, 20, 0, 0)
	insertUsage(t, db, since.UTC().Add(time.Hour), 1, "coding", "openai", "vision", "gpt", 5, 6, 0, 0)
	insertUsage(t, db, since.UTC().Add(2*time.Hour), 2, "research", "anthropic", "main", "sonnet", 7, 8, 0, 0)
	insertUsage(t, db, since.UTC().Add(3*time.Hour), nil, "deleted", "anthropic", "main", "sonnet", 9, 10, 0, 0)

	got, err := New(db).ProfileSummaries(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]ProfileSummary{
		1: {Requests: 2, InputTokens: 15, OutputTokens: 26},
		2: {Requests: 1, InputTokens: 7, OutputTokens: 8},
	}
	if len(got) != len(want) {
		t.Fatalf("summaries=%+v", got)
	}
	for profileID, wantSummary := range want {
		if got[profileID] != wantSummary {
			t.Fatalf("profile %d summary=%+v, want %+v", profileID, got[profileID], wantSummary)
		}
	}
}

type parserFunc func([]byte) (Usage, bool)

func (f parserFunc) Parse(data []byte) (Usage, bool) {
	return f(data)
}

type hookedQueryTx struct {
	*sql.Tx
	beforeRowsQuery func()
}

func (tx *hookedQueryTx) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	if tx.beforeRowsQuery != nil {
		beforeRowsQuery := tx.beforeRowsQuery
		tx.beforeRowsQuery = nil
		beforeRowsQuery()
	}
	return tx.Tx.QueryContext(ctx, query, args...)
}

func waitForUsageWrite(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("usage write timed out")
	}
}

func insertProfiles(t *testing.T, db *sql.DB, profileIDs ...int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, profileID := range profileIDs {
		if _, err := db.Exec(`
			INSERT INTO profiles (
				id, slug, display_name, enabled, config_json, created_at, updated_at
			) VALUES (?, ?, ?, 1, '{}', ?, ?)
		`, profileID, "profile-"+time.Unix(profileID, 0).UTC().Format("150405"), "Profile", now, now); err != nil {
			t.Fatal(err)
		}
	}
}

func insertUsage(
	t *testing.T,
	db *sql.DB,
	createdAt time.Time,
	profileID any,
	profileSlug, protocol, kind, model string,
	input, output, cacheRead, cacheCreation int,
) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO usage (
			created_at, profile_id, profile_slug, protocol, request_kind,
			model, path, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens
		) VALUES (?, ?, ?, ?, ?, ?, '/test', ?, ?, ?, ?)
	`,
		createdAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		profileID,
		profileSlug,
		protocol,
		kind,
		model,
		input,
		output,
		cacheRead,
		cacheCreation,
	); err != nil {
		t.Fatal(err)
	}
}

func assertUsageRow(t *testing.T, got, want UsageRow) {
	t.Helper()
	if got != want {
		t.Fatalf("usage row=%+v, want %+v", got, want)
	}
}
