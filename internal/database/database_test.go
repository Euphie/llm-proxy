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
		"provider_accounts", "aggregate_gateways", "aggregate_model_routes",
		"provider_models", "aggregate_issued_keys",
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

func TestUsageSchemaIncludesAggregateDimensions(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`PRAGMA table_info(usage)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"gateway_id", "gateway_slug", "issued_key_id", "issued_key_name",
		"issued_key_prefix", "issued_key_last_four",
	} {
		if !columns[name] {
			t.Fatalf("usage column %q is missing", name)
		}
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
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateScopesProviderSlugByProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, statement := range []string{
		`CREATE TABLE provider_accounts (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            slug TEXT NOT NULL UNIQUE,
            display_name TEXT NOT NULL,
            enabled INTEGER NOT NULL,
            protocol TEXT NOT NULL,
            upstream TEXT NOT NULL,
            auth_header TEXT NOT NULL,
            secret_value TEXT NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE provider_models (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            provider_account_id INTEGER NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
            model_id TEXT NOT NULL,
            display_name TEXT NOT NULL,
            enabled INTEGER NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE aggregate_gateways (
            id INTEGER PRIMARY KEY AUTOINCREMENT
        )`,
		`CREATE TABLE aggregate_issued_keys (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            gateway_id INTEGER NOT NULL
        )`,
		`CREATE TABLE usage (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at TEXT NOT NULL,
            profile_id INTEGER,
            profile_slug TEXT NOT NULL,
            protocol TEXT NOT NULL,
            request_kind TEXT NOT NULL,
            model TEXT NOT NULL DEFAULT '',
            path TEXT NOT NULL DEFAULT '',
            input_tokens INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            cache_read_tokens INTEGER NOT NULL DEFAULT 0,
            cache_creation_tokens INTEGER NOT NULL DEFAULT 0
        )`,
		`INSERT INTO provider_accounts (
            id, slug, display_name, enabled, protocol, upstream, auth_header,
            secret_value, created_at, updated_at
        ) VALUES (
            7, 'primary', 'OpenAI Primary', 1, 'openai', 'https://openai.example',
            'Authorization', 'secret', '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z'
        )`,
		`INSERT INTO provider_models (
            provider_account_id, model_id, display_name, enabled, created_at, updated_at
        ) VALUES (
            7, 'gpt-4o', 'GPT-4o', 1, '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z'
        )`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO provider_accounts (
        slug, display_name, enabled, protocol, upstream, auth_header,
        secret_value, created_at, updated_at
    ) VALUES (
        'primary', 'Anthropic Primary', 1, 'anthropic', 'https://anthropic.example',
        'x-api-key', 'secret', '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z'
    )`); err != nil {
		t.Fatalf("same slug in another protocol: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO provider_accounts (
        slug, display_name, enabled, protocol, upstream, auth_header,
        secret_value, created_at, updated_at
    ) VALUES (
        'primary', 'Duplicate OpenAI', 1, 'openai', 'https://duplicate.example',
        'Authorization', 'secret', '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z'
    )`); err == nil {
		t.Fatal("same slug in the same protocol was accepted")
	}

	var modelCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_models WHERE provider_account_id = 7`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if modelCount != 1 {
		t.Fatalf("provider model count=%d", modelCount)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 5 {
		t.Fatalf("user_version=%d", version)
	}
}
