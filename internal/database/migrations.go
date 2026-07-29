package database

import (
	"database/sql"
	"fmt"
	"time"
)

const schemaVersion = 1

var schemaV1 = []string{
	`CREATE TABLE admin_account (
        id                   INTEGER PRIMARY KEY CHECK (id = 1),
        username             TEXT NOT NULL UNIQUE,
        password_hash        TEXT NOT NULL,
        must_change_password INTEGER NOT NULL CHECK (must_change_password IN (0, 1)),
        auth_version         INTEGER NOT NULL DEFAULT 1,
        updated_at           TEXT NOT NULL
    )`,
	`CREATE TABLE admin_sessions (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        token_hash   BLOB NOT NULL UNIQUE,
        csrf_hash    BLOB NOT NULL,
        auth_version INTEGER NOT NULL,
        created_at   TEXT NOT NULL,
        expires_at   TEXT NOT NULL
    )`,
	`CREATE TABLE profiles (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        slug         TEXT NOT NULL UNIQUE,
        display_name TEXT NOT NULL,
        enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        config_json  TEXT NOT NULL,
        created_at   TEXT NOT NULL,
        updated_at   TEXT NOT NULL
    )`,
	`CREATE TABLE app_settings (
        id                 INTEGER PRIMARY KEY CHECK (id = 1),
        default_profile_id INTEGER REFERENCES profiles(id),
        created_at         TEXT NOT NULL,
        updated_at         TEXT NOT NULL
    )`,
	`CREATE TABLE usage (
        id                    INTEGER PRIMARY KEY AUTOINCREMENT,
        created_at            TEXT NOT NULL,
        profile_id            INTEGER REFERENCES profiles(id) ON DELETE SET NULL,
        profile_slug          TEXT NOT NULL,
        protocol              TEXT NOT NULL,
        request_kind          TEXT NOT NULL CHECK (request_kind IN ('main', 'vision')),
        model                 TEXT NOT NULL DEFAULT '',
        path                  TEXT NOT NULL DEFAULT '',
        input_tokens          INTEGER NOT NULL DEFAULT 0,
        output_tokens         INTEGER NOT NULL DEFAULT 0,
        cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
        cache_creation_tokens INTEGER NOT NULL DEFAULT 0
    )`,
	`CREATE INDEX usage_created_at_idx ON usage(created_at)`,
	`CREATE INDEX usage_profile_id_idx ON usage(profile_id)`,
	`CREATE INDEX usage_model_idx ON usage(model)`,
	`CREATE INDEX usage_request_kind_idx ON usage(request_kind)`,
}

func Migrate(db *sql.DB) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version >= schemaVersion {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration: %w", err)
		}
		return nil
	}

	for _, statement := range schemaV1 {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, ?, ?)`,
		now,
		now,
	); err != nil {
		return fmt.Errorf("create app settings: %w", err)
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
