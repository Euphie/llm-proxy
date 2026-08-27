package modeldirectory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Euphie/llm-proxy/internal/database"
)

func TestStoreListsNormalizedModelsIncludingTombstones(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'models', 'Models', 1, '{"version":2}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 1, NULL, 1, '2026-08-13T00:00:00Z');
		INSERT INTO profile_models (
		  profile_id, model_id, capability_json, status, status_reason,
		  created_at, updated_at, retired_at
		) VALUES
		  (1, 'available', '{"id":"available","supports_tools":true}', 'available', '',
		   '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', NULL),
		  (1, 'retired', '{"id":"retired"}', 'retired', 'removed by operator',
		   '2026-08-13T00:00:00Z', '2026-08-13T01:00:00Z', '2026-08-13T01:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	models, err := store.List(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ModelID != "available" || models[1].ModelID != "retired" {
		t.Fatalf("models=%+v", models)
	}
	if models[0].Status != StatusAvailable || models[1].Status != StatusRetired ||
		models[1].RetiredAt == nil || models[1].StatusReason != "removed by operator" {
		t.Fatalf("models=%+v", models)
	}
	var capability map[string]any
	if err := json.Unmarshal(models[0].CapabilityJSON, &capability); err != nil {
		t.Fatal(err)
	}
	if capability["id"] != "available" || capability["supports_tools"] != true {
		t.Fatalf("capability=%v", capability)
	}

	_, err = store.Get(context.Background(), 1, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error=%v", err)
	}
}

func TestStoreRejectsInvalidPersistedStatus(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	_, err = scanRecord(fakeRow{values: []any{
		int64(1), "model", []byte(`{"id":"model"}`), "broken", "", "2026-08-13T00:00:00Z",
		"2026-08-13T00:00:00Z", sql.NullString{},
	}})
	if err == nil {
		t.Fatal("expected invalid status error")
	}
	_ = store
}

type fakeRow struct {
	values []any
}

func (row fakeRow) Scan(dest ...any) error {
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *int64:
			*target = value.(int64)
		case *string:
			*target = value.(string)
		case *[]byte:
			*target = append([]byte(nil), value.([]byte)...)
		case *sql.NullString:
			*target = value.(sql.NullString)
		}
	}
	return nil
}
