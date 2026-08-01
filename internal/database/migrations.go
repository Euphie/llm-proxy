package database

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

const schemaVersion = 5

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

var schemaV3 = []string{
	`ALTER TABLE app_settings ADD COLUMN routing_session_hmac_key BLOB`,
	`CREATE TABLE routing_session_bindings (
        key_hash          BLOB PRIMARY KEY CHECK (length(key_hash) = 32),
        profile_id        INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        route_id          TEXT NOT NULL,
        purpose           TEXT NOT NULL,
        model             TEXT NOT NULL,
        quality_score_bps INTEGER NOT NULL CHECK (quality_score_bps BETWEEN 0 AND 10000),
        strategy_name     TEXT NOT NULL,
        created_at        TEXT NOT NULL,
        updated_at        TEXT NOT NULL,
        expires_at        TEXT NOT NULL
    )`,
	`CREATE INDEX routing_session_bindings_profile_idx ON routing_session_bindings(profile_id)`,
	`CREATE INDEX routing_session_bindings_expires_idx ON routing_session_bindings(expires_at)`,
}

var schemaV4 = []string{
	`CREATE TABLE routing_strategies (
        id          INTEGER PRIMARY KEY AUTOINCREMENT,
        profile_id  INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        name        TEXT NOT NULL,
        alias       TEXT NOT NULL DEFAULT '',
        state       TEXT NOT NULL CHECK (state IN ('draft', 'evaluating', 'ready', 'canary', 'active')),
        config_json TEXT NOT NULL,
        created_at  TEXT NOT NULL,
        updated_at  TEXT NOT NULL,
        UNIQUE(profile_id, name)
    )`,
	`CREATE INDEX routing_strategies_profile_idx ON routing_strategies(profile_id)`,
	`CREATE INDEX routing_strategies_state_idx ON routing_strategies(profile_id, state)`,
	`CREATE TABLE routing_strategy_pointers (
        profile_id                 INTEGER PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
        active_strategy_id         INTEGER NOT NULL REFERENCES routing_strategies(id),
        canary_strategy_id         INTEGER REFERENCES routing_strategies(id),
        last_known_good_strategy_id INTEGER REFERENCES routing_strategies(id),
        canary_bps                 INTEGER NOT NULL DEFAULT 0 CHECK (canary_bps BETWEEN 0 AND 9999),
        revision                   INTEGER NOT NULL CHECK (revision > 0),
        updated_at                 TEXT NOT NULL,
        CHECK (
            (canary_strategy_id IS NULL AND canary_bps = 0) OR
            (canary_strategy_id IS NOT NULL AND canary_bps > 0)
        )
    )`,
	`CREATE TABLE routing_strategy_events (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        profile_id   INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        strategy_id  INTEGER REFERENCES routing_strategies(id) ON DELETE SET NULL,
        action       TEXT NOT NULL,
        from_state   TEXT NOT NULL DEFAULT '',
        to_state     TEXT NOT NULL DEFAULT '',
        revision     INTEGER NOT NULL CHECK (revision > 0),
        created_at   TEXT NOT NULL
    )`,
	`CREATE INDEX routing_strategy_events_profile_idx ON routing_strategy_events(profile_id, created_at)`,
}

var schemaV5 = []string{
	`CREATE TABLE routing_evaluation_budgets (
        profile_id        INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        budget_day        TEXT NOT NULL,
        reserved_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK (reserved_micro_usd >= 0),
        spent_micro_usd   INTEGER NOT NULL DEFAULT 0 CHECK (spent_micro_usd >= 0),
        updated_at        TEXT NOT NULL,
        PRIMARY KEY(profile_id, budget_day)
    )`,
	`CREATE TABLE routing_quality_evidence (
        profile_id                    INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        evidence_day                  TEXT NOT NULL,
        strategy_name                 TEXT NOT NULL,
        route_id                      TEXT NOT NULL,
        task_type                     TEXT NOT NULL,
        candidate_model               TEXT NOT NULL,
        reference_model               TEXT NOT NULL,
        reviewer_model                TEXT NOT NULL,
        samples                       INTEGER NOT NULL DEFAULT 0 CHECK (samples >= 0),
        candidate_wins                INTEGER NOT NULL DEFAULT 0 CHECK (candidate_wins >= 0),
        ties                          INTEGER NOT NULL DEFAULT 0 CHECK (ties >= 0),
        reference_wins                INTEGER NOT NULL DEFAULT 0 CHECK (reference_wins >= 0),
        severe_errors                 INTEGER NOT NULL DEFAULT 0 CHECK (severe_errors >= 0),
        deterministic_failures        INTEGER NOT NULL DEFAULT 0 CHECK (deterministic_failures >= 0),
        candidate_cost_micro_usd      INTEGER NOT NULL DEFAULT 0 CHECK (candidate_cost_micro_usd >= 0),
        reference_cost_micro_usd      INTEGER NOT NULL DEFAULT 0 CHECK (reference_cost_micro_usd >= 0),
        reviewer_cost_micro_usd       INTEGER NOT NULL DEFAULT 0 CHECK (reviewer_cost_micro_usd >= 0),
        candidate_latency_ms          INTEGER NOT NULL DEFAULT 0 CHECK (candidate_latency_ms >= 0),
        reference_latency_ms          INTEGER NOT NULL DEFAULT 0 CHECK (reference_latency_ms >= 0),
        updated_at                    TEXT NOT NULL,
        PRIMARY KEY(
            profile_id, evidence_day, strategy_name, route_id, task_type,
            candidate_model, reference_model, reviewer_model
        )
    )`,
	`CREATE INDEX routing_quality_evidence_profile_idx
        ON routing_quality_evidence(profile_id, evidence_day)`,
	`CREATE INDEX routing_quality_evidence_route_idx
        ON routing_quality_evidence(profile_id, route_id, candidate_model)`,
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
		if err := ensureRoutingSessionHMACKey(tx); err != nil {
			return err
		}
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
	if version < 3 {
		for _, statement := range schemaV3 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v3: %w", err)
			}
		}
	}
	if version < 4 {
		for _, statement := range schemaV4 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v4: %w", err)
			}
		}
	}
	if version < 5 {
		for _, statement := range schemaV5 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v5: %w", err)
			}
		}
	}
	if err := ensureRoutingSessionHMACKey(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 5`); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func ensureRoutingSessionHMACKey(tx *sql.Tx) error {
	var key []byte
	if err := tx.QueryRow(
		`SELECT routing_session_hmac_key FROM app_settings WHERE id = 1`,
	).Scan(&key); err != nil {
		return fmt.Errorf("read routing session HMAC key: %w", err)
	}
	if len(key) == 32 {
		return nil
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate routing session HMAC key: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE app_settings SET routing_session_hmac_key = ?, updated_at = ? WHERE id = 1`,
		key,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("store routing session HMAC key: %w", err)
	}
	return nil
}
