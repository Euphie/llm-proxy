package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
)

func TestStoreSaveDefaultCopyAndDelete(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first, err := store.Save(ctx, SaveInput{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("first=%+v", first)
	}
	records, defaultID, err := store.LoadSnapshot(ctx)
	if err != nil || len(records) != 1 || defaultID != first.ID {
		t.Fatalf("snapshot records=%v default=%d err=%v", records, defaultID, err)
	}

	copied, err := store.Copy(ctx, first.ID, "coding-copy", "Coding Copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == first.ID || copied.Config.Upstream != first.Config.Upstream {
		t.Fatalf("copy=%+v", copied)
	}
	if err := store.Delete(ctx, first.ID, copied.ID); err != nil {
		t.Fatal(err)
	}
	records, defaultID, err = store.LoadSnapshot(ctx)
	if err != nil || len(records) != 1 || records[0].ID != copied.ID || defaultID != copied.ID {
		t.Fatalf("snapshot records=%v default=%d err=%v", records, defaultID, err)
	}
}

func TestStorePreservesExplicitEmptyRiskPatternLists(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	record := validAutoRoutingRecord()
	record.Config.AutoRouting.RiskPolicy = RiskPolicyConfig{
		SensitiveTextPatterns: []string{}, SensitiveToolPatterns: []string{},
		LongContextThresholdBPS: 7500,
	}
	if _, err := store.Save(context.Background(), SaveInput{
		Slug: record.Slug, DisplayName: record.DisplayName, Enabled: true,
		Config: record.Config,
	}, true); err != nil {
		t.Fatal(err)
	}
	records, _, err := store.LoadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy := records[0].Config.AutoRouting.RiskPolicy
	if policy.SensitiveTextPatterns == nil || policy.SensitiveToolPatterns == nil ||
		len(policy.SensitiveTextPatterns) != 0 || len(policy.SensitiveToolPatterns) != 0 {
		t.Fatalf("stored policy=%+v", policy)
	}
}

func TestStoreDuplicateSlugRollsBackDefaultChange(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	saveStoreRecord(t, store, "first", true)
	second := saveStoreRecord(t, store, "second", false)
	if err := store.SetDefault(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	_, err := store.Save(ctx, SaveInput{
		Slug: "first", DisplayName: "Duplicate", Enabled: true,
		Config: NewConfig(ProtocolOpenAI, "https://duplicate.example"),
	}, true)
	if !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("Save error=%v", err)
	}
	records, defaultID, err := store.LoadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || defaultID != second.ID {
		t.Fatalf("records=%v default=%d second=%d", records, defaultID, second.ID)
	}
}

// Break caught: mapping an unrelated SQLite UNIQUE constraint to the public slug-conflict error.
func TestTranslateWriteErrorRejectsUnrelatedUniqueConstraint(t *testing.T) {
	db := openStoreTestDB(t)
	if _, err := db.Exec(`CREATE TABLE constraint_probe (
		value TEXT NOT NULL UNIQUE
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO constraint_probe (value) VALUES ('duplicate')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO constraint_probe (value) VALUES ('duplicate')`)
	if err == nil {
		t.Fatal("duplicate insert unexpectedly succeeded")
	}
	translated := translateWriteError(context.Background(), tx, "available", 0, err)
	if errors.Is(translated, ErrSlugConflict) {
		t.Fatalf("translateWriteError=%v, unexpectedly matches ErrSlugConflict", translated)
	}
	if !errors.Is(translated, err) {
		t.Fatalf("translateWriteError=%v does not retain write error %v", translated, err)
	}
}

// Break caught: replacing the original write failure when slug-conflict classification itself fails.
func TestTranslateWriteErrorRetainsWriteErrorWhenConflictQueryFails(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "constraint-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE constraint_probe (
		value TEXT NOT NULL UNIQUE
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO constraint_probe (value) VALUES ('duplicate')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, writeErr := tx.Exec(`INSERT INTO constraint_probe (value) VALUES ('duplicate')`)
	if writeErr == nil {
		t.Fatal("duplicate insert unexpectedly succeeded")
	}

	translated := translateWriteError(context.Background(), tx, "missing", 0, writeErr)
	if !errors.Is(translated, writeErr) {
		t.Fatalf("translateWriteError=%v does not retain write error %v", translated, writeErr)
	}
	if errors.Is(translated, ErrSlugConflict) {
		t.Fatalf("translateWriteError=%v unexpectedly matches ErrSlugConflict", translated)
	}
}

func TestStoreSaveUpdatesExistingRecord(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	record := saveStoreRecord(t, store, "coding", true)
	updated, err := store.Save(ctx, SaveInput{
		ID: record.ID, Slug: "review", DisplayName: "Code Review", Enabled: true,
		Config: NewConfig(ProtocolOpenAI, "https://review.example/v1/"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != record.ID || updated.Slug != "review" ||
		updated.DisplayName != "Code Review" || updated.Config.Protocol != ProtocolOpenAI {
		t.Fatalf("updated=%+v", updated)
	}
	if !updated.CreatedAt.Equal(record.CreatedAt) || updated.UpdatedAt.Before(record.UpdatedAt) {
		t.Fatalf("created=%v updated=%v original=%+v", updated.CreatedAt, updated.UpdatedAt, record)
	}
	loaded, err := store.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Slug != "review" || loaded.Config.Upstream != "https://review.example/v1/" {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestStoreDeleteNonDefaultAllowsNoReplacement(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := saveStoreRecord(t, store, "first", true)
	second := saveStoreRecord(t, store, "second", false)
	if err := store.Delete(ctx, second.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error=%v", err)
	}
	_, defaultID, err := store.LoadSnapshot(ctx)
	if err != nil || defaultID != first.ID {
		t.Fatalf("default=%d err=%v", defaultID, err)
	}
}

func TestStoreDeleteNonDefaultRejectsAnExistingDisabledDefault(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := saveStoreRecord(t, store, "first", true)
	second := saveStoreRecord(t, store, "second", false)
	if _, err := db.Exec(`UPDATE profiles SET enabled = 0 WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, second.ID, 0); !errors.Is(err, ErrDefaultRequired) {
		t.Fatalf("Delete error=%v", err)
	}
	if _, err := store.Get(ctx, second.ID); err != nil {
		t.Fatalf("non-default profile was deleted: %v", err)
	}
}

func TestStoreDeleteDefaultRequiresValidEnabledReplacement(t *testing.T) {
	tests := []struct {
		name        string
		replacement func(first, enabled, disabled Record) int64
		wantErr     error
	}{
		{name: "missing", replacement: func(_, _, _ Record) int64 { return 0 }, wantErr: ErrDefaultRequired},
		{name: "unknown", replacement: func(_, _, _ Record) int64 { return 99999 }, wantErr: ErrNotFound},
		{name: "itself", replacement: func(first, _, _ Record) int64 { return first.ID }, wantErr: ErrDefaultRequired},
		{name: "disabled", replacement: func(_, _, disabled Record) int64 { return disabled.ID }, wantErr: ErrDefaultRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openStoreTestDB(t)
			store := NewStore(db)
			ctx := context.Background()
			first := saveStoreRecord(t, store, "first", true)
			enabled := saveStoreRecord(t, store, "enabled", false)
			disabled := saveStoreRecordWithEnabled(t, store, "disabled", false, false)

			err := store.Delete(ctx, first.ID, tt.replacement(first, enabled, disabled))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete error=%v", err)
			}
			if _, err := store.Get(ctx, first.ID); err != nil {
				t.Fatalf("default was deleted: %v", err)
			}
			_, defaultID, err := store.LoadSnapshot(ctx)
			if err != nil || defaultID != first.ID {
				t.Fatalf("default=%d err=%v", defaultID, err)
			}
		})
	}
}

func TestStoreRejectsDisabledDefault(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := saveStoreRecord(t, store, "first", true)
	disabled := saveStoreRecordWithEnabled(t, store, "disabled", false, false)
	if err := store.SetDefault(ctx, disabled.ID); !errors.Is(err, ErrDefaultRequired) {
		t.Fatalf("SetDefault error=%v", err)
	}
	_, err := store.Save(ctx, SaveInput{
		ID: first.ID, Slug: first.Slug, DisplayName: first.DisplayName, Enabled: false,
		Config: first.Config,
	}, false)
	if !errors.Is(err, ErrDefaultRequired) {
		t.Fatalf("disable Save error=%v", err)
	}
	_, err = store.Save(ctx, SaveInput{
		Slug: "new-disabled", DisplayName: "New Disabled", Enabled: false,
		Config: NewConfig(ProtocolAnthropic, "https://disabled.example"),
	}, true)
	if !errors.Is(err, ErrDefaultRequired) {
		t.Fatalf("new default Save error=%v", err)
	}
	_, defaultID, err := store.LoadSnapshot(ctx)
	if err != nil || defaultID != first.ID {
		t.Fatalf("default=%d err=%v", defaultID, err)
	}
}

func TestStoreSaveRejectsAnExistingDisabledDefault(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := saveStoreRecord(t, store, "first", true)
	if _, err := db.Exec(`UPDATE profiles SET enabled = 0 WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	_, err := store.Save(ctx, SaveInput{
		Slug: "second", DisplayName: "Second", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://second.example"),
	}, false)
	if !errors.Is(err, ErrDefaultRequired) {
		t.Fatalf("Save error=%v", err)
	}
	if _, err := store.Get(ctx, first.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new profile persisted: %v", err)
	}
}

func TestStoreDeleteRetainsUsageAndClearsProfileID(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := saveStoreRecord(t, store, "first", true)
	second := saveStoreRecord(t, store, "second", false)
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO usage (
		created_at, profile_id, profile_slug, protocol, request_kind
	) VALUES (?, ?, ?, ?, ?)`, createdAt, second.ID, second.Slug, "anthropic", "main"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, second.ID, 0); err != nil {
		t.Fatal(err)
	}
	var (
		count     int
		profileID sql.NullInt64
	)
	if err := db.QueryRow(`SELECT COUNT(*), profile_id FROM usage WHERE profile_slug = ?`, second.Slug).
		Scan(&count, &profileID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || profileID.Valid {
		t.Fatalf("count=%d profileID=%v default=%d", count, profileID, first.ID)
	}
}

func TestStoreRejectsInvalidInputBeforePersisting(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Save(ctx, SaveInput{
		Slug: "Bad Slug", DisplayName: "Bad", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}, true)
	if !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("Save error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("profiles=%d", count)
	}
}

func TestStoreRoundTripsModelCapabilities(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	contextWindow := 128000
	maxOutputTokens := 8192
	supportsVision := true
	doesNotSupportVision := false
	config := NewConfig(ProtocolAnthropic, "https://example.test")
	config.Models = []ModelCapabilityConfig{
		{ID: "GLM-5", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens, SupportsVision: &supportsVision},
		{ID: "glm-5", SupportsVision: &doesNotSupportVision},
	}
	config.Vision.UnlistedModelPolicy = UnlistedModelEnhance

	saved, err := store.Save(context.Background(), SaveInput{
		Slug: "models", DisplayName: "Models", Enabled: true, Config: config,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Vision.UnlistedModelPolicy != UnlistedModelEnhance || len(loaded.Config.Models) != 2 {
		t.Fatalf("config=%+v", loaded.Config)
	}
	first := loaded.Config.Models[0]
	if first.ID != "GLM-5" || first.ContextWindow == nil || *first.ContextWindow != contextWindow ||
		first.MaxOutputTokens == nil || *first.MaxOutputTokens != maxOutputTokens ||
		first.SupportsVision == nil || !*first.SupportsVision {
		t.Fatalf("first=%+v", first)
	}
	second := loaded.Config.Models[1]
	if second.ID != "glm-5" || second.ContextWindow != nil || second.MaxOutputTokens != nil ||
		second.SupportsVision == nil || *second.SupportsVision {
		t.Fatalf("second=%+v", second)
	}
}

func TestStoreRejectsUnsupportedPersistedConfigVersion(t *testing.T) {
	db := openStoreTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	config := NewConfig(ProtocolAnthropic, "https://example.test")
	config.Version = 2
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO profiles (
		slug, display_name, enabled, config_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`, "future", "Future", 1, string(payload), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE app_settings SET default_profile_id = ?, updated_at = ? WHERE id = 1`,
		id,
		now,
	); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if _, err := store.Get(ctx, id); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Get error=%v", err)
	}
	if _, _, err := store.LoadSnapshot(ctx); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("LoadSnapshot error=%v", err)
	}
}

func TestStoreReturnsNotFound(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Save(ctx, SaveInput{
		ID: 42, Slug: "missing", DisplayName: "Missing", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://missing.example"),
	}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update Save error=%v", err)
	}
	if _, err := store.Get(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error=%v", err)
	}
	if _, err := store.Copy(ctx, 42, "copy", "Copy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Copy error=%v", err)
	}
	if err := store.SetDefault(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDefault error=%v", err)
	}
	if err := store.Delete(ctx, 42, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete error=%v", err)
	}
}

func TestStorePersistsOnlyProfileConfiguration(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	config := NewConfig(ProtocolAnthropic, "https://user:secret@example.test")

	if _, err := store.Save(ctx, SaveInput{
		Slug: "secure", DisplayName: "Secure", Enabled: true, Config: config,
	}, true); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Save error=%v", err)
	}
	config = NewConfig(ProtocolAnthropic, "https://example.test")
	config.Vision.Prompt = "safe prompt"
	if _, err := store.Save(ctx, SaveInput{
		Slug: "secure", DisplayName: "Secure", Enabled: true, Config: config,
	}, true); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT config_json FROM profiles WHERE slug = ?`, "secure").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Authorization", "secret", "request_body", "auth_header"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("config_json contains %q: %s", forbidden, raw)
		}
	}
}

func openStoreTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db
}

func saveStoreRecord(t *testing.T, store *Store, slug string, makeDefault bool) Record {
	t.Helper()
	return saveStoreRecordWithEnabled(t, store, slug, true, makeDefault)
}

func saveStoreRecordWithEnabled(
	t *testing.T,
	store *Store,
	slug string,
	enabled bool,
	makeDefault bool,
) Record {
	t.Helper()
	record, err := store.Save(context.Background(), SaveInput{
		Slug: slug, DisplayName: strings.ToUpper(slug), Enabled: enabled,
		Config: NewConfig(ProtocolAnthropic, "https://"+slug+".example"),
	}, makeDefault)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
