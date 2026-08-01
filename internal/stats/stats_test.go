package stats

import (
	"context"
	"database/sql"
	"reflect"
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
		InitialModel: "fast", FinalModel: "strong", VisionMode: "native",
		StatusCode: 200, ClientCommitted: true, AnswerAttempts: 2,
		AuxiliaryCalls: 1, TotalOutboundCalls: 3, ModelSwitches: 1,
		TargetSwitches: 0, PlannedWorstCaseCostMicroUSD: 30126,
		ReservedCostMicroUSD: 15500, ElapsedMilliseconds: 42,
	})
	waitForUsageWrite(t, done)

	var got RoutingTrace
	var committed int
	err = db.QueryRow(`
		SELECT profile_id, profile_slug, protocol, path, strategy_name, route_id,
		       task_type, risk, classification_source, initial_model, final_model,
		       vision_mode, status_code, client_committed, answer_attempts,
		       auxiliary_calls, total_outbound_calls, model_switches,
		       target_switches, planned_worst_case_cost_micro_usd,
		       reserved_cost_micro_usd, elapsed_ms
		FROM routing_traces
	`).Scan(
		&got.ProfileID, &got.ProfileSlug, &got.Protocol, &got.Path,
		&got.Strategy, &got.Route, &got.TaskType, &got.Risk,
		&got.ClassificationSource, &got.InitialModel, &got.FinalModel,
		&got.VisionMode, &got.StatusCode, &committed, &got.AnswerAttempts,
		&got.AuxiliaryCalls, &got.TotalOutboundCalls, &got.ModelSwitches,
		&got.TargetSwitches, &got.PlannedWorstCaseCostMicroUSD,
		&got.ReservedCostMicroUSD, &got.ElapsedMilliseconds,
	)
	if err != nil {
		t.Fatal(err)
	}
	got.ClientCommitted = committed == 1
	if got.ProfileID != 9 || got.Strategy != "20260802-001" ||
		got.InitialModel != "fast" || got.FinalModel != "strong" ||
		!got.ClientCommitted || got.TotalOutboundCalls != 3 ||
		got.PlannedWorstCaseCostMicroUSD != 30126 ||
		got.ReservedCostMicroUSD != 15500 || got.ElapsedMilliseconds != 42 {
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
		rows[0].ReservedCostMicroUSD != 15500 {
		t.Fatalf("queried traces=%+v", rows)
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
