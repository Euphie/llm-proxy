package database

import (
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
		if version != 1 {
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
