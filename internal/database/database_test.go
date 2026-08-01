package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesPrivateDatabaseAndSchema(t *testing.T) {
	dataDir := t.TempDir()
	db, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{
		"app_settings", "admin_account", "admin_sessions", "profiles", "usage",
		"routing_traces", "routing_session_bindings", "routing_strategies",
		"routing_strategy_pointers", "routing_strategy_events",
		"routing_evaluation_budgets", "routing_quality_evidence",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode=%q", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d", foreignKeys)
	}

	info, err := os.Stat(filepath.Join(dataDir, filename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%#o", info.Mode().Perm())
	}
}

func TestOpenRejectsEmptyDataDir(t *testing.T) {
	if _, err := Open(" \t"); err == nil {
		t.Fatal("Open succeeded with an empty data directory")
	}
}

func TestOpenSecuresExistingDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode=%#o", info.Mode().Perm())
	}
}

func TestOpenMigratesOnlyOnce(t *testing.T) {
	dataDir := t.TempDir()
	for range 2 {
		db, err := Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}

		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if version != 5 {
			db.Close()
			t.Fatalf("user_version=%d", version)
		}

		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM app_settings`).Scan(&count); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if count != 1 {
			db.Close()
			t.Fatalf("app_settings rows=%d", count)
		}
		var key []byte
		if err := db.QueryRow(`SELECT routing_session_hmac_key FROM app_settings WHERE id = 1`).Scan(&key); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if len(key) != 32 {
			db.Close()
			t.Fatalf("routing session HMAC key length=%d", len(key))
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateUpgradesV1WithoutReplacingExistingData(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range schemaV1 {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (7, 'legacy', 'Legacy', 1, '{}', 'now', 'now');
		PRAGMA user_version = 1;
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version, profiles, settings int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE slug = 'legacy'`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_settings`).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if version != 5 || profiles != 1 || settings != 1 {
		t.Fatalf("version=%d profiles=%d settings=%d", version, profiles, settings)
	}
}
