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
	key, ok := store.Key(headers, "agent-session-42", 8, "balanced", "answer")
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}
	same, ok := store.Key(headers, "agent-session-42", 8, "balanced", "answer")
	if !ok || key != same {
		t.Fatal("Session key is not deterministic")
	}
	other, ok := store.Key(headers, "agent-session-42", 8, "strong", "answer")
	if !ok || key == other {
		t.Fatal("Route did not scope Session key")
	}
	otherAuth, ok := store.Key(
		http.Header{"Authorization": {"Bearer another-caller"}},
		"agent-session-42", 8, "balanced", "answer",
	)
	if !ok || key == otherAuth {
		t.Fatal("authentication domain did not scope Session key")
	}
	if _, ok := store.Key(http.Header{}, "agent-session-42", 8, "balanced", "answer"); ok {
		t.Fatal("anonymous Session identity was accepted")
	}

	if err := store.Bind(context.Background(), key, SessionBinding{
		ProfileID: 8, Route: "balanced", Purpose: "answer",
		Model: "fast", QualityScoreBPS: 9200, Strategy: "20260802-001",
	}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	binding, found, err := store.Get(context.Background(), key)
	if err != nil || !found || binding.Model != "fast" || binding.QualityScoreBPS != 9200 {
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
		"agent-session-42", 8, "balanced", "answer",
	)
	if !ok {
		t.Fatal("valid Session identity was rejected")
	}

	bind := func(model string, quality int) {
		t.Helper()
		if err := store.Bind(context.Background(), key, SessionBinding{
			ProfileID: 8, Route: "balanced", Purpose: "answer",
			Model: model, QualityScoreBPS: quality, Strategy: "20260802-001",
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
