package database

import (
	"database/sql"
	"fmt"
	"time"
)

const schemaVersion = 2

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

var schemaV2 = []string{
	`CREATE TABLE routing_traces (
        id                        INTEGER PRIMARY KEY AUTOINCREMENT,
        created_at                TEXT NOT NULL,
        profile_id                INTEGER REFERENCES profiles(id) ON DELETE SET NULL,
        profile_slug              TEXT NOT NULL,
        protocol                  TEXT NOT NULL,
        path                      TEXT NOT NULL,
        strategy_name             TEXT NOT NULL,
        route_id                  TEXT NOT NULL,
        task_type                 TEXT NOT NULL,
        risk                      TEXT NOT NULL,
        classification_source     TEXT NOT NULL,
        initial_model             TEXT NOT NULL,
        final_model               TEXT NOT NULL,
        vision_mode               TEXT NOT NULL,
        status_code               INTEGER NOT NULL CHECK (status_code BETWEEN 0 AND 599),
        client_committed          INTEGER NOT NULL CHECK (client_committed IN (0, 1)),
        answer_attempts           INTEGER NOT NULL CHECK (answer_attempts >= 0),
        auxiliary_calls           INTEGER NOT NULL CHECK (auxiliary_calls >= 0),
        total_outbound_calls      INTEGER NOT NULL CHECK (total_outbound_calls >= 0),
        model_switches            INTEGER NOT NULL CHECK (model_switches >= 0),
        target_switches           INTEGER NOT NULL CHECK (target_switches >= 0),
        planned_worst_case_cost_micro_usd INTEGER NOT NULL CHECK (planned_worst_case_cost_micro_usd >= 0),
        reserved_cost_micro_usd   INTEGER NOT NULL CHECK (reserved_cost_micro_usd >= 0),
        elapsed_ms                INTEGER NOT NULL CHECK (elapsed_ms >= 0)
    )`,
	`CREATE INDEX routing_traces_created_at_idx ON routing_traces(created_at)`,
	`CREATE INDEX routing_traces_profile_id_idx ON routing_traces(profile_id)`,
	`CREATE INDEX routing_traces_strategy_idx ON routing_traces(strategy_name)`,
	`CREATE INDEX routing_traces_route_idx ON routing_traces(route_id)`,
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

	if version < 1 {
		for _, statement := range schemaV1 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v1: %w", err)
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
	}
	if version < 2 {
		for _, statement := range schemaV2 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v2: %w", err)
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
