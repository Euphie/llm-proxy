package runtimeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (store *Store) Load(ctx context.Context, profileID int64) (Aggregate, error) {
	record, err := profile.NewStore(store.db).Get(ctx, profileID)
	if err != nil {
		return Aggregate{}, err
	}
	models, err := modeldirectory.NewStore(store.db).List(ctx, profileID)
	if err != nil {
		return Aggregate{}, err
	}
	state, activePolicyID, err := store.state(ctx, profileID)
	if err != nil {
		return Aggregate{}, err
	}
	aggregate := Aggregate{Profile: record, Models: models, State: state}
	if activePolicyID.Valid {
		active, err := store.Policy(ctx, profileID, activePolicyID.Int64)
		if err != nil {
			return Aggregate{}, err
		}
		aggregate.Active = &active
	}
	return aggregate, nil
}

func (store *Store) AutomaticProfileIDs(ctx context.Context) ([]int64, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT state.profile_id
		FROM profile_runtime_state AS state
		JOIN profiles AS profile ON profile.id = state.profile_id
		JOIN routing_policy_versions AS policy ON policy.id = state.active_policy_version_id
		WHERE profile.enabled = 1
		  AND json_extract(policy.policy_json, '$.dynamic_optimization.enabled') = 1
		  AND json_extract(policy.policy_json, '$.dynamic_optimization.auto_update_policy') = 1
		ORDER BY state.profile_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list automatic routing Policy Profiles: %w", err)
	}
	defer rows.Close()
	var profileIDs []int64
	for rows.Next() {
		var profileID int64
		if err := rows.Scan(&profileID); err != nil {
			return nil, fmt.Errorf("scan automatic routing Policy Profile: %w", err)
		}
		profileIDs = append(profileIDs, profileID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read automatic routing Policy Profiles: %w", err)
	}
	return profileIDs, nil
}

func (store *Store) state(
	ctx context.Context,
	profileID int64,
) (State, sql.NullInt64, error) {
	var state State
	var activePolicyID sql.NullInt64
	var updatedAt string
	if err := store.db.QueryRowContext(ctx, `
		SELECT profile_id, revision, active_policy_version_id, model_catalog_revision, updated_at
		FROM profile_runtime_state WHERE profile_id = ?
	`, profileID).Scan(
		&state.ProfileID, &state.Revision, &activePolicyID, &state.ModelCatalogRevision, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return State{}, sql.NullInt64{}, ErrNotFound
		}
		return State{}, sql.NullInt64{}, fmt.Errorf("read Profile runtime state: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return State{}, sql.NullInt64{}, fmt.Errorf("parse Profile runtime update time: %w", err)
	}
	state.UpdatedAt = parsed
	if activePolicyID.Valid {
		state.ActivePolicyVersionID = activePolicyID.Int64
	}
	return state, activePolicyID, nil
}

func (store *Store) ApplyPolicy(
	ctx context.Context,
	input ApplyPolicyInput,
) (ApplyPolicyResult, error) {
	prepared, err := store.PreparePolicy(ctx, input)
	if err != nil {
		return ApplyPolicyResult{}, err
	}
	return prepared.Commit(ctx)
}

type PreparedPolicy struct {
	store       *Store
	input       ApplyPolicyInput
	policyJSON  []byte
	prospective ApplyPolicyResult
}

func (prepared *PreparedPolicy) Prospective() ApplyPolicyResult {
	result := prepared.prospective
	result.Active.Policy = clonePolicy(result.Active.Policy)
	return result
}

func (prepared *PreparedPolicy) Commit(ctx context.Context) (ApplyPolicyResult, error) {
	return prepared.store.commitPreparedPolicy(ctx, prepared)
}

func (store *Store) PreparePolicy(
	ctx context.Context,
	input ApplyPolicyInput,
) (*PreparedPolicy, error) {
	if input.ProfileID <= 0 || input.ExpectedRevision <= 0 || input.Actor == "" ||
		(input.Kind != ChangeApply && input.Kind != ChangeRollback && input.Kind != ChangeEmergencyOffline) {
		return nil, errors.New("invalid Policy apply input")
	}
	policyJSON, err := json.Marshal(input.Policy)
	if err != nil {
		return nil, fmt.Errorf("encode routing Policy: %w", err)
	}
	if err := json.Unmarshal(policyJSON, &input.Policy); err != nil {
		return nil, fmt.Errorf("clone routing Policy: %w", err)
	}
	state, _, err := store.state(ctx, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if state.Revision != input.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	var policyID, sequence int64
	if err := store.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0) + 1 FROM routing_policy_versions
	`).Scan(&policyID); err != nil {
		return nil, fmt.Errorf("allocate routing Policy ID: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(policy_sequence), 0) + 1
		FROM routing_policy_versions WHERE profile_id = ?
	`, input.ProfileID).Scan(&sequence); err != nil {
		return nil, fmt.Errorf("allocate routing Policy sequence: %w", err)
	}
	now := store.now().UTC()
	var sourceID *int64
	if input.SourceVersionID > 0 {
		value := input.SourceVersionID
		sourceID = &value
	}
	prospective := ApplyPolicyResult{
		State: State{
			ProfileID: input.ProfileID, Revision: input.ExpectedRevision + 1,
			ActivePolicyVersionID: policyID, ModelCatalogRevision: state.ModelCatalogRevision,
			UpdatedAt: now,
		},
		Active: PolicyVersion{
			ID: policyID, ProfileID: input.ProfileID, Sequence: sequence, Policy: input.Policy,
			SourceVersionID: sourceID, Kind: input.Kind, Reason: input.Reason,
			Actor: input.Actor, CreatedAt: now,
		},
	}
	return &PreparedPolicy{
		store: store, input: input, policyJSON: append([]byte(nil), policyJSON...),
		prospective: prospective,
	}, nil
}

func (store *Store) commitPreparedPolicy(
	ctx context.Context,
	prepared *PreparedPolicy,
) (ApplyPolicyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("begin routing Policy apply: %w", err)
	}
	defer tx.Rollback()
	var revision, modelRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT revision, model_catalog_revision
		FROM profile_runtime_state WHERE profile_id = ?
	`, prepared.input.ProfileID).Scan(&revision, &modelRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApplyPolicyResult{}, ErrNotFound
		}
		return ApplyPolicyResult{}, fmt.Errorf("read Profile runtime state for Policy commit: %w", err)
	}
	if revision != prepared.input.ExpectedRevision ||
		modelRevision != prepared.prospective.State.ModelCatalogRevision {
		return ApplyPolicyResult{}, ErrRevisionConflict
	}
	var source any
	if prepared.input.SourceVersionID > 0 {
		source = prepared.input.SourceVersionID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO routing_policy_versions (
		  id, profile_id, policy_sequence, policy_json, source_version_id,
		  change_kind, change_reason, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, prepared.prospective.Active.ID, prepared.input.ProfileID, prepared.prospective.Active.Sequence,
		string(prepared.policyJSON), source, prepared.input.Kind, prepared.input.Reason,
		prepared.input.Actor, formatTime(prepared.prospective.Active.CreatedAt))
	if err != nil {
		var currentRevision int64
		if queryErr := tx.QueryRowContext(ctx, `
			SELECT revision FROM profile_runtime_state WHERE profile_id = ?
		`, prepared.input.ProfileID).Scan(&currentRevision); queryErr == nil &&
			currentRevision != prepared.input.ExpectedRevision {
			return ApplyPolicyResult{}, ErrRevisionConflict
		}
		return ApplyPolicyResult{}, fmt.Errorf("insert routing Policy version: %w", err)
	}
	cas, err := tx.ExecContext(ctx, `
		UPDATE profile_runtime_state
		SET revision = revision + 1, active_policy_version_id = ?, updated_at = ?
		WHERE profile_id = ? AND revision = ?
	`, prepared.prospective.Active.ID, formatTime(prepared.prospective.State.UpdatedAt),
		prepared.input.ProfileID, prepared.input.ExpectedRevision)
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("advance Profile runtime state: %w", err)
	}
	affected, err := cas.RowsAffected()
	if err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("read Profile runtime CAS result: %w", err)
	}
	if affected != 1 {
		return ApplyPolicyResult{}, ErrRevisionConflict
	}
	if prepared.input.ResetOrdinarySessionLocks {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM routing_session_bindings
			WHERE profile_id = ? AND lock_reason = 'stable_session_conversation_v2'
		`, prepared.input.ProfileID); err != nil {
			return ApplyPolicyResult{}, fmt.Errorf("reset ordinary routing Session locks: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO routing_runtime_events (
		  profile_id, runtime_revision, policy_version_id, model_catalog_revision,
		  action, model_id, reason, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, '', ?, ?, ?)
	`, prepared.input.ProfileID, prepared.prospective.State.Revision, prepared.prospective.Active.ID,
		prepared.prospective.State.ModelCatalogRevision, prepared.input.Kind, prepared.input.Reason,
		prepared.input.Actor, formatTime(prepared.prospective.State.UpdatedAt)); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("insert routing runtime event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ApplyPolicyResult{}, fmt.Errorf("commit routing Policy apply: %w", err)
	}

	return prepared.Prospective(), nil
}

func clonePolicy(policy profile.RoutingPolicyConfig) profile.RoutingPolicyConfig {
	payload, err := json.Marshal(policy)
	if err != nil {
		panic(err)
	}
	var cloned profile.RoutingPolicyConfig
	if err := json.Unmarshal(payload, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

func (store *Store) Policy(ctx context.Context, profileID, versionID int64) (PolicyVersion, error) {
	version, err := scanPolicy(store.db.QueryRowContext(ctx, `
		SELECT id, profile_id, policy_sequence, policy_json, source_version_id,
		       change_kind, change_reason, created_by, created_at
		FROM routing_policy_versions
		WHERE profile_id = ? AND id = ?
	`, profileID, versionID))
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyVersion{}, ErrNotFound
	}
	return version, err
}

func (store *Store) History(ctx context.Context, profileID int64, limit int) ([]PolicyVersion, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, profile_id, policy_sequence, policy_json, source_version_id,
		       change_kind, change_reason, created_by, created_at
		FROM routing_policy_versions
		WHERE profile_id = ?
		ORDER BY policy_sequence DESC
		LIMIT ?
	`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list routing Policy history: %w", err)
	}
	defer rows.Close()
	versions := make([]PolicyVersion, 0)
	for rows.Next() {
		version, scanErr := scanPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routing Policy history: %w", err)
	}
	return versions, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanPolicy(row rowScanner) (PolicyVersion, error) {
	var version PolicyVersion
	var policyJSON []byte
	var source sql.NullInt64
	var kind, createdAt string
	if err := row.Scan(
		&version.ID, &version.ProfileID, &version.Sequence, &policyJSON, &source,
		&kind, &version.Reason, &version.Actor, &createdAt,
	); err != nil {
		return PolicyVersion{}, err
	}
	if err := json.Unmarshal(policyJSON, &version.Policy); err != nil {
		return PolicyVersion{}, fmt.Errorf("decode routing Policy: %w", err)
	}
	version.Kind = ChangeKind(kind)
	if source.Valid {
		value := source.Int64
		version.SourceVersionID = &value
	}
	var err error
	version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return PolicyVersion{}, fmt.Errorf("parse routing Policy created time: %w", err)
	}
	return version, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
