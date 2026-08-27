package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestStoreAppliesImmutablePolicyWithRuntimeRevisionCAS(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (3, 'policy', 'Policy', 1, '{
		  "version":2,
		  "protocol":"anthropic",
		  "upstream":"https://example.com/",
		  "auto_routing":{"enabled":true},
		  "vision":{"enabled":false},
		  "overload_rules":[]
		}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (4, 3, 1, '{"name":"20260805-001"}', NULL,
		  'migration', 'legacy', 'system', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (3, 1, 4, 1, '2026-08-13T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	store := NewStore(db, func() time.Time { return now })
	aggregate, err := store.Load(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Profile.Config.Version != 2 || aggregate.State.Revision != 1 ||
		aggregate.Active == nil || aggregate.Active.ID != 4 || aggregate.Active.Policy.Name != "20260805-001" {
		t.Fatalf("aggregate=%+v", aggregate)
	}

	result, err := store.ApplyPolicy(context.Background(), ApplyPolicyInput{
		ProfileID: 3, ExpectedRevision: 1,
		Policy: profile.RoutingPolicyConfig{
			RoutingStrategyConfig: profile.RoutingStrategyConfig{Name: "20260813-001"},
		},
		Kind: ChangeApply, Reason: "replace candidates", Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Revision != 2 || result.State.ActivePolicyVersionID == 4 ||
		result.Active.ID != result.State.ActivePolicyVersionID ||
		result.Active.Policy.Name != "20260813-001" || result.Active.Sequence != 2 {
		t.Fatalf("result=%+v", result)
	}
	history, err := store.History(context.Background(), 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != result.Active.ID || history[1].ID != 4 ||
		history[1].Policy.Name != "20260805-001" {
		t.Fatalf("history=%+v", history)
	}

	_, err = store.ApplyPolicy(context.Background(), ApplyPolicyInput{
		ProfileID: 3, ExpectedRevision: 1,
		Policy: profile.RoutingPolicyConfig{
			RoutingStrategyConfig: profile.RoutingStrategyConfig{Name: "20260813-002"},
		},
		Kind: ChangeApply, Actor: "admin",
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale apply error=%v", err)
	}
	history, err = store.History(context.Background(), 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history after conflict=%+v", history)
	}
}

func TestStoreRollbackCopiesHistoricalPolicyIntoANewVersion(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'rollback', 'Rollback', 1, '{"version":2}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES
		  (1, 1, 1, '{"name":"20260801-001"}', NULL, 'migration', '', 'system', '2026-08-13T00:00:00Z'),
		  (2, 1, 2, '{"name":"20260802-001"}', NULL, 'apply', '', 'admin', '2026-08-13T01:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 2, 2, 1, '2026-08-13T01:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil)
	target, err := store.Policy(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyPolicy(context.Background(), ApplyPolicyInput{
		ProfileID: 1, ExpectedRevision: 2, Policy: target.Policy,
		SourceVersionID: target.ID, Kind: ChangeRollback, Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Active.ID == target.ID || result.Active.SourceVersionID == nil ||
		*result.Active.SourceVersionID != target.ID || result.Active.Kind != ChangeRollback ||
		result.Active.Policy.Name != target.Policy.Name {
		t.Fatalf("rollback=%+v target=%+v", result.Active, target)
	}
}

func TestPreparedPolicyAdvertisesExactRuntimeIdentityBeforeCommit(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'prepare', 'Prepare', 1, '{"version":2}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (8, 1, 1, '{"name":"20260801-001"}', NULL,
		  'migration', '', 'system', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 3, 8, 2, '2026-08-13T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil)
	prepared, err := store.PreparePolicy(context.Background(), ApplyPolicyInput{
		ProfileID: 1, ExpectedRevision: 3,
		Policy: profile.RoutingPolicyConfig{
			RoutingStrategyConfig: profile.RoutingStrategyConfig{Name: "20260813-001"},
		},
		Kind: ChangeApply, Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	prospective := prepared.Prospective()
	if prospective.State.Revision != 4 || prospective.State.ActivePolicyVersionID <= 8 ||
		prospective.Active.ID != prospective.State.ActivePolicyVersionID || prospective.Active.Sequence != 2 {
		t.Fatalf("prospective=%+v", prospective)
	}
	history, err := store.History(context.Background(), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("prepare persisted history=%+v", history)
	}
	committed, err := prepared.Commit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != prospective.State || committed.Active.ID != prospective.Active.ID {
		t.Fatalf("prospective=%+v committed=%+v", prospective, committed)
	}
}

func TestPolicyCommitResetsOnlyOrdinarySessionLocksWhenRequested(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'locks', 'Locks', 1, '{"version":2}',
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (1, 1, 1, '{"name":"old","session_lock_token_threshold":100000}', NULL,
		  'migration', '', 'system', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 1, 1, 1, '2026-08-13T00:00:00Z');
		INSERT INTO routing_session_bindings (
		  key_hash, profile_id, route_id, purpose, model, quality_score_bps,
		  strategy_name, created_at, updated_at, expires_at, model_locked, lock_reason
		) VALUES
		  (zeroblob(32), 1, 'general', 'answer', 'fast', 9000, 'old',
		   '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', '2026-08-14T00:00:00Z', 1,
		   'stable_session_conversation_v2'),
		  (randomblob(32), 1, 'strong', 'answer', 'strong', 10000, 'old',
		   '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', '2026-08-14T00:00:00Z', 1,
		   'highest_model');
	`); err != nil {
		t.Fatal(err)
	}

	_, err = NewStore(db, nil).ApplyPolicy(context.Background(), ApplyPolicyInput{
		ProfileID: 1, ExpectedRevision: 1,
		Policy: profile.RoutingPolicyConfig{
			RoutingStrategyConfig:     profile.RoutingStrategyConfig{Name: "new"},
			SessionLockTokenThreshold: 200_000,
		},
		Kind: ChangeApply, Actor: "admin", ResetOrdinarySessionLocks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var reason string
	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(MAX(lock_reason), '')
		FROM routing_session_bindings WHERE profile_id = 1
	`).Scan(&count, &reason); err != nil {
		t.Fatal(err)
	}
	if count != 1 || reason != "highest_model" {
		t.Fatalf("count=%d remaining_reason=%q", count, reason)
	}
}

func TestStoreListsProfilesWithAutomaticPolicyUpdatesEnabled(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES
		  (1, 'auto', 'Auto', 1, '{"version":2}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z'),
		  (2, 'manual', 'Manual', 1, '{"version":2}', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES
		  (1, 1, 1, '{"dynamic_optimization":{"enabled":true,"auto_update_policy":true}}', NULL,
		   'migration', '', 'system', '2026-08-13T00:00:00Z'),
		  (2, 2, 1, '{"dynamic_optimization":{"enabled":true,"auto_update_policy":false}}', NULL,
		   'migration', '', 'system', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES
		  (1, 1, 1, 1, '2026-08-13T00:00:00Z'),
		  (2, 1, 2, 1, '2026-08-13T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	ids, err := NewStore(db, nil).AutomaticProfileIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("automatic profiles=%v", ids)
	}
}

func TestPreparedModelMutationAdvancesRuntimeAndCatalogOnce(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'models', 'Models', 1, '{"version":2,"auto_routing":{"enabled":false}}',
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 4, NULL, 7, '2026-08-13T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	capability := json.RawMessage(`{"id":"new-model"}`)
	store := NewStore(db, nil)
	prepared, err := store.PrepareModel(context.Background(), ModelMutationInput{
		ProfileID: 1, ExpectedRevision: 4, Kind: ModelAdd,
		ModelID: "new-model", CapabilityJSON: capability, Reason: "new upstream", Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	prospective := prepared.Prospective()
	if prospective.State.Revision != 5 || prospective.State.ModelCatalogRevision != 8 ||
		len(prospective.Models) != 1 || prospective.Models[0].Status != modeldirectory.StatusAvailable {
		t.Fatalf("prospective=%+v", prospective)
	}
	if _, err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Revision != 5 || loaded.State.ModelCatalogRevision != 8 ||
		len(loaded.Models) != 1 || loaded.Models[0].ModelID != "new-model" {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestEmergencyOfflineCommitsModelPolicyAndSessionDeletionAtomically(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'emergency', 'Emergency', 1, '{"version":2,"auto_routing":{"enabled":true}}',
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z');
		INSERT INTO profile_models (
		  profile_id, model_id, capability_json, status, status_reason, created_at, updated_at, retired_at
		) VALUES (1, 'unsafe', '{"id":"unsafe"}', 'available', '',
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', NULL);
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (2, 1, 1, '{"name":"old"}', NULL, 'migration', '', 'system', '2026-08-13T00:00:00Z');
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 2, 2, 3, '2026-08-13T00:00:00Z');
		INSERT INTO routing_session_bindings (
		  key_hash, profile_id, route_id, purpose, model, quality_score_bps,
		  strategy_name, created_at, updated_at, expires_at
		) VALUES (zeroblob(32), 1, 'general', 'answer', 'unsafe', 9000,
		  'old', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', '2026-08-14T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil)
	prepared, err := store.PrepareEmergencyOffline(context.Background(), EmergencyOfflineInput{
		ProfileID: 1, ExpectedRevision: 2, ModelID: "unsafe",
		Policy: profile.RoutingPolicyConfig{RoutingStrategyConfig: profile.RoutingStrategyConfig{Name: "safe"}},
		Reason: "provider outage", Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	prospective := prepared.Prospective()
	if prospective.State.Revision != 3 || prospective.State.ModelCatalogRevision != 4 ||
		prospective.Active == nil || prospective.Active.Kind != ChangeEmergencyOffline ||
		prospective.Models[0].Status != modeldirectory.StatusOffline {
		t.Fatalf("prospective=%+v", prospective)
	}
	if _, err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var revision, modelRevision, sessions int64
	if err := db.QueryRow(`
		SELECT model.status, state.revision, state.model_catalog_revision,
		       (SELECT COUNT(*) FROM routing_session_bindings WHERE profile_id = 1)
		FROM profile_models AS model
		JOIN profile_runtime_state AS state ON state.profile_id = model.profile_id
		WHERE model.profile_id = 1 AND model.model_id = 'unsafe'
	`).Scan(&status, &revision, &modelRevision, &sessions); err != nil {
		t.Fatal(err)
	}
	if status != "offline" || revision != 3 || modelRevision != 4 || sessions != 0 {
		t.Fatalf("status=%s revision=%d model_revision=%d sessions=%d",
			status, revision, modelRevision, sessions)
	}
}

func TestPreparedProfileUpdateIsNormalizedAndAdvancesTheRuntimeAtomically(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := profile.NewConfig(profile.ProtocolAnthropic, "https://old.example")
	config.Version = 2
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := json.Marshal(profile.ModelCapabilityConfig{
		ID: "only-model", ContextWindow: intPointer(200_000), MaxOutputTokens: intPointer(32_000),
		SupportsVision: boolPointer(false), SupportsTools: boolPointer(true),
		SupportsStructuredOutput:      boolPointer(true),
		InputPriceMicroUSDPerMillion:  int64Pointer(1_000_000),
		OutputPriceMicroUSDPerMillion: int64Pointer(2_000_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'runtime-profile', 'Runtime Profile', 1, ?,
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z')
	`, string(configJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE app_settings SET default_profile_id = 1 WHERE id = 1;
		INSERT INTO profile_models (
		  profile_id, model_id, capability_json, status, status_reason, created_at, updated_at, retired_at
		) VALUES (1, 'only-model', ?, 'available', '',
		  '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', NULL);
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (1, 4, NULL, 7, '2026-08-13T00:00:00Z');
		INSERT INTO routing_session_bindings (
		  key_hash, profile_id, route_id, purpose, model, quality_score_bps,
		  strategy_name, created_at, updated_at, expires_at
		) VALUES (zeroblob(32), 1, 'general', 'answer', 'only-model', 9000,
		  '', '2026-08-13T00:00:00Z', '2026-08-13T00:00:00Z', '2026-08-14T00:00:00Z');
	`, string(capability)); err != nil {
		t.Fatal(err)
	}

	next := config
	next.Upstream = "https://new.example"
	next.Models = []profile.ModelCapabilityConfig{{ID: "must-not-be-persisted"}}
	next.AutoRouting.Participants = []string{"must-not-be-persisted"}
	now := time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)
	store := NewStore(db, func() time.Time { return now })
	prepared, err := store.PrepareProfile(context.Background(), ProfileMutationInput{
		ProfileID: 1, ExpectedRevision: 4,
		Profile: profile.SaveInput{
			ID: 1, Slug: "runtime-profile", DisplayName: "Runtime Profile Updated",
			Enabled: true, Config: next,
		},
		Reason: "change upstream", Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	prospective := prepared.Prospective()
	if prospective.State.Revision != 5 || prospective.State.ModelCatalogRevision != 7 ||
		prospective.Profile.Config.Upstream != "https://new.example" ||
		len(prospective.Profile.Config.Models) != 0 ||
		len(prospective.Profile.Config.AutoRouting.Participants) != 0 {
		t.Fatalf("prospective=%+v", prospective)
	}
	if _, err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Revision != 5 || loaded.Profile.DisplayName != "Runtime Profile Updated" ||
		loaded.Profile.Config.Upstream != "https://new.example" {
		t.Fatalf("loaded=%+v", loaded)
	}
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routing_session_bindings WHERE profile_id = 1`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions=%d", sessions)
	}
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
func boolPointer(value bool) *bool    { return &value }
