package routing

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
)

func TestSessionStoreScopesOpaqueKeysAndNeverPersistsRawIdentity(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	store, err := NewSessionStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)

	headers := http.Header{"Authorization": {"Bearer caller-secret"}}
	key, ok := store.Key(headers, "agent-session-42", 8, "answer")
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}
	same, ok := store.Key(headers, "agent-session-42", 8, "answer")
	if !ok || key != same {
		t.Fatal("Session key is not deterministic")
	}
	other, ok := store.Key(headers, "another-session", 8, "answer")
	if !ok || key == other {
		t.Fatal("raw Session identity did not scope Session key")
	}
	otherAuth, ok := store.Key(
		http.Header{"Authorization": {"Bearer another-caller"}},
		"agent-session-42", 8, "answer",
	)
	if !ok || key == otherAuth {
		t.Fatal("authentication domain did not scope Session key")
	}
	if _, ok := store.Key(http.Header{}, "agent-session-42", 8, "answer"); ok {
		t.Fatal("anonymous Session identity was accepted")
	}

	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer",
		TaskType: "simple", Difficulty: DifficultyMedium,
		Model: "fast", QualityScoreBPS: 9200, Strategy: "20260802-001",
	}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.TaskType != "simple" || binding.Difficulty != DifficultyMedium ||
		binding.Model != "fast" || binding.QualityScoreBPS != 9200 {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}

	var storedKey []byte
	if err := db.QueryRow(`SELECT key_hash FROM routing_session_bindings`).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(storedKey, []byte("agent-session-42")) ||
		bytes.Contains(storedKey, []byte("caller-secret")) {
		t.Fatalf("opaque key contains raw identity: %x", storedKey)
	}
}

func TestSessionStoreUpgradesButNeverDowngradesAndExpiresBindings(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	store, err := NewSessionStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, ok := store.Key(
		http.Header{"X-Api-Key": {"caller-secret"}},
		"agent-session-42", 8, "answer",
	)
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}

	bind := func(model string, quality int) {
		t.Helper()
		if err := store.Bind(context.Background(), key, SessionBinding{
			ProfileID: 8, Route: "balanced", Purpose: "answer",
			TaskType: "simple",
			Model:    model, QualityScoreBPS: quality, Strategy: "20260802-001",
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	bind("balanced", 9500)
	bind("cheap", 9200)
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "balanced" {
		t.Fatalf("downgraded binding=%+v found=%v err=%v", binding, found, err)
	}
	bind("strong", 9900)
	binding, found, err = store.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "strong" || binding.QualityScoreBPS != 9900 {
		t.Fatalf("upgraded binding=%+v found=%v err=%v", binding, found, err)
	}

	now = now.Add(2 * time.Hour)
	_, found, err = store.Get(context.Background(), key)
	if err != nil || found {
		t.Fatalf("expired binding found=%v err=%v", found, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_session_bindings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired rows=%d", count)
	}
}

func TestSessionStoreRefreshesBindingWhenStrategyChanges(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, ok := store.Key(
		http.Header{"Authorization": {"Bearer caller-secret"}},
		"agent-session-42", 8, "answer",
	)
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}
	bind := func(strategy, model string, quality int) {
		t.Helper()
		if err := store.Bind(context.Background(), key, SessionBinding{
			ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "simple",
			Model: model, QualityScoreBPS: quality, Strategy: strategy,
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	bind("20260802-001", "strong", 9900)
	bind("20260803-001", "fast", 9000)
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.Strategy != "20260803-001" ||
		binding.Model != "fast" || binding.QualityScoreBPS != 9000 {
		t.Fatalf("new strategy binding=%+v found=%v err=%v", binding, found, err)
	}

	bind("20260803-001", "cheaper", 8500)
	binding, found, err = store.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "fast" || binding.QualityScoreBPS != 9000 {
		t.Fatalf("same strategy downgraded binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreBuildsStablePrivateCanaryBuckets(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Authorization": {"Bearer caller-secret"}}
	bucket, ok := store.CanaryBucket(headers, "agent-session-42", 8, 17)
	if !ok || bucket < 0 || bucket >= 10_000 {
		t.Fatalf("bucket=%d ok=%v", bucket, ok)
	}
	same, ok := store.CanaryBucket(headers, "agent-session-42", 8, 17)
	if !ok || same != bucket {
		t.Fatalf("same bucket=%d ok=%v, want %d", same, ok, bucket)
	}
	other, ok := store.CanaryBucket(headers, "agent-session-42", 8, 18)
	if !ok || other == bucket {
		t.Fatalf("strategy did not scope bucket: %d", other)
	}
	if _, ok := store.CanaryBucket(http.Header{}, "agent-session-42", 8, 17); ok {
		t.Fatal("anonymous request received a canary bucket")
	}
}

func TestSessionStoreLocksOnlyAfterThreeDistinctConfidentLargeTasks(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, ok := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "session", 8, "answer")
	if !ok {
		t.Fatal("valid session identity was rejected")
	}
	bind := func(seed byte, conversationTokens int) SessionBinding {
		t.Helper()
		binding := SessionBinding{
			ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "coding",
			Difficulty: DifficultyMedium, Model: "fast", QualityScoreBPS: 9200,
			Strategy: "20260812-001", ClassificationConfidenceBPS: 9000,
			ClassificationReliable: true,
			ConversationTokens:     conversationTokens, TaskFingerprint: bytes.Repeat([]byte{seed}, 32),
		}
		if err := store.Bind(context.Background(), key, binding, time.Hour); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.Get(context.Background(), key)
		if err != nil || !found {
			t.Fatalf("binding=%+v found=%v err=%v", got, found, err)
		}
		return got
	}
	if got := bind(1, 160_000); got.StableTaskCount != 1 || got.ModelLocked {
		t.Fatalf("first=%+v", got)
	}
	if got := bind(1, 160_000); got.StableTaskCount != 1 || got.ModelLocked {
		t.Fatalf("same task advanced warmup: %+v", got)
	}
	if got := bind(2, 160_000); got.StableTaskCount != 2 || got.ModelLocked {
		t.Fatalf("second=%+v", got)
	}
	if got := bind(3, 160_000); got.StableTaskCount != 3 || !got.ModelLocked || got.LockReason != SessionLockReasonStableConversation {
		t.Fatalf("third=%+v", got)
	}
}

func TestSessionStoreHighestModelBindingIsImmediatelyLocked(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "session", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "strong", Purpose: "answer", TaskType: "reasoning",
		Difficulty: DifficultyHard, Model: "strong", QualityScoreBPS: 10_000,
		Strategy: "20260812-001", HighestModel: true, ClassificationConfidenceBPS: 9500,
		ConversationTokens: 1000, TaskFingerprint: bytes.Repeat([]byte{1}, 32),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || !binding.ModelLocked || binding.LockReason != "highest_model" {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreStrongModelWithoutHighestMarkerDoesNotLock(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "fallback-session", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "strong", Purpose: "answer", TaskType: "unknown",
		Difficulty: DifficultyUnknown, Model: "strong", QualityScoreBPS: 10_000,
		Strategy: "20260812-001", ClassificationConfidenceBPS: 0,
		ConversationTokens: 1000, TaskFingerprint: bytes.Repeat([]byte{1}, 32),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.ModelLocked || binding.LockReason != "" {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreReliableClassificationReplacesInvalidHighestModelLock(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "repair-fallback-lock", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "unknown",
		Difficulty: DifficultyUnknown, Model: "strong", QualityScoreBPS: 9900,
		Strategy: "20260812-001", HighestModel: true,
		TaskFingerprint: bytes.Repeat([]byte{1}, 32),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "simple",
		Difficulty: DifficultyEasy, Model: "fast", QualityScoreBPS: 9200,
		Strategy: "20260812-001", ClassificationConfidenceBPS: 9500,
		ClassificationReliable: true, TaskFingerprint: bytes.Repeat([]byte{2}, 32),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "fast" || binding.ModelLocked ||
		binding.TaskType != "simple" || binding.Difficulty != DifficultyEasy {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreSameTaskFallbackKeepsLastUsableClassification(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "fallback-tool-round", 8, "answer")
	fingerprint := bytes.Repeat([]byte{1}, 32)
	for _, binding := range []SessionBinding{
		{
			ProfileID: 8, Route: "strong", Purpose: "answer", TaskType: "reasoning",
			Difficulty: DifficultyHard, Model: "strong", QualityScoreBPS: 10_000,
			Strategy: "20260812-001", ClassificationConfidenceBPS: 7800,
			ClassificationReliable: true, TaskFingerprint: fingerprint,
		},
		{
			ProfileID: 8, Route: "strong", Purpose: "answer", TaskType: "unknown",
			Difficulty: DifficultyUnknown, Model: "strong", QualityScoreBPS: 10_000,
			Strategy: "20260812-001", TaskFingerprint: fingerprint,
		},
	} {
		if err := store.Bind(context.Background(), key, binding, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.TaskType != "reasoning" || binding.Difficulty != DifficultyHard {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreCanLockFromCurrentSessionCacheReadTokens(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "cache-session", 8, "answer")
	for task := byte(1); task <= 3; task++ {
		cacheRead := 0
		if task == 3 {
			cacheRead = 120_000
		}
		if err := store.Bind(context.Background(), key, SessionBinding{
			ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "coding",
			Difficulty: DifficultyMedium, Model: "fast", QualityScoreBPS: 9200,
			Strategy: "20260812-001", ClassificationConfidenceBPS: 9000,
			ClassificationReliable: true,
			ConversationTokens:     1000, CacheReadTokens: cacheRead,
			TaskFingerprint: bytes.Repeat([]byte{task}, 32),
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || !binding.ModelLocked || binding.LockReason != SessionLockReasonStableConversation {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreInvalidatesLegacyWholeInputLock(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "legacy-lock", 8, "answer")
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO routing_session_bindings (
			key_hash, profile_id, route_id, purpose, task_type, difficulty, model,
			quality_score_bps, strategy_name, created_at, updated_at, expires_at,
			task_fingerprint, stable_task_count, stable_confidence_bps,
			model_locked, lock_reason, last_input_tokens
		) VALUES (?, 8, 'balanced', 'answer', 'coding', 'medium', 'fast', 9200,
			'20260812-001', ?, ?, ?, ?, 3, 9000, 1, 'stable_session_model', 16000)
	`, key[:], formatSessionTime(now), formatSessionTime(now), formatSessionTime(now.Add(time.Hour)), bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.ModelLocked || binding.LockReason != "" ||
		binding.StableTaskCount != 0 || binding.ConversationTokens != 0 {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreRejectsBindingFromDifferentRuntimeIdentity(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "runtime", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "coding",
		Difficulty: DifficultyMedium, Model: "fast", QualityScoreBPS: 9200,
		Strategy: "20260813-001", RuntimeRevision: 4, PolicyVersionID: 10,
		ModelCatalogRevision: 3,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetForRuntime(context.Background(), key, 5, 11, 4); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_session_bindings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale binding count=%d", count)
	}
}

func TestSessionStoreKeepsLockedBindingAcrossPolicyRevision(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "runtime", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "coding",
		Difficulty: DifficultyMedium, Model: "strong", QualityScoreBPS: 10_000,
		Strategy: "20260813-001", HighestModel: true, RuntimeRevision: 4,
		PolicyVersionID: 10, ModelCatalogRevision: 3,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.GetForRuntime(context.Background(), key, 5, 11, 3)
	if err != nil || !found || !binding.ModelLocked || binding.Model != "strong" {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "strong", Purpose: "answer", TaskType: "coding",
		Difficulty: DifficultyMedium, Model: "strong", QualityScoreBPS: 10_000,
		Strategy: "20260813-001", RuntimeRevision: 5, PolicyVersionID: 11,
		ModelCatalogRevision: 3,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err = store.GetForRuntime(context.Background(), key, 5, 11, 3)
	if err != nil || !found || !binding.ModelLocked || binding.Model != "strong" ||
		binding.Route != "strong" || binding.RuntimeRevision != 5 || binding.PolicyVersionID != 11 {
		t.Fatalf("updated binding=%+v found=%v err=%v", binding, found, err)
	}
}

func TestSessionStoreRejectsLockedBindingAfterModelCatalogRevision(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSessionStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	insertRoutingSessionProfile(t, db, 8)
	key, _ := store.Key(http.Header{"Authorization": {"Bearer caller-secret"}}, "runtime", 8, "answer")
	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer", TaskType: "coding",
		Difficulty: DifficultyMedium, Model: "strong", QualityScoreBPS: 10_000,
		Strategy: "20260813-001", HighestModel: true, RuntimeRevision: 4,
		PolicyVersionID: 10, ModelCatalogRevision: 3,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetForRuntime(context.Background(), key, 5, 11, 4); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func insertRoutingSessionProfile(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (?, ?, 'Session Test', 1, '{}', ?, ?)
	`, id, "session-test", now, now); err != nil {
		t.Fatal(err)
	}
}
