package database

import (
	"database/sql"
	"encoding/json"
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
		"routing_traces", "routing_session_bindings", "profile_models",
		"routing_calls",
		"routing_policy_versions", "profile_runtime_state", "routing_runtime_events",
		"routing_policy_reconcile_state",
		"routing_evaluation_budgets", "routing_quality_evidence",
		"routing_agent_trajectories",
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

func TestMigrateV25UsesPointerSelectedActivePolicyAndNormalizesModels(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "v24.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		schemaV15, schemaV16, schemaV17, schemaV18, schemaV19, schemaV20,
		schemaV21, schemaV22, schemaV23, schemaV24,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, routing_session_hmac_key, created_at, updated_at)
		VALUES (1, zeroblob(32), 'now', 'now');
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (3, 'crs2', 'CRS 2', 1, '{
		  "version": 1,
		  "protocol": "anthropic",
		  "upstream": "https://example.com/",
		  "models": [
		    {"id":"fast","context_window":100000,"max_output_tokens":8192,"supports_tools":true},
		    {"id":"strong","context_window":200000,"max_output_tokens":16384,"supports_tools":true}
		  ],
		  "auto_routing": {
		    "enabled": true,
		    "participants": ["fast","strong"],
		    "strong_baseline_model": "strong",
		    "task_analyzer_model": "fast",
		    "analyzer_timeout": "15s",
		    "session_ttl": "24h",
		    "dynamic_optimization": {"reviewer_model":"strong"}
		  },
		  "vision": {"enabled":false},
		  "overload_rules": []
		}', 'now', 'now');
		INSERT INTO routing_strategies (
		  id, profile_id, name, alias, state, config_json, created_at, updated_at
		) VALUES
		  (4, 3, '20260805-001', '', 'active', '{
		    "name":"20260805-001",
		    "roles":{"participants":["fast","strong"],"strong_baseline_model":"strong","task_analyzer_model":"fast","reviewer_model":"strong"},
		    "default_route":"general",
		    "routes":[{"id":"general","candidates":[{"model":"fast"},{"model":"strong"}]}]
		  }', 'now', 'now'),
		  (16, 3, '20260813-001', '', 'draft', '{
		    "name":"20260813-001",
		    "roles":{"participants":["strong"],"strong_baseline_model":"strong","task_analyzer_model":"strong","reviewer_model":"strong"},
		    "default_route":"strong",
		    "routes":[{"id":"strong","candidates":[{"model":"strong"}]}]
		  }', 'now', 'now');
		INSERT INTO routing_strategy_pointers (
		  profile_id, active_strategy_id, canary_strategy_id,
		  last_known_good_strategy_id, canary_bps, revision, updated_at
		) VALUES (3, 4, NULL, NULL, 0, 7, 'now');
		INSERT INTO routing_session_bindings (
		  key_hash, profile_id, route_id, purpose, model, quality_score_bps,
		  strategy_name, created_at, updated_at, expires_at
		) VALUES (zeroblob(32), 3, 'general', 'answer', 'fast', 8000,
		  '20260805-001', 'now', 'now', 'later');
		PRAGMA user_version = 24;
	`); err != nil {
		t.Fatal(err)
	}
	artifacts, err := PreparePolicyMigrationArtifacts(db, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := PreparePolicyMigrationArtifacts(db, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Directory != artifacts.Directory {
		t.Fatalf("artifact directory changed: %q != %q", reused.Directory, artifacts.Directory)
	}
	for _, path := range []string{artifacts.DatabasePath, artifacts.CandidatePath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %q mode=%#o", path, info.Mode().Perm())
		}
	}
	var candidate LegacyPolicyCandidate
	payload, err := os.ReadFile(artifacts.CandidatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &candidate); err != nil {
		t.Fatal(err)
	}
	if !candidate.Found || candidate.StrategyID != 16 || candidate.ProfileSlug != "crs2" ||
		candidate.PointerRevision != 7 {
		t.Fatalf("candidate=%+v", candidate)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	var version, runtimeRevision, activePolicyID, modelRevision int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT revision, active_policy_version_id, model_catalog_revision
		FROM profile_runtime_state WHERE profile_id = 3
	`).Scan(&runtimeRevision, &activePolicyID, &modelRevision); err != nil {
		t.Fatal(err)
	}
	var policyName string
	if err := db.QueryRow(`
		SELECT json_extract(policy_json, '$.name')
		FROM routing_policy_versions WHERE id = ?
	`, activePolicyID).Scan(&policyName); err != nil {
		t.Fatal(err)
	}
	var modelCount, sessionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_models WHERE profile_id = 3`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_session_bindings WHERE profile_id = 3`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	var profileVersion int
	var autoEnabled bool
	var modelsJSON any
	if err := db.QueryRow(`
		SELECT json_extract(config_json, '$.version'),
		       json_extract(config_json, '$.auto_routing.enabled'),
		       json_extract(config_json, '$.models')
		FROM profiles WHERE id = 3
	`).Scan(&profileVersion, &autoEnabled, &modelsJSON); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || runtimeRevision != 1 || modelRevision != 1 ||
		policyName != "20260805-001" || modelCount != 2 || sessionCount != 0 ||
		profileVersion != 2 || !autoEnabled || modelsJSON != nil {
		t.Fatalf(
			"version=%d runtime=%d active=%d models_revision=%d policy=%q models=%d sessions=%d profile_version=%d auto=%t profile_models=%v",
			version, runtimeRevision, activePolicyID, modelRevision, policyName, modelCount,
			sessionCount, profileVersion, autoEnabled, modelsJSON,
		)
	}
	for _, legacyTable := range []string{
		"routing_strategies", "routing_strategy_pointers",
		"routing_strategy_events", "routing_strategy_generations",
	} {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			legacyTable,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy table %q still exists", legacyTable)
		}
	}
}

func TestSchemaV21AddsPrivateSessionKeysToRoutingTraces(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version, columns, indexes int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('routing_traces')
		WHERE name = 'session_key' AND type = 'BLOB'
	`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_index_list('routing_traces')
		WHERE name = 'routing_traces_session_idx'
	`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || columns != 1 || indexes != 1 {
		t.Fatalf("version=%d columns=%d indexes=%d", version, columns, indexes)
	}
}

func TestMigrateV25DiscardsUnpointedLegacyStrategiesAndDraftMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v23.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		schemaV15, schemaV16, schemaV17, schemaV18, schemaV19, schemaV20,
		schemaV21, schemaV22, schemaV23,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'auto', 'Auto', 1, '{
		  "auto_routing": {
		    "participants": ["fast", "strong"],
		    "strong_baseline_model": "strong",
		    "task_analyzer_model": "fast",
		    "dynamic_optimization": {"reviewer_model": "strong"}
		  }
		}', 'now', 'now');
		INSERT INTO routing_strategies (
		  id, profile_id, name, alias, state, config_json, created_at, updated_at
		) VALUES (1, 1, '20260812-001', '', 'active', '{"name":"20260812-001"}', 'now', 'now');
		INSERT INTO routing_strategy_generations (
		  strategy_id, profile_id, generator_version, generation_intent_json,
		  source_snapshot_digest, generated_config_json, created_at, updated_at
		) VALUES (
		  1, 1, 'v1', '{}',
		  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		  '{"name":"20260812-001"}', 'now', 'now'
		);
		PRAGMA user_version = 23;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var policies, legacyTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_policy_versions WHERE profile_id = 1`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('routing_strategies', 'routing_strategy_generations')
	`).Scan(&legacyTables); err != nil {
		t.Fatal(err)
	}
	if policies != 0 || legacyTables != 0 {
		t.Fatalf("policies=%d legacy_tables=%d", policies, legacyTables)
	}
}

func TestMigrateV20AddsDurableAgentTrajectoryDeduplication(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v18.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		schemaV15, schemaV16, schemaV17, schemaV18,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		PRAGMA user_version = 18;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("version=%d", version)
	}
	wantColumns := []string{
		"id", "profile_id", "profile_slug", "protocol", "source", "status", "session_key",
		"strategy_name", "route_id", "task_type", "difficulty", "risk", "vision_mode",
		"model_path_json", "tool_calls", "turns", "elapsed_ms", "truncated", "reason_code",
		"candidate_cost_micro_usd", "evaluation_cost_micro_usd", "evidence_recorded", "evaluation_result_json", "encryption_version", "nonce",
		"ciphertext", "observation_digest", "started_at", "completed_at", "expires_at", "created_at", "updated_at",
	}
	for _, column := range wantColumns {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('routing_agent_trajectories') WHERE name = ?`,
			column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing routing_agent_trajectories.%s", column)
		}
	}
	for _, forbidden := range []string{"request", "response", "header", "authorization", "cookie", "credential"} {
		var count int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM pragma_table_info('routing_agent_trajectories')
			WHERE lower(name) LIKE '%' || ? || '%'
		`, forbidden).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("plaintext-like column contains %q", forbidden)
		}
	}
	for _, index := range []string{
		"routing_agent_trajectories_profile_idx",
		"routing_agent_trajectories_status_idx",
		"routing_agent_trajectories_expiry_idx",
		"routing_agent_trajectories_session_idx",
		"routing_agent_trajectories_observation_idx",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing index %s", index)
		}
	}
}

func TestMigrateV17LegacyGenerationMetadataIsRemovedByV25(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v16.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		schemaV15, schemaV16,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		PRAGMA user_version = 16;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version, columns int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('routing_strategy_generations')
		WHERE name IN (
			'strategy_id', 'profile_id', 'generator_version', 'generation_intent_json',
			'source_snapshot_digest', 'generated_config_json',
			'manual_override_patch_json', 'explanations_json', 'created_at', 'updated_at'
		)
	`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || columns != 0 {
		t.Fatalf("version=%d generation_columns=%d", version, columns)
	}
}

func TestMigrateV18LegacyStrategyStateIsRemovedByV25(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v17.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		schemaV15, schemaV16, schemaV17,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'test', 'Test', 1, '{}', 'now', 'now');
		INSERT INTO routing_strategies (
			id, profile_id, name, alias, state, config_json, created_at, updated_at
		) VALUES (1, 1, 'candidate', '', 'evaluating', '{}', 'now', 'now');
		PRAGMA user_version = 17;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version, tables int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'routing_strategies'
	`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || tables != 0 {
		t.Fatalf("version=%d legacy_tables=%d", version, tables)
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
		if version != schemaVersion {
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

func TestMigrationAddsRoutingDecisionObservability(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, columns := range map[string][]string{
		"routing_traces": {
			"difficulty", "classification_confidence_bps", "classification_reason_codes",
			"task_type_confidence_bps", "difficulty_confidence_bps", "risk_confidence_bps",
			"classification_underspecified", "complexity_signals",
			"estimated_input_tokens", "requested_output_tokens", "decision_reason",
		},
		"routing_candidate_decisions": {
			"trace_id", "ordinal", "model", "decision", "reason_code",
			"quality_score_bps", "severe_error_rate_bps", "expected_cost_micro_usd",
			"answer_worst_cost_micro_usd", "vision_call_cost_micro_usd",
			"vision_mode", "upstream_nodes",
		},
	} {
		for _, column := range columns {
			var count int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
			).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("%s.%s count=%d", table, column, count)
			}
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
	if version != schemaVersion || profiles != 1 || settings != 1 {
		t.Fatalf("version=%d profiles=%d settings=%d", version, profiles, settings)
	}
}

func TestMigrateV6DefaultsExistingRoutingTraceTargetsToPrimary(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v5.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for version, statements := range [][]string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO routing_traces (
			created_at, profile_slug, protocol, path, strategy_name, route_id,
			task_type, risk, classification_source, initial_model, final_model,
			vision_mode, status_code, client_committed, answer_attempts,
			auxiliary_calls, total_outbound_calls, model_switches, target_switches,
			planned_worst_case_cost_micro_usd, reserved_cost_micro_usd, elapsed_ms
		) VALUES (
			'now', 'legacy', 'anthropic', '/v1/messages', 'legacy', 'route',
			'chat', 'low', 'rule', 'fast', 'fast', 'none', 200, 1, 1,
			0, 1, 0, 0, 10, 10, 5
		);
		PRAGMA user_version = 5;
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version int
	var initialTarget, finalTarget string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT initial_target, final_target FROM routing_traces`).Scan(&initialTarget, &finalTarget); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || initialTarget != "primary" || finalTarget != "primary" {
		t.Fatalf("version=%d initial_target=%q final_target=%q", version, initialTarget, finalTarget)
	}
}

func TestMigrateClearsLegacySessionBindingsBeforePolicyRuntimeStarts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v7.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (7, 'legacy', 'Legacy', 1, '{}', 'now', 'now');
		INSERT INTO routing_session_bindings (
			key_hash, profile_id, route_id, purpose, model, quality_score_bps,
			strategy_name, created_at, updated_at, expires_at
		) VALUES (
			zeroblob(32), 7, 'balanced', 'answer', 'fast', 9200,
			'20260802-001', 'now', 'now', 'never'
		);
		PRAGMA user_version = 7;
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version int
	var sessionCount, taskTypeColumns, difficultyColumns int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_session_bindings`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('routing_session_bindings') WHERE name = 'task_type'`).Scan(&taskTypeColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('routing_session_bindings') WHERE name = 'difficulty'`).Scan(&difficultyColumns); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || sessionCount != 0 || taskTypeColumns != 1 || difficultyColumns != 1 {
		t.Fatalf(
			"version=%d sessions=%d task_type_columns=%d difficulty_columns=%d",
			version, sessionCount, taskTypeColumns, difficultyColumns,
		)
	}
}

func TestMigrateV9AddsSelfEscalationTraceFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v8.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7, schemaV8,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO routing_traces (
			created_at, profile_slug, protocol, path, strategy_name, route_id,
			task_type, risk, classification_source, initial_model, final_model,
			vision_mode, status_code, client_committed, answer_attempts,
			auxiliary_calls, total_outbound_calls, model_switches, target_switches,
			planned_worst_case_cost_micro_usd, consumed_estimated_cost_micro_usd,
			elapsed_ms
		) VALUES (
			'now', 'legacy', 'anthropic', '/v1/messages', 'legacy', 'route',
			'simple', 'normal', 'rule', 'fast', 'fast', 'none', 200, 1, 1,
			0, 1, 0, 0, 10, 10, 5
		);
		PRAGMA user_version = 8;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version, count int
	var reason string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT self_escalations, self_escalation_reason FROM routing_traces`).Scan(&count, &reason); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || count != 0 || reason != "" {
		t.Fatalf("version=%d count=%d reason=%q", version, count, reason)
	}
}

func TestMigrateV10AddsSelfEscalationEvidenceFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v9.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7, schemaV8, schemaV9,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		PRAGMA user_version = 9;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(routing_quality_evidence)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, name := range []string{
		"self_escalation_eligible_samples", "self_escalations",
		"supported_self_escalations", "unnecessary_self_escalations",
		"missed_self_escalations",
	} {
		if !columns[name] {
			t.Fatalf("column %q missing: %v", name, columns)
		}
	}
	if version != schemaVersion {
		t.Fatalf("version=%d", version)
	}
}

func TestMigrateV11AddsRoutingCallUsageFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v10.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6,
		schemaV7, schemaV8, schemaV9, schemaV10,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		PRAGMA user_version = 10;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('routing_calls')
		WHERE name IN (
			'usage_present', 'input_tokens', 'output_tokens',
			'cache_read_tokens', 'cache_write_tokens', 'input_includes_cache'
		)
	`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || columns != 6 {
		t.Fatalf("version=%d usage_columns=%d", version, columns)
	}
}

func TestMigrateV14PreservesLegacyEvidenceAsUnknownOverallOnly(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v13.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'legacy', 'Legacy', 1, '{}', 'now', 'now');
		INSERT INTO routing_quality_evidence (
			profile_id, evidence_day, strategy_name, route_id, task_type,
			candidate_model, reference_model, reviewer_model,
			samples, candidate_wins, ties, reference_wins, updated_at
		) VALUES (1, '2026-08-01', 'old', 'balanced', 'code', 'fast', 'strong', 'judge', 3, 2, 1, 0, 'now');
		PRAGMA user_version = 13;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var difficulty, risk, vision, dimension string
	var samples int
	if err := db.QueryRow(`
		SELECT difficulty, risk, vision_mode, samples
		FROM routing_quality_evidence_v2
	`).Scan(&difficulty, &risk, &vision, &samples); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT dimension FROM routing_quality_dimension_evidence`).Scan(&dimension); err != nil {
		t.Fatal(err)
	}
	if difficulty != "unknown" || risk != "normal" || vision != "none" ||
		dimension != "overall" || samples != 3 {
		t.Fatalf("difficulty=%q risk=%q vision=%q dimension=%q samples=%d",
			difficulty, risk, vision, dimension, samples)
	}
}

func TestMigrateV15SeparatesAnalyzerFallbackFromHighRisk(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v14.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO routing_traces (
			created_at, profile_slug, protocol, path, strategy_name, route_id,
			task_type, risk, classification_source, initial_model, final_model,
			vision_mode, status_code, client_committed, answer_attempts,
			auxiliary_calls, total_outbound_calls, model_switches, target_switches,
			planned_worst_case_cost_micro_usd, consumed_estimated_cost_micro_usd,
			elapsed_ms, decision_reason
		) VALUES
			('now', 'p', 'anthropic', '/v1/messages', 's', 'r', 'unknown', 'high', 'fallback', 'strong', 'strong', 'none', 200, 1, 1, 1, 2, 0, 0, 10, 5, 1, 'high risk'),
			('now', 'p', 'anthropic', '/v1/messages', 's', 'r', 'high_risk', 'high', 'rule', 'strong', 'strong', 'none', 200, 1, 1, 0, 1, 0, 0, 10, 5, 1, 'high risk');
		PRAGMA user_version = 14;
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT risk, classification_source, decision_reason FROM routing_traces ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][3]string{{"unknown", "fallback", "task analyzer fallback"}, {"high", "rule", "high risk"}}
	for index := 0; rows.Next(); index++ {
		var got [3]string
		if err := rows.Scan(&got[0], &got[1], &got[2]); err != nil {
			t.Fatal(err)
		}
		if index >= len(want) || got != want[index] {
			t.Fatalf("row %d=%v want=%v", index, got, want)
		}
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("version=%d", version)
	}
}

func TestMigrateV7PreservesV6TraceAndCreatesPhysicalCallAccounting(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v6.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for version, statements := range [][]string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		INSERT INTO routing_traces (
			created_at, profile_slug, protocol, path, strategy_name, route_id,
			task_type, risk, classification_source, initial_model, final_model,
			vision_mode, status_code, client_committed, answer_attempts,
			auxiliary_calls, total_outbound_calls, model_switches, target_switches,
			planned_worst_case_cost_micro_usd, reserved_cost_micro_usd, elapsed_ms
		) VALUES (
			'now', 'legacy', 'anthropic', '/v1/messages', 'legacy', 'route',
			'chat', 'normal', 'rule', 'fast', 'fast', 'none', 200, 1, 1,
			0, 1, 0, 0, 10, 7, 5
		);
		PRAGMA user_version = 6;
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var version int
	var correlation string
	var consumed, held, actual, allKnown int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT correlation_id, consumed_estimated_cost_micro_usd,
		       held_cost_micro_usd, known_actual_cost_micro_usd,
		       all_actual_costs_known
		FROM routing_traces
	`).Scan(&correlation, &consumed, &held, &actual, &allKnown); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || correlation != "" || consumed != 7 || held != 0 || actual != 0 || allKnown != 0 {
		t.Fatalf("version=%d correlation=%q consumed=%d held=%d actual=%d all_known=%d",
			version, correlation, consumed, held, actual, allKnown)
	}
	var callsTable string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='routing_calls'`).Scan(&callsTable); err != nil {
		t.Fatal(err)
	}
	var oldColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('routing_traces') WHERE name='reserved_cost_micro_usd'`).Scan(&oldColumn); err != nil {
		t.Fatal(err)
	}
	if callsTable != "routing_calls" || oldColumn != 0 {
		t.Fatalf("calls_table=%q old_column_count=%d", callsTable, oldColumn)
	}
}

func TestMigrateV16AddsFourDimensionalCandidateDecisionFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v15.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, statements := range [][]string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6,
		schemaV7, schemaV8, schemaV9, schemaV10, schemaV11, schemaV12,
		schemaV13, schemaV14, schemaV15,
	} {
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema v%d: %v", version+1, err)
			}
		}
	}
	if _, err := db.Exec(`
		INSERT INTO app_settings (id, created_at, updated_at) VALUES (1, 'now', 'now');
		PRAGMA user_version = 15;
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"stability_score_bps", "expected_latency_ms", "cost_efficiency_score_bps",
		"performance_score_bps", "routing_score_bps",
	} {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('routing_candidate_decisions') WHERE name = ?`,
			column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("column %q count=%d", column, count)
		}
	}
}
