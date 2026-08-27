package runtimeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
)

type PreparedModel struct {
	store       *Store
	input       ModelMutationInput
	prospective Aggregate
}

func (prepared *PreparedModel) Prospective() Aggregate {
	return cloneAggregate(prepared.prospective)
}

func (prepared *PreparedModel) Commit(ctx context.Context) (ApplyPolicyResult, error) {
	return prepared.store.commitPreparedModel(ctx, prepared)
}

func (store *Store) PrepareModel(
	ctx context.Context,
	input ModelMutationInput,
) (*PreparedModel, error) {
	input.ModelID = strings.TrimSpace(input.ModelID)
	if input.ProfileID <= 0 || input.ExpectedRevision <= 0 || input.ModelID == "" || input.Actor == "" {
		return nil, errors.New("invalid model mutation input")
	}
	aggregate, err := store.Load(ctx, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if aggregate.State.Revision != input.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	index := -1
	for i := range aggregate.Models {
		if aggregate.Models[i].ModelID == input.ModelID {
			index = i
			break
		}
	}
	now := store.now().UTC()
	switch input.Kind {
	case ModelAdd:
		if index >= 0 {
			return nil, ErrModelConflict
		}
		capability, err := normalizeCapability(input.ModelID, input.CapabilityJSON)
		if err != nil {
			return nil, err
		}
		input.CapabilityJSON = capability
		aggregate.Models = append(aggregate.Models, modeldirectory.Record{
			ProfileID: input.ProfileID, ModelID: input.ModelID,
			CapabilityJSON: capability, Status: modeldirectory.StatusAvailable,
			StatusReason: input.Reason, CreatedAt: now, UpdatedAt: now,
		})
	case ModelUpdate:
		if index < 0 {
			return nil, modeldirectory.ErrNotFound
		}
		if aggregate.Models[index].Status == modeldirectory.StatusRetired {
			return nil, ErrModelTransition
		}
		capability, err := normalizeCapability(input.ModelID, input.CapabilityJSON)
		if err != nil {
			return nil, err
		}
		input.CapabilityJSON = capability
		aggregate.Models[index].CapabilityJSON = capability
		aggregate.Models[index].StatusReason = input.Reason
		aggregate.Models[index].UpdatedAt = now
	case ModelRestore:
		if index < 0 {
			return nil, modeldirectory.ErrNotFound
		}
		if aggregate.Models[index].Status != modeldirectory.StatusOffline {
			return nil, ErrModelTransition
		}
		aggregate.Models[index].Status = modeldirectory.StatusAvailable
		aggregate.Models[index].StatusReason = input.Reason
		aggregate.Models[index].UpdatedAt = now
	case ModelRetire:
		if index < 0 {
			return nil, modeldirectory.ErrNotFound
		}
		if aggregate.Models[index].Status == modeldirectory.StatusRetired {
			return nil, ErrModelTransition
		}
		aggregate.Models[index].Status = modeldirectory.StatusRetired
		aggregate.Models[index].StatusReason = input.Reason
		aggregate.Models[index].UpdatedAt = now
		aggregate.Models[index].RetiredAt = &now
	default:
		return nil, errors.New("invalid model mutation kind")
	}
	aggregate.State.Revision++
	aggregate.State.ModelCatalogRevision++
	aggregate.State.UpdatedAt = now
	return &PreparedModel{store: store, input: input, prospective: aggregate}, nil
}

func (store *Store) commitPreparedModel(
	ctx context.Context,
	prepared *PreparedModel,
) (ApplyPolicyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("begin Profile model mutation: %w", err)
	}
	defer tx.Rollback()
	if err := requireRuntimeState(
		ctx, tx, prepared.input.ProfileID, prepared.input.ExpectedRevision,
		prepared.prospective.State.ModelCatalogRevision-1,
	); err != nil {
		return ApplyPolicyResult{}, err
	}
	now := formatTime(prepared.prospective.State.UpdatedAt)
	switch prepared.input.Kind {
	case ModelAdd:
		_, err = tx.ExecContext(ctx, `
			INSERT INTO profile_models (
			  profile_id, model_id, capability_json, status, status_reason,
			  created_at, updated_at, retired_at
			) VALUES (?, ?, ?, 'available', ?, ?, ?, NULL)
		`, prepared.input.ProfileID, prepared.input.ModelID,
			string(prepared.input.CapabilityJSON), prepared.input.Reason, now, now)
	case ModelUpdate:
		_, err = tx.ExecContext(ctx, `
			UPDATE profile_models SET capability_json = ?, status_reason = ?, updated_at = ?
			WHERE profile_id = ? AND model_id = ? AND status <> 'retired'
		`, string(prepared.input.CapabilityJSON), prepared.input.Reason, now,
			prepared.input.ProfileID, prepared.input.ModelID)
	case ModelRestore:
		_, err = tx.ExecContext(ctx, `
			UPDATE profile_models SET status = 'available', status_reason = ?, updated_at = ?, retired_at = NULL
			WHERE profile_id = ? AND model_id = ? AND status = 'offline'
		`, prepared.input.Reason, now, prepared.input.ProfileID, prepared.input.ModelID)
	case ModelRetire:
		_, err = tx.ExecContext(ctx, `
			UPDATE profile_models SET status = 'retired', status_reason = ?, updated_at = ?, retired_at = ?
			WHERE profile_id = ? AND model_id = ? AND status <> 'retired'
		`, prepared.input.Reason, now, now, prepared.input.ProfileID, prepared.input.ModelID)
	}
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("persist Profile model mutation: %w", err)
	}
	if err := advanceRuntimeState(ctx, tx, prepared.prospective.State, prepared.input.ExpectedRevision); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, prepared.prospective.State,
		string(prepared.input.Kind), prepared.input.ModelID, prepared.input.Reason, prepared.input.Actor); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("commit Profile model mutation: %w", err)
	}
	return policyResult(prepared.prospective), nil
}

type PreparedEmergencyOffline struct {
	store       *Store
	input       EmergencyOfflineInput
	policyJSON  []byte
	profileJSON []byte
	prospective Aggregate
}

func (prepared *PreparedEmergencyOffline) Prospective() Aggregate {
	return cloneAggregate(prepared.prospective)
}

func (prepared *PreparedEmergencyOffline) Commit(ctx context.Context) (ApplyPolicyResult, error) {
	return prepared.store.commitPreparedEmergencyOffline(ctx, prepared)
}

func (store *Store) PrepareEmergencyOffline(
	ctx context.Context,
	input EmergencyOfflineInput,
) (*PreparedEmergencyOffline, error) {
	input.ModelID = strings.TrimSpace(input.ModelID)
	if input.ProfileID <= 0 || input.ExpectedRevision <= 0 || input.ModelID == "" || input.Actor == "" {
		return nil, errors.New("invalid emergency offline input")
	}
	aggregate, err := store.Load(ctx, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if aggregate.State.Revision != input.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	modelIndex := -1
	for index := range aggregate.Models {
		if aggregate.Models[index].ModelID == input.ModelID {
			modelIndex = index
			break
		}
	}
	if modelIndex < 0 {
		return nil, modeldirectory.ErrNotFound
	}
	if aggregate.Models[modelIndex].Status != modeldirectory.StatusAvailable {
		return nil, ErrModelTransition
	}
	if input.ProfileConfig != nil {
		aggregate.Profile.Config = *input.ProfileConfig
	}
	policyJSON, err := json.Marshal(input.Policy)
	if err != nil {
		return nil, fmt.Errorf("encode emergency routing Policy: %w", err)
	}
	profileJSON, err := json.Marshal(aggregate.Profile.Config)
	if err != nil {
		return nil, fmt.Errorf("encode emergency Profile: %w", err)
	}
	var policyID, sequence int64
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM routing_policy_versions`).Scan(&policyID); err != nil {
		return nil, fmt.Errorf("allocate emergency Policy ID: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(policy_sequence), 0) + 1 FROM routing_policy_versions WHERE profile_id = ?
	`, input.ProfileID).Scan(&sequence); err != nil {
		return nil, fmt.Errorf("allocate emergency Policy sequence: %w", err)
	}
	now := store.now().UTC()
	aggregate.Models[modelIndex].Status = modeldirectory.StatusOffline
	aggregate.Models[modelIndex].StatusReason = input.Reason
	aggregate.Models[modelIndex].UpdatedAt = now
	aggregate.State.Revision++
	aggregate.State.ModelCatalogRevision++
	aggregate.State.ActivePolicyVersionID = policyID
	aggregate.State.UpdatedAt = now
	aggregate.Active = &PolicyVersion{
		ID: policyID, ProfileID: input.ProfileID, Sequence: sequence, Policy: input.Policy,
		Kind: ChangeEmergencyOffline, Reason: input.Reason, Actor: input.Actor, CreatedAt: now,
	}
	return &PreparedEmergencyOffline{
		store: store, input: input, policyJSON: policyJSON, profileJSON: profileJSON,
		prospective: aggregate,
	}, nil
}

func (store *Store) commitPreparedEmergencyOffline(
	ctx context.Context,
	prepared *PreparedEmergencyOffline,
) (ApplyPolicyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("begin emergency model offline: %w", err)
	}
	defer tx.Rollback()
	if err := requireRuntimeState(
		ctx, tx, prepared.input.ProfileID, prepared.input.ExpectedRevision,
		prepared.prospective.State.ModelCatalogRevision-1,
	); err != nil {
		return ApplyPolicyResult{}, err
	}
	now := formatTime(prepared.prospective.State.UpdatedAt)
	if _, err := tx.ExecContext(ctx, `
		UPDATE profile_models SET status = 'offline', status_reason = ?, updated_at = ?, retired_at = NULL
		WHERE profile_id = ? AND model_id = ? AND status = 'available'
	`, prepared.input.Reason, now, prepared.input.ProfileID, prepared.input.ModelID); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("mark Profile model offline: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE profiles SET config_json = ?, updated_at = ? WHERE id = ?
	`, string(prepared.profileJSON), now, prepared.input.ProfileID); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("persist emergency Profile update: %w", err)
	}
	active := prepared.prospective.Active
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (?, ?, ?, ?, NULL, 'emergency_offline', ?, ?, ?)
	`, active.ID, prepared.input.ProfileID, active.Sequence, string(prepared.policyJSON),
		prepared.input.Reason, prepared.input.Actor, now); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("insert emergency routing Policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM routing_session_bindings WHERE profile_id = ? AND model = ?
	`, prepared.input.ProfileID, prepared.input.ModelID); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("delete offline model Session bindings: %w", err)
	}
	if err := advanceRuntimeState(ctx, tx, prepared.prospective.State, prepared.input.ExpectedRevision); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, prepared.prospective.State,
		string(ChangeEmergencyOffline), prepared.input.ModelID, prepared.input.Reason, prepared.input.Actor); err != nil {
		return ApplyPolicyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("commit emergency model offline: %w", err)
	}
	return policyResult(prepared.prospective), nil
}

func normalizeCapability(modelID string, capabilityJSON []byte) ([]byte, error) {
	if !json.Valid(capabilityJSON) {
		return nil, errors.New("model capability is invalid JSON")
	}
	var identity struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(capabilityJSON, &identity); err != nil || identity.ID != modelID {
		return nil, errors.New("model capability ID does not match model_id")
	}
	return append([]byte(nil), capabilityJSON...), nil
}

func requireRuntimeState(
	ctx context.Context,
	tx *sql.Tx,
	profileID, revision, modelRevision int64,
) error {
	var currentRevision, currentModelRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT revision, model_catalog_revision FROM profile_runtime_state WHERE profile_id = ?
	`, profileID).Scan(&currentRevision, &currentModelRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read Profile runtime state: %w", err)
	}
	if currentRevision != revision || currentModelRevision != modelRevision {
		return ErrRevisionConflict
	}
	return nil
}

func advanceRuntimeState(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	expectedRevision int64,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE profile_runtime_state
		SET revision = ?, active_policy_version_id = ?, model_catalog_revision = ?, updated_at = ?
		WHERE profile_id = ? AND revision = ?
	`, state.Revision, nullablePolicyID(state.ActivePolicyVersionID), state.ModelCatalogRevision,
		formatTime(state.UpdatedAt), state.ProfileID, expectedRevision)
	if err != nil {
		return fmt.Errorf("advance Profile runtime state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Profile runtime CAS result: %w", err)
	}
	if affected != 1 {
		return ErrRevisionConflict
	}
	return nil
}

func insertRuntimeEvent(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	action, modelID, reason, actor string,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO routing_runtime_events (
		  profile_id, runtime_revision, policy_version_id, model_catalog_revision,
		  action, model_id, reason, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, state.ProfileID, state.Revision, nullablePolicyID(state.ActivePolicyVersionID),
		state.ModelCatalogRevision, action, modelID, reason, actor, formatTime(state.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert routing runtime event: %w", err)
	}
	return nil
}

func nullablePolicyID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func policyResult(aggregate Aggregate) ApplyPolicyResult {
	result := ApplyPolicyResult{State: aggregate.State}
	if aggregate.Active != nil {
		result.Active = *aggregate.Active
	}
	return result
}

func cloneAggregate(source Aggregate) Aggregate {
	payload, err := json.Marshal(source)
	if err != nil {
		panic(err)
	}
	var cloned Aggregate
	if err := json.Unmarshal(payload, &cloned); err != nil {
		panic(err)
	}
	return cloned
}
