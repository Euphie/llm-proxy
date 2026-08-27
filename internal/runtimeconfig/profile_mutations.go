package runtimeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type PreparedProfile struct {
	store       *Store
	input       ProfileMutationInput
	profileJSON []byte
	prospective Aggregate
	defaultID   int64
}

func (prepared *PreparedProfile) Prospective() Aggregate {
	return cloneAggregate(prepared.prospective)
}

func (prepared *PreparedProfile) DefaultProfileID() int64 {
	return prepared.defaultID
}

func (prepared *PreparedProfile) Commit(ctx context.Context) (ApplyPolicyResult, error) {
	return prepared.store.commitPreparedProfile(ctx, prepared)
}

func (store *Store) PrepareProfile(
	ctx context.Context,
	input ProfileMutationInput,
) (*PreparedProfile, error) {
	if input.ProfileID <= 0 || input.ExpectedRevision <= 0 || input.Actor == "" ||
		input.Profile.ID != input.ProfileID {
		return nil, errors.New("invalid Profile mutation input")
	}
	aggregate, err := store.Load(ctx, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if aggregate.State.Revision != input.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	if input.Profile.Config.Version != 2 {
		return nil, fmt.Errorf("%w: runtime Profile version must be 2", profile.ErrInvalidConfig)
	}
	input.Profile.Config = normalizeProfileConfig(input.Profile.Config)
	aggregate.Profile.Slug = input.Profile.Slug
	aggregate.Profile.DisplayName = input.Profile.DisplayName
	aggregate.Profile.Enabled = input.Profile.Enabled
	aggregate.Profile.Config = input.Profile.Config

	var defaultID sql.NullInt64
	if err := store.db.QueryRowContext(ctx,
		`SELECT default_profile_id FROM app_settings WHERE id = 1`,
	).Scan(&defaultID); err != nil {
		return nil, fmt.Errorf("load default Profile: %w", err)
	}
	if input.MakeDefault && !input.Profile.Enabled {
		return nil, profile.ErrDefaultRequired
	}
	if defaultID.Valid && defaultID.Int64 == input.ProfileID && !input.Profile.Enabled {
		return nil, profile.ErrDefaultRequired
	}
	if !defaultID.Valid {
		return nil, profile.ErrDefaultRequired
	}
	resolvedDefaultID := defaultID.Int64
	if input.MakeDefault {
		resolvedDefaultID = input.ProfileID
	}
	var duplicate int
	err = store.db.QueryRowContext(ctx,
		`SELECT 1 FROM profiles WHERE slug = ? AND id <> ?`,
		input.Profile.Slug, input.ProfileID,
	).Scan(&duplicate)
	if err == nil {
		return nil, profile.ErrSlugConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check Profile slug: %w", err)
	}
	var policy *profile.RoutingPolicyConfig
	if aggregate.Active != nil {
		policy = &aggregate.Active.Policy
	}
	if _, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, policy); err != nil {
		return nil, err
	}
	profileJSON, err := json.Marshal(aggregate.Profile.Config)
	if err != nil {
		return nil, fmt.Errorf("encode Profile config: %w", err)
	}
	now := store.now().UTC()
	aggregate.Profile.UpdatedAt = now
	aggregate.State.Revision++
	aggregate.State.UpdatedAt = now
	return &PreparedProfile{
		store: store, input: input, profileJSON: profileJSON,
		prospective: aggregate, defaultID: resolvedDefaultID,
	}, nil
}

func (store *Store) commitPreparedProfile(
	ctx context.Context,
	prepared *PreparedProfile,
) (ApplyPolicyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("begin Profile mutation: %w", err)
	}
	defer tx.Rollback()
	if err := requireRuntimeState(
		ctx, tx, prepared.input.ProfileID, prepared.input.ExpectedRevision,
		prepared.prospective.State.ModelCatalogRevision,
	); err != nil {
		return ApplyPolicyResult{}, err
	}
	now := formatTime(prepared.prospective.State.UpdatedAt)
	result, err := tx.ExecContext(ctx, `
		UPDATE profiles
		SET slug = ?, display_name = ?, enabled = ?, config_json = ?, updated_at = ?
		WHERE id = ?
	`, prepared.prospective.Profile.Slug, prepared.prospective.Profile.DisplayName,
		prepared.prospective.Profile.Enabled, string(prepared.profileJSON), now,
		prepared.input.ProfileID)
	if err != nil {
		var duplicate int
		if queryErr := tx.QueryRowContext(ctx,
			`SELECT 1 FROM profiles WHERE slug = ? AND id <> ?`,
			prepared.prospective.Profile.Slug, prepared.input.ProfileID,
		).Scan(&duplicate); queryErr == nil {
			return ApplyPolicyResult{}, profile.ErrSlugConflict
		}
		return ApplyPolicyResult{}, fmt.Errorf("persist Profile mutation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("read Profile mutation count: %w", err)
	}
	if affected != 1 {
		return ApplyPolicyResult{}, profile.ErrNotFound
	}
	if prepared.input.MakeDefault {
		result, err = tx.ExecContext(ctx, `
			UPDATE app_settings SET default_profile_id = ?, updated_at = ? WHERE id = 1
		`, prepared.input.ProfileID, now)
		if err != nil {
			return ApplyPolicyResult{}, fmt.Errorf("persist default Profile: %w", err)
		}
		if affected, err = result.RowsAffected(); err != nil || affected != 1 {
			return ApplyPolicyResult{}, profile.ErrDefaultRequired
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM routing_session_bindings WHERE profile_id = ?`,
		prepared.input.ProfileID,
	); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("clear Profile Session bindings: %w", err)
	}
	if err := advanceRuntimeState(ctx, tx, prepared.prospective.State, prepared.input.ExpectedRevision); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, prepared.prospective.State,
		"profile_update", "", prepared.input.Reason, prepared.input.Actor); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("commit Profile mutation: %w", err)
	}
	return policyResult(prepared.prospective), nil
}

func normalizeProfileConfig(config profile.Config) profile.Config {
	enabled := config.AutoRouting.Enabled
	config.Version = 2
	config.Models = nil
	config.AutoRouting = profile.AutoRoutingConfig{Enabled: enabled}
	return config
}
