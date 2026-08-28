package database

import (
	"context"
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
	`CREATE TABLE provider_accounts (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        slug         TEXT NOT NULL UNIQUE,
        display_name TEXT NOT NULL,
        enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        protocol     TEXT NOT NULL CHECK (protocol IN ('anthropic', 'openai')),
        upstream     TEXT NOT NULL,
        auth_header  TEXT NOT NULL,
        secret_value TEXT NOT NULL,
        created_at   TEXT NOT NULL,
        updated_at   TEXT NOT NULL
    )`,
	`CREATE TABLE aggregate_gateways (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        slug         TEXT NOT NULL UNIQUE,
        display_name TEXT NOT NULL,
        enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        protocol     TEXT NOT NULL CHECK (protocol IN ('anthropic', 'openai')),
        created_at   TEXT NOT NULL,
        updated_at   TEXT NOT NULL
    )`,
	`CREATE TABLE aggregate_model_routes (
        id                  INTEGER PRIMARY KEY AUTOINCREMENT,
        gateway_id          INTEGER NOT NULL REFERENCES aggregate_gateways(id) ON DELETE CASCADE,
        public_model        TEXT NOT NULL,
        provider_account_id INTEGER NOT NULL REFERENCES provider_accounts(id),
        provider_model      TEXT NOT NULL,
        enabled             INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        created_at          TEXT NOT NULL,
        updated_at          TEXT NOT NULL,
        UNIQUE (gateway_id, public_model)
    )`,
	`CREATE TABLE aggregate_issued_keys (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        gateway_id   INTEGER NOT NULL REFERENCES aggregate_gateways(id) ON DELETE CASCADE,
        name         TEXT NOT NULL,
        prefix       TEXT NOT NULL,
        last_four    TEXT NOT NULL,
        token_hash   BLOB NOT NULL UNIQUE,
        enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        expires_at   TEXT,
        last_used_at TEXT,
        created_at   TEXT NOT NULL,
        updated_at   TEXT NOT NULL
    )`,
	`CREATE INDEX aggregate_model_routes_gateway_idx ON aggregate_model_routes(gateway_id)`,
	`CREATE INDEX aggregate_model_routes_public_model_idx ON aggregate_model_routes(gateway_id, public_model)`,
	`CREATE INDEX aggregate_issued_keys_gateway_idx ON aggregate_issued_keys(gateway_id)`,
	`CREATE INDEX aggregate_issued_keys_token_hash_idx ON aggregate_issued_keys(token_hash)`,
}

var schemaV3 = []string{
	`CREATE TABLE provider_models (
        id                  INTEGER PRIMARY KEY AUTOINCREMENT,
        provider_account_id INTEGER NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
        model_id            TEXT NOT NULL,
        display_name        TEXT NOT NULL DEFAULT '',
        enabled             INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        created_at          TEXT NOT NULL,
        updated_at          TEXT NOT NULL,
        UNIQUE (provider_account_id, model_id)
    )`,
	`INSERT OR IGNORE INTO provider_models (
        provider_account_id, model_id, display_name, enabled, created_at, updated_at
    )
    SELECT
        provider_account_id,
        provider_model,
        provider_model,
        1,
        MIN(created_at),
        MIN(updated_at)
    FROM aggregate_model_routes
    GROUP BY provider_account_id, provider_model`,
	`CREATE INDEX provider_models_provider_idx ON provider_models(provider_account_id)`,
}

var schemaV4 = []string{
	`CREATE TABLE provider_accounts_v4 (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        slug         TEXT NOT NULL,
        display_name TEXT NOT NULL,
        enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
        protocol     TEXT NOT NULL CHECK (protocol IN ('anthropic', 'openai')),
        upstream     TEXT NOT NULL,
        auth_header  TEXT NOT NULL,
        secret_value TEXT NOT NULL,
        created_at   TEXT NOT NULL,
        updated_at   TEXT NOT NULL,
        UNIQUE (protocol, slug)
    )`,
	`INSERT INTO provider_accounts_v4 (
        id, slug, display_name, enabled, protocol, upstream, auth_header,
        secret_value, created_at, updated_at
    )
    SELECT
        id, slug, display_name, enabled, protocol, upstream, auth_header,
        secret_value, created_at, updated_at
    FROM provider_accounts`,
	`DROP TABLE provider_accounts`,
	`ALTER TABLE provider_accounts_v4 RENAME TO provider_accounts`,
}

var schemaV5 = []string{
	`ALTER TABLE usage ADD COLUMN gateway_id INTEGER REFERENCES aggregate_gateways(id) ON DELETE SET NULL`,
	`ALTER TABLE usage ADD COLUMN gateway_slug TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE usage ADD COLUMN issued_key_id INTEGER REFERENCES aggregate_issued_keys(id) ON DELETE SET NULL`,
	`ALTER TABLE usage ADD COLUMN issued_key_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE usage ADD COLUMN issued_key_prefix TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE usage ADD COLUMN issued_key_last_four TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX usage_gateway_id_idx ON usage(gateway_id)`,
	`CREATE INDEX usage_issued_key_id_idx ON usage(issued_key_id)`,
}

func Migrate(db *sql.DB) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign key setting: %w", err)
	}
	if foreignKeys != 0 {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys for migration: %w", err)
		}
		defer func() {
			if _, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err == nil && restoreErr != nil {
				err = fmt.Errorf("restore foreign keys after migration: %w", restoreErr)
			}
		}()
	}

	tx, err := conn.BeginTx(ctx, nil)
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
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}

	for version < schemaVersion {
		nextVersion := version + 1
		for _, statement := range schemaForVersion(nextVersion) {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema version %d: %w", nextVersion, err)
			}
		}
		if nextVersion == 1 {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.Exec(
				`INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, ?, ?)`,
				now,
				now,
			); err != nil {
				return fmt.Errorf("create app settings: %w", err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, nextVersion)); err != nil {
			return fmt.Errorf("set schema version %d: %w", nextVersion, err)
		}
		version = nextVersion
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	if foreignKeys != 0 {
		rows, checkErr := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if checkErr != nil {
			return fmt.Errorf("check foreign keys after migration: %w", checkErr)
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("foreign key check failed after migration")
		}
		if checkErr := rows.Err(); checkErr != nil {
			return fmt.Errorf("read foreign key check after migration: %w", checkErr)
		}
	}
	return nil
}

func schemaForVersion(version int) []string {
	switch version {
	case 1:
		return schemaV1
	case 2:
		return schemaV2
	case 3:
		return schemaV3
	case 4:
		return schemaV4
	case 5:
		return schemaV5
	default:
		return nil
	}
}
