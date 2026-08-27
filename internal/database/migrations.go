package database

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

const schemaVersion = 27

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

var schemaV6 = []string{
	`ALTER TABLE routing_traces ADD COLUMN initial_target TEXT NOT NULL DEFAULT 'primary'`,
	`ALTER TABLE routing_traces ADD COLUMN final_target TEXT NOT NULL DEFAULT 'primary'`,
}

var schemaV7 = []string{
	`ALTER TABLE routing_traces RENAME COLUMN reserved_cost_micro_usd TO consumed_estimated_cost_micro_usd`,
	`ALTER TABLE routing_traces ADD COLUMN correlation_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE routing_traces ADD COLUMN held_cost_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK (held_cost_micro_usd >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN known_actual_cost_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK (known_actual_cost_micro_usd >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN all_actual_costs_known INTEGER NOT NULL DEFAULT 0 CHECK (all_actual_costs_known IN (0, 1))`,
	`CREATE INDEX routing_traces_correlation_idx ON routing_traces(correlation_id)`,
	`CREATE TABLE routing_calls (
        id                       INTEGER PRIMARY KEY AUTOINCREMENT,
        created_at               TEXT NOT NULL,
        trace_id                 INTEGER NOT NULL REFERENCES routing_traces(id) ON DELETE CASCADE,
        correlation_id           TEXT NOT NULL,
        sequence                 INTEGER NOT NULL CHECK (sequence > 0),
        kind                     TEXT NOT NULL CHECK (kind IN ('analyzer', 'vision', 'answer')),
        logical_model            TEXT NOT NULL DEFAULT '',
        target                   TEXT NOT NULL DEFAULT '',
        image_index              INTEGER NOT NULL DEFAULT -1 CHECK (image_index >= -1),
        retry_index              INTEGER NOT NULL DEFAULT 0 CHECK (retry_index >= 0),
        model_switch_index       INTEGER NOT NULL DEFAULT 0 CHECK (model_switch_index >= 0),
        target_switch_index      INTEGER NOT NULL DEFAULT 0 CHECK (target_switch_index >= 0),
        estimated_cost_micro_usd INTEGER NOT NULL CHECK (estimated_cost_micro_usd >= 0),
        actual_cost_known        INTEGER NOT NULL CHECK (actual_cost_known IN (0, 1)),
        actual_cost_micro_usd    INTEGER NOT NULL DEFAULT 0 CHECK (actual_cost_micro_usd >= 0),
        status_code              INTEGER NOT NULL CHECK (status_code BETWEEN 0 AND 599),
        outcome                  TEXT NOT NULL DEFAULT '',
		UNIQUE(trace_id, sequence)
    )`,
	`CREATE INDEX routing_calls_trace_idx ON routing_calls(trace_id, sequence)`,
	`CREATE INDEX routing_calls_correlation_idx ON routing_calls(correlation_id, sequence)`,
}

var schemaV8 = []string{
	`ALTER TABLE routing_session_bindings ADD COLUMN task_type TEXT NOT NULL DEFAULT ''`,
}

var schemaV9 = []string{
	`ALTER TABLE routing_traces ADD COLUMN self_escalations INTEGER NOT NULL DEFAULT 0 CHECK (self_escalations >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN self_escalation_reason TEXT NOT NULL DEFAULT ''`,
}

var schemaV10 = []string{
	`ALTER TABLE routing_quality_evidence ADD COLUMN self_escalation_eligible_samples INTEGER NOT NULL DEFAULT 0 CHECK (self_escalation_eligible_samples >= 0)`,
	`ALTER TABLE routing_quality_evidence ADD COLUMN self_escalations INTEGER NOT NULL DEFAULT 0 CHECK (self_escalations >= 0)`,
	`ALTER TABLE routing_quality_evidence ADD COLUMN supported_self_escalations INTEGER NOT NULL DEFAULT 0 CHECK (supported_self_escalations >= 0)`,
	`ALTER TABLE routing_quality_evidence ADD COLUMN unnecessary_self_escalations INTEGER NOT NULL DEFAULT 0 CHECK (unnecessary_self_escalations >= 0)`,
	`ALTER TABLE routing_quality_evidence ADD COLUMN missed_self_escalations INTEGER NOT NULL DEFAULT 0 CHECK (missed_self_escalations >= 0)`,
}

var schemaV11 = []string{
	`ALTER TABLE routing_calls ADD COLUMN usage_present INTEGER NOT NULL DEFAULT 0 CHECK (usage_present IN (0, 1))`,
	`ALTER TABLE routing_calls ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0)`,
	`ALTER TABLE routing_calls ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0)`,
	`ALTER TABLE routing_calls ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0)`,
	`ALTER TABLE routing_calls ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0)`,
	`ALTER TABLE routing_calls ADD COLUMN input_includes_cache INTEGER NOT NULL DEFAULT 0 CHECK (input_includes_cache IN (0, 1))`,
	`CREATE INDEX routing_calls_model_usage_idx ON routing_calls(kind, usage_present, status_code, created_at, logical_model)`,
}

var schemaV12 = []string{
	`ALTER TABLE routing_session_bindings ADD COLUMN difficulty TEXT NOT NULL DEFAULT 'unknown'`,
}

var schemaV13 = []string{
	`ALTER TABLE routing_traces ADD COLUMN difficulty TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE routing_traces ADD COLUMN classification_confidence_bps INTEGER NOT NULL DEFAULT 0 CHECK (classification_confidence_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_traces ADD COLUMN classification_reason_codes TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE routing_traces ADD COLUMN estimated_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (estimated_input_tokens >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN requested_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (requested_output_tokens >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN decision_reason TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE routing_candidate_decisions (
        trace_id                    INTEGER NOT NULL REFERENCES routing_traces(id) ON DELETE CASCADE,
        ordinal                     INTEGER NOT NULL CHECK (ordinal > 0),
        model                       TEXT NOT NULL,
        decision                    TEXT NOT NULL CHECK (decision IN ('selected', 'eligible', 'rejected')),
        reason_code                 TEXT NOT NULL,
        quality_score_bps           INTEGER NOT NULL CHECK (quality_score_bps BETWEEN 0 AND 10000),
        severe_error_rate_bps       INTEGER NOT NULL CHECK (severe_error_rate_bps BETWEEN 0 AND 10000),
        expected_cost_micro_usd     INTEGER NOT NULL CHECK (expected_cost_micro_usd >= 0),
        answer_worst_cost_micro_usd INTEGER NOT NULL CHECK (answer_worst_cost_micro_usd >= 0),
        vision_call_cost_micro_usd  INTEGER NOT NULL CHECK (vision_call_cost_micro_usd >= 0),
        vision_mode                 TEXT NOT NULL,
        upstream_nodes              TEXT NOT NULL DEFAULT '[]',
        PRIMARY KEY(trace_id, ordinal)
    )`,
	`CREATE INDEX routing_candidate_decisions_model_idx ON routing_candidate_decisions(model, decision)`,
}

var schemaV14 = []string{
	`CREATE TABLE routing_quality_evidence_v2 (
        profile_id                    INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        evidence_day                  TEXT NOT NULL,
        strategy_name                 TEXT NOT NULL,
        route_id                      TEXT NOT NULL,
        task_type                     TEXT NOT NULL,
        difficulty                    TEXT NOT NULL,
        risk                          TEXT NOT NULL,
        vision_mode                   TEXT NOT NULL,
        candidate_model               TEXT NOT NULL,
        reference_model               TEXT NOT NULL,
        reviewer_model                TEXT NOT NULL,
        samples                       INTEGER NOT NULL DEFAULT 0 CHECK (samples >= 0),
        candidate_wins                INTEGER NOT NULL DEFAULT 0 CHECK (candidate_wins >= 0),
        ties                          INTEGER NOT NULL DEFAULT 0 CHECK (ties >= 0),
        reference_wins                INTEGER NOT NULL DEFAULT 0 CHECK (reference_wins >= 0),
        severe_errors                 INTEGER NOT NULL DEFAULT 0 CHECK (severe_errors >= 0),
        deterministic_failures        INTEGER NOT NULL DEFAULT 0 CHECK (deterministic_failures >= 0),
        self_escalation_eligible_samples INTEGER NOT NULL DEFAULT 0 CHECK (self_escalation_eligible_samples >= 0),
        self_escalations              INTEGER NOT NULL DEFAULT 0 CHECK (self_escalations >= 0),
        supported_self_escalations    INTEGER NOT NULL DEFAULT 0 CHECK (supported_self_escalations >= 0),
        unnecessary_self_escalations  INTEGER NOT NULL DEFAULT 0 CHECK (unnecessary_self_escalations >= 0),
        missed_self_escalations       INTEGER NOT NULL DEFAULT 0 CHECK (missed_self_escalations >= 0),
        candidate_cost_micro_usd      INTEGER NOT NULL DEFAULT 0 CHECK (candidate_cost_micro_usd >= 0),
        reference_cost_micro_usd      INTEGER NOT NULL DEFAULT 0 CHECK (reference_cost_micro_usd >= 0),
        reviewer_cost_micro_usd       INTEGER NOT NULL DEFAULT 0 CHECK (reviewer_cost_micro_usd >= 0),
        candidate_latency_ms          INTEGER NOT NULL DEFAULT 0 CHECK (candidate_latency_ms >= 0),
        reference_latency_ms          INTEGER NOT NULL DEFAULT 0 CHECK (reference_latency_ms >= 0),
        updated_at                    TEXT NOT NULL,
        PRIMARY KEY (
            profile_id, evidence_day, strategy_name, route_id, task_type,
            difficulty, risk, vision_mode,
            candidate_model, reference_model, reviewer_model
        )
    )`,
	`CREATE INDEX routing_quality_evidence_v2_profile_idx
        ON routing_quality_evidence_v2(profile_id, evidence_day)`,
	`CREATE INDEX routing_quality_evidence_v2_model_idx
        ON routing_quality_evidence_v2(profile_id, candidate_model, evidence_day)`,
	`CREATE TABLE routing_quality_dimension_evidence (
        profile_id       INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        evidence_day     TEXT NOT NULL,
        strategy_name    TEXT NOT NULL,
        route_id         TEXT NOT NULL,
        task_type        TEXT NOT NULL,
        difficulty       TEXT NOT NULL,
        risk             TEXT NOT NULL,
        vision_mode      TEXT NOT NULL,
        candidate_model  TEXT NOT NULL,
        reference_model  TEXT NOT NULL,
        reviewer_model   TEXT NOT NULL,
        dimension        TEXT NOT NULL,
        samples          INTEGER NOT NULL DEFAULT 0 CHECK (samples >= 0),
        candidate_wins   INTEGER NOT NULL DEFAULT 0 CHECK (candidate_wins >= 0),
        ties             INTEGER NOT NULL DEFAULT 0 CHECK (ties >= 0),
        reference_wins   INTEGER NOT NULL DEFAULT 0 CHECK (reference_wins >= 0),
        updated_at       TEXT NOT NULL,
        PRIMARY KEY (
            profile_id, evidence_day, strategy_name, route_id, task_type,
            difficulty, risk, vision_mode,
            candidate_model, reference_model, reviewer_model, dimension
        )
    )`,
	`CREATE INDEX routing_quality_dimension_profile_idx
        ON routing_quality_dimension_evidence(profile_id, evidence_day, dimension)`,
	`INSERT INTO routing_quality_evidence_v2 (
        profile_id, evidence_day, strategy_name, route_id, task_type,
        difficulty, risk, vision_mode, candidate_model, reference_model, reviewer_model,
        samples, candidate_wins, ties, reference_wins, severe_errors, deterministic_failures,
        self_escalation_eligible_samples, self_escalations, supported_self_escalations,
        unnecessary_self_escalations, missed_self_escalations,
        candidate_cost_micro_usd, reference_cost_micro_usd, reviewer_cost_micro_usd,
        candidate_latency_ms, reference_latency_ms, updated_at
    ) SELECT
        profile_id, evidence_day, strategy_name, route_id, task_type,
        'unknown', 'normal', 'none', candidate_model, reference_model, reviewer_model,
        samples, candidate_wins, ties, reference_wins, severe_errors, deterministic_failures,
        self_escalation_eligible_samples, self_escalations, supported_self_escalations,
        unnecessary_self_escalations, missed_self_escalations,
        candidate_cost_micro_usd, reference_cost_micro_usd, reviewer_cost_micro_usd,
        candidate_latency_ms, reference_latency_ms, updated_at
    FROM routing_quality_evidence`,
	`INSERT INTO routing_quality_dimension_evidence (
        profile_id, evidence_day, strategy_name, route_id, task_type,
        difficulty, risk, vision_mode, candidate_model, reference_model, reviewer_model,
        dimension, samples, candidate_wins, ties, reference_wins, updated_at
    ) SELECT
        profile_id, evidence_day, strategy_name, route_id, task_type,
        'unknown', 'normal', 'none', candidate_model, reference_model, reviewer_model,
        'overall', samples, candidate_wins, ties, reference_wins, updated_at
    FROM routing_quality_evidence`,
}

var schemaV15 = []string{
	`UPDATE routing_traces
	 SET risk = 'unknown',
	     decision_reason = CASE
	         WHEN decision_reason = '' OR decision_reason = 'high risk'
	         THEN 'task analyzer fallback'
	         ELSE decision_reason
	     END
	 WHERE classification_source = 'fallback'`,
}

var schemaV16 = []string{
	`ALTER TABLE routing_candidate_decisions ADD COLUMN stability_score_bps INTEGER NOT NULL DEFAULT 0 CHECK (stability_score_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_candidate_decisions ADD COLUMN expected_latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (expected_latency_ms >= 0)`,
	`ALTER TABLE routing_candidate_decisions ADD COLUMN cost_efficiency_score_bps INTEGER NOT NULL DEFAULT 0 CHECK (cost_efficiency_score_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_candidate_decisions ADD COLUMN performance_score_bps INTEGER NOT NULL DEFAULT 0 CHECK (performance_score_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_candidate_decisions ADD COLUMN routing_score_bps INTEGER NOT NULL DEFAULT 0 CHECK (routing_score_bps BETWEEN 0 AND 10000)`,
}

var schemaV17 = []string{
	`CREATE TABLE routing_strategy_generations (
        strategy_id               INTEGER PRIMARY KEY REFERENCES routing_strategies(id) ON DELETE CASCADE,
        profile_id                INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        generator_version         TEXT NOT NULL CHECK (generator_version <> ''),
        generation_intent_json    TEXT NOT NULL,
        source_snapshot_digest    TEXT NOT NULL CHECK (length(source_snapshot_digest) = 64),
        generated_config_json     TEXT NOT NULL,
        manual_override_patch_json TEXT NOT NULL DEFAULT '[]',
        explanations_json         TEXT NOT NULL DEFAULT '[]',
        created_at                TEXT NOT NULL,
        updated_at                TEXT NOT NULL
    )`,
	`CREATE INDEX routing_strategy_generations_profile_idx
        ON routing_strategy_generations(profile_id, updated_at)`,
}

var schemaV18 = []string{
	`ALTER TABLE routing_strategies ADD COLUMN archived_at TEXT`,
	`UPDATE routing_strategies SET state = 'ready' WHERE state = 'evaluating'`,
	`CREATE INDEX routing_strategies_archive_idx
        ON routing_strategies(profile_id, archived_at, id)`,
}

var schemaV19 = []string{
	`CREATE TABLE routing_agent_trajectories (
        id                        INTEGER PRIMARY KEY AUTOINCREMENT,
        profile_id                INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
        profile_slug              TEXT NOT NULL,
        protocol                  TEXT NOT NULL CHECK (protocol IN ('anthropic_messages', 'openai_chat_completions', 'openai_responses')),
        source                    TEXT NOT NULL CHECK (source IN ('session', 'request_snapshot')),
        status                    TEXT NOT NULL CHECK (status IN (
            'collecting', 'completed', 'queued', 'evaluating', 'evaluated',
            'timed_out', 'interrupted', 'skipped', 'failed', 'deleted'
        )),
        session_key               BLOB CHECK (session_key IS NULL OR length(session_key) = 32),
        strategy_name             TEXT NOT NULL DEFAULT '',
        route_id                  TEXT NOT NULL DEFAULT '',
        task_type                 TEXT NOT NULL DEFAULT '',
        difficulty                TEXT NOT NULL DEFAULT 'unknown',
        risk                      TEXT NOT NULL DEFAULT 'unknown',
        vision_mode               TEXT NOT NULL DEFAULT 'none',
        model_path_json           TEXT NOT NULL DEFAULT '[]',
        tool_calls                INTEGER NOT NULL DEFAULT 0 CHECK (tool_calls >= 0),
        turns                     INTEGER NOT NULL DEFAULT 0 CHECK (turns >= 0),
        elapsed_ms                INTEGER NOT NULL DEFAULT 0 CHECK (elapsed_ms >= 0),
        truncated                 INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
        reason_code               TEXT NOT NULL DEFAULT '',
        candidate_cost_micro_usd  INTEGER NOT NULL DEFAULT 0 CHECK (candidate_cost_micro_usd >= 0),
        evaluation_cost_micro_usd INTEGER NOT NULL DEFAULT 0 CHECK (evaluation_cost_micro_usd >= 0),
        evidence_recorded         INTEGER NOT NULL DEFAULT 0 CHECK (evidence_recorded IN (0, 1)),
        encryption_version        INTEGER NOT NULL CHECK (encryption_version > 0),
        nonce                     BLOB NOT NULL CHECK (length(nonce) > 0),
        ciphertext                BLOB NOT NULL CHECK (length(ciphertext) > 0),
        started_at                TEXT NOT NULL,
        completed_at              TEXT,
        expires_at                TEXT NOT NULL,
        created_at                TEXT NOT NULL,
        updated_at                TEXT NOT NULL,
        CHECK (
            (source = 'session' AND session_key IS NOT NULL) OR
            (source = 'request_snapshot' AND session_key IS NULL)
        )
    )`,
	`CREATE INDEX routing_agent_trajectories_profile_idx
        ON routing_agent_trajectories(profile_id, created_at DESC)`,
	`CREATE INDEX routing_agent_trajectories_status_idx
        ON routing_agent_trajectories(status, updated_at)`,
	`CREATE INDEX routing_agent_trajectories_expiry_idx
        ON routing_agent_trajectories(expires_at)`,
	`CREATE UNIQUE INDEX routing_agent_trajectories_session_idx
        ON routing_agent_trajectories(profile_id, session_key)
        WHERE source = 'session' AND status = 'collecting'`,
}

var schemaV20 = []string{
	`ALTER TABLE routing_agent_trajectories ADD COLUMN observation_digest BLOB
	    CHECK (observation_digest IS NULL OR length(observation_digest) = 32)`,
	`CREATE UNIQUE INDEX routing_agent_trajectories_observation_idx
	    ON routing_agent_trajectories(profile_id, session_key, observation_digest)
	    WHERE source = 'session' AND observation_digest IS NOT NULL`,
}

var schemaV21 = []string{
	`ALTER TABLE routing_traces ADD COLUMN session_key BLOB
	    CHECK (session_key IS NULL OR length(session_key) = 32)`,
	`CREATE INDEX routing_traces_session_idx
	    ON routing_traces(session_key, created_at, id)
	    WHERE session_key IS NOT NULL`,
}

var schemaV22 = []string{
	`ALTER TABLE routing_traces ADD COLUMN task_type_confidence_bps INTEGER NOT NULL DEFAULT 0
	    CHECK (task_type_confidence_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_traces ADD COLUMN difficulty_confidence_bps INTEGER NOT NULL DEFAULT 0
	    CHECK (difficulty_confidence_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_traces ADD COLUMN risk_confidence_bps INTEGER NOT NULL DEFAULT 0
	    CHECK (risk_confidence_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_traces ADD COLUMN classification_underspecified INTEGER NOT NULL DEFAULT 0
	    CHECK (classification_underspecified IN (0, 1))`,
	`ALTER TABLE routing_traces ADD COLUMN complexity_signals TEXT NOT NULL DEFAULT '{}'`,
}

var schemaV23 = []string{
	`ALTER TABLE routing_session_bindings ADD COLUMN task_fingerprint BLOB
	    CHECK (task_fingerprint IS NULL OR length(task_fingerprint) = 32)`,
	`ALTER TABLE routing_session_bindings ADD COLUMN stable_task_count INTEGER NOT NULL DEFAULT 0
	    CHECK (stable_task_count >= 0)`,
	`ALTER TABLE routing_session_bindings ADD COLUMN stable_confidence_bps INTEGER NOT NULL DEFAULT 0
	    CHECK (stable_confidence_bps BETWEEN 0 AND 10000)`,
	`ALTER TABLE routing_session_bindings ADD COLUMN model_locked INTEGER NOT NULL DEFAULT 0
	    CHECK (model_locked IN (0, 1))`,
	`ALTER TABLE routing_session_bindings ADD COLUMN lock_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE routing_session_bindings ADD COLUMN last_input_tokens INTEGER NOT NULL DEFAULT 0
	    CHECK (last_input_tokens >= 0)`,
}

var schemaV24 = []string{
	`UPDATE routing_strategies AS strategy
	 SET config_json = json_set(
	     strategy.config_json,
	     '$.roles',
	     json_object(
	       'participants', json(COALESCE(json_extract(profile.config_json, '$.auto_routing.participants'), '[]')),
	       'strong_baseline_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.strong_baseline_model'), ''),
	       'task_analyzer_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.task_analyzer_model'), ''),
	       'reviewer_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.dynamic_optimization.reviewer_model'), '')
	     )
	 )
	 FROM profiles AS profile
	 WHERE profile.id = strategy.profile_id
	   AND json_type(strategy.config_json, '$.roles') IS NULL`,
	`UPDATE routing_strategy_generations AS generation
	 SET generated_config_json = json_set(
	     generation.generated_config_json,
	     '$.roles',
	     json_object(
	       'participants', json(COALESCE(json_extract(profile.config_json, '$.auto_routing.participants'), '[]')),
	       'strong_baseline_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.strong_baseline_model'), ''),
	       'task_analyzer_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.task_analyzer_model'), ''),
	       'reviewer_model', COALESCE(json_extract(profile.config_json, '$.auto_routing.dynamic_optimization.reviewer_model'), '')
	     )
	 )
	 FROM profiles AS profile
	 WHERE profile.id = generation.profile_id
	   AND json_type(generation.generated_config_json, '$.roles') IS NULL`,
}

var schemaV25 = []string{
	`CREATE TABLE profile_models (
		profile_id      INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
		model_id        TEXT NOT NULL,
		capability_json TEXT NOT NULL,
		status          TEXT NOT NULL CHECK (status IN ('available', 'offline', 'retired')),
		status_reason   TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		retired_at      TEXT,
		PRIMARY KEY(profile_id, model_id)
	)`,
	`CREATE INDEX profile_models_status_idx
		ON profile_models(profile_id, status, model_id)`,
	`CREATE TABLE routing_policy_versions (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		profile_id        INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
		policy_sequence   INTEGER NOT NULL CHECK (policy_sequence > 0),
		policy_json       TEXT NOT NULL,
		source_version_id INTEGER REFERENCES routing_policy_versions(id),
		change_kind       TEXT NOT NULL CHECK (change_kind IN ('migration', 'apply', 'rollback', 'emergency_offline')),
		change_reason     TEXT NOT NULL DEFAULT '',
		created_by        TEXT NOT NULL,
		created_at        TEXT NOT NULL,
		UNIQUE(profile_id, policy_sequence)
	)`,
	`CREATE INDEX routing_policy_versions_profile_idx
		ON routing_policy_versions(profile_id, policy_sequence DESC)`,
	`CREATE TABLE profile_runtime_state (
		profile_id               INTEGER PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
		revision                 INTEGER NOT NULL CHECK (revision > 0),
		active_policy_version_id INTEGER REFERENCES routing_policy_versions(id),
		model_catalog_revision   INTEGER NOT NULL CHECK (model_catalog_revision > 0),
		updated_at               TEXT NOT NULL
	)`,
	`CREATE TABLE routing_runtime_events (
		id                     INTEGER PRIMARY KEY AUTOINCREMENT,
		profile_id             INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
		runtime_revision       INTEGER NOT NULL CHECK (runtime_revision > 0),
		policy_version_id      INTEGER REFERENCES routing_policy_versions(id),
		model_catalog_revision INTEGER NOT NULL CHECK (model_catalog_revision > 0),
		action                 TEXT NOT NULL,
		model_id               TEXT NOT NULL DEFAULT '',
		reason                 TEXT NOT NULL DEFAULT '',
		created_by             TEXT NOT NULL,
		created_at             TEXT NOT NULL
	)`,
	`CREATE INDEX routing_runtime_events_profile_idx
		ON routing_runtime_events(profile_id, runtime_revision DESC)`,
	`INSERT INTO profile_models (
		profile_id, model_id, capability_json, status, status_reason,
		created_at, updated_at, retired_at
	)
	SELECT profile.id,
	       json_extract(model.value, '$.id'),
	       model.value,
	       'available',
	       '',
	       profile.created_at,
	       profile.updated_at,
	       NULL
	FROM profiles AS profile, json_each(profile.config_json, '$.models') AS model
	WHERE json_type(model.value, '$.id') = 'text'
	  AND json_extract(model.value, '$.id') <> ''`,
	`INSERT INTO routing_policy_versions (
		profile_id, policy_sequence, policy_json, source_version_id,
		change_kind, change_reason, created_by, created_at
	)
	SELECT profile.id,
	       1,
	       json_set(
	         strategy.config_json,
		         '$.analyzer_timeout', COALESCE(json_extract(profile.config_json, '$.auto_routing.analyzer_timeout'), ''),
		         '$.analyzer_min_confidence_bps', COALESCE(json_extract(profile.config_json, '$.auto_routing.analyzer_min_confidence_bps'), 0),
		         '$.session_ttl', COALESCE(json_extract(profile.config_json, '$.auto_routing.session_ttl'), ''),
		         '$.session_lock_token_threshold', 100000,
		         '$.self_escalation', json(COALESCE(json_extract(profile.config_json, '$.auto_routing.self_escalation'), '{}')),
	         '$.dynamic_optimization', json(COALESCE(json_extract(profile.config_json, '$.auto_routing.dynamic_optimization'), '{}'))
	       ),
	       NULL,
	       'migration',
	       'Migrated from the active v24 routing strategy.',
	       'system:migration-v25',
	       profile.updated_at
	FROM profiles AS profile
	JOIN routing_strategy_pointers AS pointer ON pointer.profile_id = profile.id
	JOIN routing_strategies AS strategy ON strategy.id = pointer.active_strategy_id`,
	`INSERT INTO profile_runtime_state (
		profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
	)
	SELECT profile.id,
	       1,
	       (
	         SELECT policy.id
	         FROM routing_policy_versions AS policy
	         WHERE policy.profile_id = profile.id
	         ORDER BY policy.policy_sequence DESC
	         LIMIT 1
	       ),
	       1,
	       profile.updated_at
	FROM profiles AS profile`,
	`INSERT INTO routing_runtime_events (
		profile_id, runtime_revision, policy_version_id, model_catalog_revision,
		action, model_id, reason, created_by, created_at
	)
	SELECT state.profile_id,
	       state.revision,
	       state.active_policy_version_id,
	       state.model_catalog_revision,
	       'migration',
	       '',
	       'Migrated v24 Profile, models, and active strategy.',
	       'system:migration-v25',
	       state.updated_at
	FROM profile_runtime_state AS state`,
	`UPDATE profiles
	SET config_json = json_set(
	      json_remove(config_json, '$.models', '$.auto_routing'),
	      '$.version', 2,
	      '$.auto_routing', json_object(
	        'enabled', json(CASE
	          WHEN COALESCE(json_extract(config_json, '$.auto_routing.enabled'), 0) <> 0
	          THEN 'true'
	          ELSE 'false'
	        END)
	      )
	    )`,
	`ALTER TABLE routing_session_bindings ADD COLUMN runtime_revision INTEGER NOT NULL DEFAULT 0
		CHECK (runtime_revision >= 0)`,
	`ALTER TABLE routing_session_bindings ADD COLUMN policy_version_id INTEGER NOT NULL DEFAULT 0
		CHECK (policy_version_id >= 0)`,
	`ALTER TABLE routing_session_bindings ADD COLUMN model_catalog_revision INTEGER NOT NULL DEFAULT 0
		CHECK (model_catalog_revision >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN runtime_revision INTEGER NOT NULL DEFAULT 0
		CHECK (runtime_revision >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN policy_version_id INTEGER NOT NULL DEFAULT 0
		CHECK (policy_version_id >= 0)`,
	`ALTER TABLE routing_traces ADD COLUMN model_catalog_revision INTEGER NOT NULL DEFAULT 0
		CHECK (model_catalog_revision >= 0)`,
	`DELETE FROM routing_session_bindings`,
	`DROP TABLE routing_strategy_generations`,
	`DROP TABLE routing_strategy_events`,
	`DROP TABLE routing_strategy_pointers`,
	`DROP TABLE routing_strategies`,
}

var schemaV26 = []string{
	`ALTER TABLE routing_agent_trajectories ADD COLUMN evaluation_result_json TEXT NOT NULL DEFAULT '{}'`,
}

var schemaV27 = []string{
	`CREATE TABLE routing_policy_reconcile_state (
		profile_id         INTEGER PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
		dirty_at          TEXT,
		next_attempt_at   TEXT,
		pending_digest    TEXT NOT NULL DEFAULT '',
		confirmation_count INTEGER NOT NULL DEFAULT 0 CHECK (confirmation_count >= 0),
		last_attempt_at   TEXT,
		last_applied_at   TEXT,
		last_outcome      TEXT NOT NULL DEFAULT '',
		last_reason       TEXT NOT NULL DEFAULT '',
		updated_at        TEXT NOT NULL
	)`,
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
	if version < 6 {
		for _, statement := range schemaV6 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v6: %w", err)
			}
		}
	}
	if version < 7 {
		for _, statement := range schemaV7 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v7: %w", err)
			}
		}
	}
	if version < 8 {
		for _, statement := range schemaV8 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v8: %w", err)
			}
		}
	}
	if version < 9 {
		for _, statement := range schemaV9 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v9: %w", err)
			}
		}
	}
	if version < 10 {
		for _, statement := range schemaV10 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v10: %w", err)
			}
		}
	}
	if version < 11 {
		for _, statement := range schemaV11 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v11: %w", err)
			}
		}
	}
	if version < 12 {
		for _, statement := range schemaV12 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v12: %w", err)
			}
		}
	}
	if version < 13 {
		for _, statement := range schemaV13 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v13: %w", err)
			}
		}
	}
	if version < 14 {
		for _, statement := range schemaV14 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v14: %w", err)
			}
		}
	}
	if version < 15 {
		for _, statement := range schemaV15 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v15: %w", err)
			}
		}
	}
	if version < 16 {
		for _, statement := range schemaV16 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v16: %w", err)
			}
		}
	}
	if version < 17 {
		for _, statement := range schemaV17 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v17: %w", err)
			}
		}
	}
	if version < 18 {
		for _, statement := range schemaV18 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v18: %w", err)
			}
		}
	}
	if version < 19 {
		for _, statement := range schemaV19 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v19: %w", err)
			}
		}
	}
	if version < 20 {
		for _, statement := range schemaV20 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v20: %w", err)
			}
		}
	}
	if version < 21 {
		for _, statement := range schemaV21 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v21: %w", err)
			}
		}
	}
	if version < 22 {
		for _, statement := range schemaV22 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v22: %w", err)
			}
		}
	}
	if version < 23 {
		for _, statement := range schemaV23 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v23: %w", err)
			}
		}
	}
	if version < 24 {
		for _, statement := range schemaV24 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v24: %w", err)
			}
		}
	}
	if version < 25 {
		for _, statement := range schemaV25 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v25: %w", err)
			}
		}
	}
	if version < 26 {
		for _, statement := range schemaV26 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v26: %w", err)
			}
		}
	}
	if version < 27 {
		for _, statement := range schemaV27 {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("apply schema v27: %w", err)
			}
		}
	}
	if err := ensureRoutingSessionHMACKey(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
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
