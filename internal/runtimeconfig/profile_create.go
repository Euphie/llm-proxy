package runtimeconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type PreparedProfileCreate struct {
	store       *Store
	input       ProfileCreateInput
	profileJSON []byte
	policyJSON  []byte
	prospective Aggregate
	defaultID   int64
}

func (prepared *PreparedProfileCreate) Prospective() Aggregate {
	return cloneAggregate(prepared.prospective)
}

func (prepared *PreparedProfileCreate) DefaultProfileID() int64 {
	return prepared.defaultID
}

func (prepared *PreparedProfileCreate) Commit(ctx context.Context) (ApplyPolicyResult, error) {
	return prepared.store.commitPreparedProfileCreate(ctx, prepared)
}

func (store *Store) PrepareProfileCreate(
	ctx context.Context,
	input ProfileCreateInput,
) (*PreparedProfileCreate, error) {
	if input.Profile.ID != 0 || input.Actor == "" {
		return nil, errors.New("invalid Profile create input")
	}
	var profileID int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM profiles`).Scan(&profileID); err != nil {
		return nil, fmt.Errorf("allocate Profile ID: %w", err)
	}
	input.Profile.ID = profileID
	input.Profile.Config = normalizeProfileConfig(input.Profile.Config)
	now := store.now().UTC()
	aggregate := Aggregate{
		Profile: profileRecordFromInput(input.Profile, now),
		Models:  cloneModelsForProfile(input.Models, profileID, now),
		State: State{
			ProfileID: profileID, Revision: 1, ModelCatalogRevision: 1, UpdatedAt: now,
		},
	}
	if input.Policy != nil {
		var policyID int64
		if err := store.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), 0) + 1 FROM routing_policy_versions`,
		).Scan(&policyID); err != nil {
			return nil, fmt.Errorf("allocate routing Policy ID: %w", err)
		}
		aggregate.State.ActivePolicyVersionID = policyID
		aggregate.Active = &PolicyVersion{
			ID: policyID, ProfileID: profileID, Sequence: 1, Policy: *input.Policy,
			Kind: ChangeApply, Reason: input.Reason, Actor: input.Actor, CreatedAt: now,
		}
	}
	var activePolicy = input.Policy
	if _, err := resolveAggregateRuntime(aggregate, activePolicy); err != nil {
		return nil, err
	}
	var defaultID sql.NullInt64
	if err := store.db.QueryRowContext(ctx,
		`SELECT default_profile_id FROM app_settings WHERE id = 1`,
	).Scan(&defaultID); err != nil {
		return nil, fmt.Errorf("load default Profile: %w", err)
	}
	if input.MakeDefault && !aggregate.Profile.Enabled {
		return nil, profile.ErrDefaultRequired
	}
	if !defaultID.Valid && !input.MakeDefault {
		return nil, profile.ErrDefaultRequired
	}
	resolvedDefaultID := profileID
	if defaultID.Valid && !input.MakeDefault {
		resolvedDefaultID = defaultID.Int64
	}
	profileJSON, policyJSON, err := encodeAggregateConfig(aggregate)
	if err != nil {
		return nil, err
	}
	return &PreparedProfileCreate{
		store: store, input: input, profileJSON: profileJSON, policyJSON: policyJSON,
		prospective: aggregate, defaultID: resolvedDefaultID,
	}, nil
}

func (store *Store) commitPreparedProfileCreate(
	ctx context.Context,
	prepared *PreparedProfileCreate,
) (ApplyPolicyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("begin Profile create: %w", err)
	}
	defer tx.Rollback()
	record := prepared.prospective.Profile
	now := formatTime(record.CreatedAt)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, record.ID, record.Slug, record.DisplayName, record.Enabled,
		string(prepared.profileJSON), now, now); err != nil {
		var duplicate int
		if queryErr := tx.QueryRowContext(ctx,
			`SELECT 1 FROM profiles WHERE slug = ?`, record.Slug,
		).Scan(&duplicate); queryErr == nil {
			return ApplyPolicyResult{}, profile.ErrSlugConflict
		}
		return ApplyPolicyResult{}, fmt.Errorf("insert Profile: %w", err)
	}
	for _, model := range prepared.prospective.Models {
		var retiredAt any
		if model.RetiredAt != nil {
			retiredAt = formatTime(*model.RetiredAt)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO profile_models (
			  profile_id, model_id, capability_json, status, status_reason,
			  created_at, updated_at, retired_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, record.ID, model.ModelID, string(model.CapabilityJSON), model.Status,
			model.StatusReason, formatTime(model.CreatedAt), formatTime(model.UpdatedAt), retiredAt); err != nil {
			return ApplyPolicyResult{}, fmt.Errorf("insert Profile model %q: %w", model.ModelID, err)
		}
	}
	if active := prepared.prospective.Active; active != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO routing_policy_versions (
			  id, profile_id, policy_sequence, policy_json, source_version_id,
			  change_kind, change_reason, created_by, created_at
			) VALUES (?, ?, 1, ?, NULL, 'apply', ?, ?, ?)
		`, active.ID, record.ID, string(prepared.policyJSON), active.Reason,
			active.Actor, formatTime(active.CreatedAt)); err != nil {
			return ApplyPolicyResult{}, fmt.Errorf("insert initial routing Policy: %w", err)
		}
	}
	state := prepared.prospective.State
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO profile_runtime_state (
		  profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		) VALUES (?, 1, ?, 1, ?)
	`, record.ID, nullablePolicyID(state.ActivePolicyVersionID), formatTime(state.UpdatedAt)); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("insert Profile runtime state: %w", err)
	}
	if prepared.input.MakeDefault {
		if _, err := tx.ExecContext(ctx, `
			UPDATE app_settings SET default_profile_id = ?, updated_at = ? WHERE id = 1
		`, record.ID, now); err != nil {
			return ApplyPolicyResult{}, fmt.Errorf("set created default Profile: %w", err)
		}
	}
	if err := insertRuntimeEvent(ctx, tx, state,
		"profile_create", "", prepared.input.Reason, prepared.input.Actor); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("commit Profile create: %w", err)
	}
	return policyResult(prepared.prospective), nil
}
