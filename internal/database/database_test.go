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
		"routing_calls",
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

func TestMigrateV8AddsTaskTypeToExistingSessionBindings(t *testing.T) {
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
	var taskType, difficulty string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT task_type, difficulty FROM routing_session_bindings`).Scan(&taskType, &difficulty); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || taskType != "" || difficulty != "unknown" {
		t.Fatalf("version=%d task_type=%q difficulty=%q", version, taskType, difficulty)
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
