package stats

import (
	"context"
	"database/sql"
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
		InitialTarget: "primary", FinalTarget: "region_b", VisionMode: "native",
		StatusCode: 200, ClientCommitted: true, AnswerAttempts: 2,
		AuxiliaryCalls: 1, TotalOutboundCalls: 3, ModelSwitches: 1,
		SelfEscalations: 1, SelfEscalationReason: "insufficient_reasoning",
		TargetSwitches: 0, PlannedWorstCaseCostMicroUSD: 30126,
		ConsumedEstimatedCostMicroUSD: 15500, ElapsedMilliseconds: 42,
	})
	waitForUsageWrite(t, done)

	var got RoutingTrace
	var committed int
	err = db.QueryRow(`
		SELECT profile_id, profile_slug, protocol, path, strategy_name, route_id,
		       task_type, risk, classification_source, initial_model, final_model,
		       initial_target, final_target,
		       vision_mode, status_code, client_committed, answer_attempts,
		       auxiliary_calls, total_outbound_calls, model_switches,
		       self_escalations, self_escalation_reason,
		       target_switches, planned_worst_case_cost_micro_usd,
		       consumed_estimated_cost_micro_usd, elapsed_ms
		FROM routing_traces
	`).Scan(
		&got.ProfileID, &got.ProfileSlug, &got.Protocol, &got.Path,
		&got.Strategy, &got.Route, &got.TaskType, &got.Risk,
		&got.ClassificationSource, &got.InitialModel, &got.FinalModel,
		&got.InitialTarget, &got.FinalTarget,
		&got.VisionMode, &got.StatusCode, &committed, &got.AnswerAttempts,
		&got.AuxiliaryCalls, &got.TotalOutboundCalls, &got.ModelSwitches,
		&got.SelfEscalations, &got.SelfEscalationReason,
		&got.TargetSwitches, &got.PlannedWorstCaseCostMicroUSD,
		&got.ConsumedEstimatedCostMicroUSD, &got.ElapsedMilliseconds,
	)
	if err != nil {
		t.Fatal(err)
	}
	got.ClientCommitted = committed == 1
	if got.ProfileID != 9 || got.Strategy != "20260802-001" ||
		got.InitialModel != "fast" || got.FinalModel != "strong" ||
		got.InitialTarget != "primary" || got.FinalTarget != "region_b" ||
		!got.ClientCommitted || got.TotalOutboundCalls != 3 ||
		got.PlannedWorstCaseCostMicroUSD != 30126 ||
		got.SelfEscalations != 1 || got.SelfEscalationReason != "insufficient_reasoning" ||
		got.ConsumedEstimatedCostMicroUSD != 15500 || got.ElapsedMilliseconds != 42 {
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
		rows[0].InitialTarget != "primary" || rows[0].FinalTarget != "region_b" ||
		rows[0].SelfEscalations != 1 || rows[0].SelfEscalationReason != "insufficient_reasoning" ||
		rows[0].ConsumedEstimatedCostMicroUSD != 15500 {
		t.Fatalf("queried traces=%+v", rows)
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
		InitialTarget: "primary", FinalTarget: "primary", VisionMode: "composite",
		StatusCode: 502, AnswerAttempts: 2, AuxiliaryCalls: 3, TotalOutboundCalls: 5,
		PlannedWorstCaseCostMicroUSD: 100, ConsumedEstimatedCostMicroUSD: 73,
		HeldCostMicroUSD: 0, KnownActualCostMicroUSD: 11, AllActualCostsKnown: false,
	}
	calls := []PhysicalCall{
		{Sequence: 1, Kind: "analyzer", Model: "classifier", Target: "primary", ImageIndex: -1, EstimatedMicroUSD: 5, ActualCostKnown: true, ActualMicroUSD: 2, StatusCode: 200, Outcome: "success"},
		{Sequence: 2, Kind: "vision", Model: "vision", Target: "primary", ImageIndex: 0, RetryIndex: 0, EstimatedMicroUSD: 17, StatusCode: 503, Outcome: "overload"},
		{Sequence: 3, Kind: "vision", Model: "vision", Target: "primary", ImageIndex: 0, RetryIndex: 1, EstimatedMicroUSD: 17, ActualCostKnown: true, ActualMicroUSD: 4, StatusCode: 200, Outcome: "success"},
		{Sequence: 4, Kind: "answer", Model: "fast", Target: "primary", ImageIndex: -1, RetryIndex: 0, EstimatedMicroUSD: 17, StatusCode: 503, Outcome: "upstream"},
		{
			Sequence: 5, Kind: "answer", Model: "fast", Target: "primary",
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
		ClassificationReasonCodes: []string{"task_analyzer"}, EstimatedInputTokens: 1200,
		RequestedOutputTokens: 800, DecisionReason: "lowest expected cost",
	}
	calls := []PhysicalCall{{Sequence: 1, Kind: "answer", Model: "fast", Target: "primary", ImageIndex: -1}}
	candidates := []CandidateDecision{{
		Model: "fast", Decision: "selected", ReasonCode: "lowest_expected_cost",
		QualityScoreBPS: 9200, SevereErrorRateBPS: 50, ExpectedCostMicroUSD: 400,
		AnswerWorstCostMicroUSD: 400, VisionMode: "none", UpstreamNodes: []string{"primary"},
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
		len(detail.Trace.ClassificationReasonCodes) != 1 || len(detail.Candidates) != 1 ||
		detail.Candidates[0].Model != "fast" || len(detail.Calls) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestRoutingTracePaginationAndCategories(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 6)
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
	if page.Total != 7 || page.TotalPages != 4 || len(page.Items) != 2 ||
		page.CategoryCounts.HighRisk != 2 || page.CategoryCounts.Failed != 1 ||
		page.CategoryCounts.Fallback != 1 || page.CategoryCounts.Changed != 2 ||
		page.CategoryCounts.CostAnomaly != 1 {
		t.Fatalf("page=%+v", page)
	}
	fallback, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{
		Page: 1, PageSize: 25, Category: "fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Total != 1 || len(fallback.Items) != 1 || fallback.Items[0].Risk != "unknown" {
		t.Fatalf("fallback=%+v", fallback)
	}
	failed, err := store.QueryRoutingTracePage(context.Background(), RoutingTraceFilter{
		Page: 1, PageSize: 25, Category: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 1 || len(failed.Items) != 1 || failed.Items[0].CorrelationID != "failed" ||
		failed.CategoryCounts.All != 7 {
		t.Fatalf("failed page=%+v", failed)
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
