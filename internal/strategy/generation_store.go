package strategy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

var generationDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Generation struct {
	StrategyID       int64                          `json:"strategy_id"`
	ProfileID        int64                          `json:"profile_id"`
	GeneratorVersion string                         `json:"generator_version"`
	Intent           strategycompiler.Intent        `json:"intent"`
	SourceDigest     string                         `json:"source_digest"`
	GeneratedConfig  profile.RoutingStrategyConfig  `json:"generated_config"`
	ManualOverrides  OverridePatch                  `json:"manual_overrides"`
	Explanations     []strategycompiler.Explanation `json:"explanations"`
	CreatedAt        time.Time                      `json:"created_at"`
	UpdatedAt        time.Time                      `json:"updated_at"`
}

type GenerationInput struct {
	StrategyID       int64
	ProfileID        int64
	GeneratorVersion string
	Intent           strategycompiler.Intent
	SourceDigest     string
	GeneratedConfig  profile.RoutingStrategyConfig
	Explanations     []strategycompiler.Explanation
}

type RecommendationInput struct {
	GeneratorVersion string
	SourceDigest     string
	GeneratedConfig  profile.RoutingStrategyConfig
	Explanations     []strategycompiler.Explanation
}

type GenerationStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewGenerationStore(db *sql.DB, now func() time.Time) *GenerationStore {
	if now == nil {
		now = time.Now
	}
	return &GenerationStore{db: db, now: now}
}

func (store *GenerationStore) CreateGeneratedDraft(
	ctx context.Context,
	record profile.Record,
	input GenerationInput,
) (version Version, generation Generation, err error) {
	input.ProfileID = record.ID
	if record.ID <= 0 || input.GeneratorVersion == "" ||
		!generationDigestPattern.MatchString(input.SourceDigest) {
		return Version{}, Generation{}, fmt.Errorf("invalid strategy generation metadata")
	}
	if err := validateStrategy(record, input.GeneratedConfig); err != nil {
		return Version{}, Generation{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, Generation{}, fmt.Errorf("begin generated strategy draft: %w", err)
	}
	defer rollback(tx)
	snapshot, err := readSnapshot(ctx, tx, record.ID)
	if err != nil {
		return Version{}, Generation{}, err
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	version, err = insertVersion(ctx, tx, record.ID, StateDraft, input.GeneratedConfig, now)
	if err != nil {
		return Version{}, Generation{}, err
	}
	input.StrategyID = version.ID
	if err := insertGeneration(ctx, tx, input, now); err != nil {
		return Version{}, Generation{}, err
	}
	if err := insertEvent(ctx, tx, record.ID, version.ID, "create", "", StateDraft, snapshot.Revision, now); err != nil {
		return Version{}, Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Version{}, Generation{}, fmt.Errorf("commit generated strategy draft: %w", err)
	}
	generation, err = store.Get(ctx, record.ID, version.ID)
	return version, generation, err
}

func (store *GenerationStore) UpdateGeneratedDraft(
	ctx context.Context,
	record profile.Record,
	strategyID int64,
	config profile.RoutingStrategyConfig,
) (version Version, generation Generation, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, Generation{}, fmt.Errorf("begin generated strategy update: %w", err)
	}
	defer rollback(tx)
	current, err := getVersion(ctx, tx, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, err
	}
	if current.State != StateDraft || current.ArchivedAt != nil {
		return Version{}, Generation{}, ErrImmutable
	}
	if config.Name != current.Config.Name {
		return Version{}, Generation{}, ErrImmutable
	}
	if err := validateStrategy(record, config); err != nil {
		return Version{}, Generation{}, err
	}
	generation, err = getGeneration(ctx, tx, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, err
	}
	patch, err := DiffOverrides(generation.GeneratedConfig, config)
	if err != nil {
		return Version{}, Generation{}, err
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	version, err = updateGeneratedDraftConfig(ctx, tx, record, current, config, now)
	if err != nil {
		return Version{}, Generation{}, err
	}
	patchJSON, err := json.Marshal(normalizePatch(patch))
	if err != nil {
		return Version{}, Generation{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE routing_strategy_generations
		SET manual_override_patch_json = ?, updated_at = ?
		WHERE profile_id = ? AND strategy_id = ?
	`, string(patchJSON), now, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, fmt.Errorf("update generated strategy overrides: %w", err)
	}
	if err := requireOneCAS(result); err != nil {
		return Version{}, Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Version{}, Generation{}, fmt.Errorf("commit generated strategy update: %w", err)
	}
	generation, err = store.Get(ctx, record.ID, strategyID)
	return version, generation, err
}

func (store *GenerationStore) RestoreGeneratedDraft(
	ctx context.Context,
	record profile.Record,
	strategyID int64,
	path string,
) (version Version, generation Generation, err error) {
	return store.replaceGeneratedDraft(ctx, record, strategyID, RecommendationInput{}, path)
}

func (store *GenerationStore) replaceGeneratedDraft(
	ctx context.Context,
	record profile.Record,
	strategyID int64,
	input RecommendationInput,
	restorePath string,
) (version Version, generation Generation, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, Generation{}, fmt.Errorf("begin generated strategy recommendation update: %w", err)
	}
	defer rollback(tx)
	current, err := getVersion(ctx, tx, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, err
	}
	if current.State != StateDraft || current.ArchivedAt != nil {
		return Version{}, Generation{}, ErrImmutable
	}
	generation, err = getGeneration(ctx, tx, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, err
	}
	patch := generation.ManualOverrides
	if restorePath != "" {
		patch = RemoveOverride(patch, restorePath)
		input = RecommendationInput{
			GeneratorVersion: generation.GeneratorVersion,
			SourceDigest:     generation.SourceDigest,
			GeneratedConfig:  generation.GeneratedConfig,
			Explanations:     generation.Explanations,
		}
	} else if input.GeneratorVersion == "" || !generationDigestPattern.MatchString(input.SourceDigest) {
		return Version{}, Generation{}, fmt.Errorf("invalid strategy generation metadata")
	}
	if input.GeneratedConfig.Name != current.Config.Name {
		return Version{}, Generation{}, ErrImmutable
	}
	merged, err := ApplyOverrides(input.GeneratedConfig, patch)
	if err != nil {
		return Version{}, Generation{}, err
	}
	if err := validateStrategy(record, merged); err != nil {
		return Version{}, Generation{}, err
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	version, err = updateGeneratedDraftConfig(ctx, tx, record, current, merged, now)
	if err != nil {
		return Version{}, Generation{}, err
	}
	configJSON, err := json.Marshal(input.GeneratedConfig)
	if err != nil {
		return Version{}, Generation{}, err
	}
	patchJSON, err := json.Marshal(normalizePatch(patch))
	if err != nil {
		return Version{}, Generation{}, err
	}
	explanations := input.Explanations
	if explanations == nil {
		explanations = []strategycompiler.Explanation{}
	}
	explanationsJSON, err := json.Marshal(explanations)
	if err != nil {
		return Version{}, Generation{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE routing_strategy_generations
		SET generator_version = ?, source_snapshot_digest = ?, generated_config_json = ?,
		    manual_override_patch_json = ?, explanations_json = ?, updated_at = ?
		WHERE profile_id = ? AND strategy_id = ?
	`, input.GeneratorVersion, input.SourceDigest, string(configJSON), string(patchJSON),
		string(explanationsJSON), now, record.ID, strategyID)
	if err != nil {
		return Version{}, Generation{}, fmt.Errorf("update generated strategy recommendation: %w", err)
	}
	if err := requireOneCAS(result); err != nil {
		return Version{}, Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Version{}, Generation{}, fmt.Errorf("commit generated strategy recommendation update: %w", err)
	}
	generation, err = store.Get(ctx, record.ID, strategyID)
	return version, generation, err
}

func (store *GenerationStore) Create(ctx context.Context, input GenerationInput) (Generation, error) {
	if err := validateGenerationIdentity(input.StrategyID, input.ProfileID, input.GeneratorVersion, input.SourceDigest); err != nil {
		return Generation{}, err
	}
	if err := store.requireDraft(ctx, input.ProfileID, input.StrategyID); err != nil {
		return Generation{}, err
	}
	intentJSON, err := json.Marshal(input.Intent)
	if err != nil {
		return Generation{}, fmt.Errorf("encode strategy generation intent: %w", err)
	}
	configJSON, err := json.Marshal(input.GeneratedConfig)
	if err != nil {
		return Generation{}, fmt.Errorf("encode generated strategy: %w", err)
	}
	explanations := input.Explanations
	if explanations == nil {
		explanations = []strategycompiler.Explanation{}
	}
	explanationsJSON, err := json.Marshal(explanations)
	if err != nil {
		return Generation{}, fmt.Errorf("encode strategy explanations: %w", err)
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO routing_strategy_generations (
			strategy_id, profile_id, generator_version, generation_intent_json,
			source_snapshot_digest, generated_config_json, manual_override_patch_json,
			explanations_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)
	`, input.StrategyID, input.ProfileID, input.GeneratorVersion, string(intentJSON),
		input.SourceDigest, string(configJSON), string(explanationsJSON), now, now)
	if err != nil {
		return Generation{}, fmt.Errorf("create strategy generation metadata: %w", err)
	}
	return store.Get(ctx, input.ProfileID, input.StrategyID)
}

func (store *GenerationStore) Get(ctx context.Context, profileID, strategyID int64) (Generation, error) {
	return scanGeneration(store.db.QueryRowContext(ctx, `
		SELECT strategy_id, profile_id, generator_version, generation_intent_json,
		       source_snapshot_digest, generated_config_json, manual_override_patch_json,
		       explanations_json, created_at, updated_at
		FROM routing_strategy_generations
		WHERE profile_id = ? AND strategy_id = ?
	`, profileID, strategyID))
}

func (store *GenerationStore) List(ctx context.Context, profileID int64) ([]Generation, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT strategy_id, profile_id, generator_version, generation_intent_json,
		       source_snapshot_digest, generated_config_json, manual_override_patch_json,
		       explanations_json, created_at, updated_at
		FROM routing_strategy_generations
		WHERE profile_id = ?
		ORDER BY strategy_id DESC
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list strategy generation metadata: %w", err)
	}
	defer rows.Close()
	result := make([]Generation, 0)
	for rows.Next() {
		generation, err := scanGeneration(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate strategy generation metadata: %w", err)
	}
	return result, nil
}

func scanGeneration(row rowScanner) (Generation, error) {
	var generation Generation
	var intentJSON, configJSON, patchJSON, explanationsJSON, createdAt, updatedAt string
	err := row.Scan(
		&generation.StrategyID, &generation.ProfileID, &generation.GeneratorVersion,
		&intentJSON, &generation.SourceDigest, &configJSON, &patchJSON,
		&explanationsJSON, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrNotFound
	}
	if err != nil {
		return Generation{}, fmt.Errorf("read strategy generation metadata: %w", err)
	}
	if err := json.Unmarshal([]byte(intentJSON), &generation.Intent); err != nil {
		return Generation{}, fmt.Errorf("decode strategy generation intent: %w", err)
	}
	if err := json.Unmarshal([]byte(configJSON), &generation.GeneratedConfig); err != nil {
		return Generation{}, fmt.Errorf("decode generated strategy: %w", err)
	}
	if err := json.Unmarshal([]byte(patchJSON), &generation.ManualOverrides); err != nil {
		return Generation{}, fmt.Errorf("decode strategy manual overrides: %w", err)
	}
	if err := json.Unmarshal([]byte(explanationsJSON), &generation.Explanations); err != nil {
		return Generation{}, fmt.Errorf("decode strategy explanations: %w", err)
	}
	if generation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Generation{}, fmt.Errorf("parse strategy generation creation time: %w", err)
	}
	if generation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Generation{}, fmt.Errorf("parse strategy generation update time: %w", err)
	}
	return generation, nil
}

func (store *GenerationStore) UpdateOverrides(
	ctx context.Context,
	profileID, strategyID int64,
	edited profile.RoutingStrategyConfig,
) (Generation, error) {
	generation, err := store.Get(ctx, profileID, strategyID)
	if err != nil {
		return Generation{}, err
	}
	patch, err := DiffOverrides(generation.GeneratedConfig, edited)
	if err != nil {
		return Generation{}, err
	}
	if err := store.updatePatch(ctx, profileID, strategyID, patch); err != nil {
		return Generation{}, err
	}
	return store.Get(ctx, profileID, strategyID)
}

func (store *GenerationStore) ReplaceRecommendation(
	ctx context.Context,
	profileID, strategyID int64,
	input RecommendationInput,
) (Generation, profile.RoutingStrategyConfig, error) {
	if err := validateGenerationIdentity(strategyID, profileID, input.GeneratorVersion, input.SourceDigest); err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	generation, err := store.Get(ctx, profileID, strategyID)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	merged, err := ApplyOverrides(input.GeneratedConfig, generation.ManualOverrides)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	configJSON, err := json.Marshal(input.GeneratedConfig)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	explanations := input.Explanations
	if explanations == nil {
		explanations = []strategycompiler.Explanation{}
	}
	explanationsJSON, err := json.Marshal(explanations)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	_, err = store.db.ExecContext(ctx, `
		UPDATE routing_strategy_generations
		SET generator_version = ?, source_snapshot_digest = ?, generated_config_json = ?,
		    explanations_json = ?, updated_at = ?
		WHERE profile_id = ? AND strategy_id = ?
	`, input.GeneratorVersion, input.SourceDigest, string(configJSON), string(explanationsJSON),
		store.now().UTC().Format(time.RFC3339Nano), profileID, strategyID)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, fmt.Errorf("replace generated strategy recommendation: %w", err)
	}
	updated, err := store.Get(ctx, profileID, strategyID)
	return updated, merged, err
}

func (store *GenerationStore) RestoreOverride(
	ctx context.Context,
	profileID, strategyID int64,
	path string,
) (Generation, profile.RoutingStrategyConfig, error) {
	generation, err := store.Get(ctx, profileID, strategyID)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	patch := RemoveOverride(generation.ManualOverrides, path)
	merged, err := ApplyOverrides(generation.GeneratedConfig, patch)
	if err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	if err := store.updatePatch(ctx, profileID, strategyID, patch); err != nil {
		return Generation{}, profile.RoutingStrategyConfig{}, err
	}
	updated, err := store.Get(ctx, profileID, strategyID)
	return updated, merged, err
}

func (store *GenerationStore) updatePatch(
	ctx context.Context,
	profileID, strategyID int64,
	patch OverridePatch,
) error {
	if patch == nil {
		patch = OverridePatch{}
	}
	contents, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode strategy manual overrides: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE routing_strategy_generations
		SET manual_override_patch_json = ?, updated_at = ?
		WHERE profile_id = ? AND strategy_id = ?
	`, string(contents), store.now().UTC().Format(time.RFC3339Nano), profileID, strategyID)
	if err != nil {
		return fmt.Errorf("update strategy manual overrides: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read strategy manual override update count: %w", err)
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (store *GenerationStore) requireDraft(ctx context.Context, profileID, strategyID int64) error {
	var state string
	var archivedAt sql.NullString
	err := store.db.QueryRowContext(ctx, `
		SELECT state, archived_at FROM routing_strategies WHERE id = ? AND profile_id = ?
	`, strategyID, profileID).Scan(&state, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read generated strategy state: %w", err)
	}
	if State(state) != StateDraft || archivedAt.Valid {
		return ErrImmutable
	}
	return nil
}

func insertGeneration(
	ctx context.Context,
	execer interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	},
	input GenerationInput,
	now string,
) error {
	intentJSON, err := json.Marshal(input.Intent)
	if err != nil {
		return fmt.Errorf("encode strategy generation intent: %w", err)
	}
	configJSON, err := json.Marshal(input.GeneratedConfig)
	if err != nil {
		return fmt.Errorf("encode generated strategy: %w", err)
	}
	explanations := input.Explanations
	if explanations == nil {
		explanations = []strategycompiler.Explanation{}
	}
	explanationsJSON, err := json.Marshal(explanations)
	if err != nil {
		return fmt.Errorf("encode strategy explanations: %w", err)
	}
	_, err = execer.ExecContext(ctx, `
		INSERT INTO routing_strategy_generations (
			strategy_id, profile_id, generator_version, generation_intent_json,
			source_snapshot_digest, generated_config_json, manual_override_patch_json,
			explanations_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)
	`, input.StrategyID, input.ProfileID, input.GeneratorVersion, string(intentJSON),
		input.SourceDigest, string(configJSON), string(explanationsJSON), now, now)
	if err != nil {
		return fmt.Errorf("create strategy generation metadata: %w", err)
	}
	return nil
}

func getGeneration(
	ctx context.Context,
	queryer queryRower,
	profileID, strategyID int64,
) (Generation, error) {
	return scanGeneration(queryer.QueryRowContext(ctx, `
		SELECT strategy_id, profile_id, generator_version, generation_intent_json,
		       source_snapshot_digest, generated_config_json, manual_override_patch_json,
		       explanations_json, created_at, updated_at
		FROM routing_strategy_generations
		WHERE profile_id = ? AND strategy_id = ?
	`, profileID, strategyID))
}

func updateGeneratedDraftConfig(
	ctx context.Context,
	tx *sql.Tx,
	record profile.Record,
	current Version,
	config profile.RoutingStrategyConfig,
	now string,
) (Version, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return Version{}, fmt.Errorf("encode routing strategy: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE routing_strategies
		SET name = ?, alias = ?, config_json = ?, updated_at = ?
		WHERE id = ? AND profile_id = ? AND state = 'draft' AND archived_at IS NULL
	`, config.Name, config.Alias, string(encoded), now, current.ID, record.ID)
	if err != nil {
		return Version{}, translateWriteError(err)
	}
	if err := requireOneCAS(result); err != nil {
		return Version{}, err
	}
	revision, err := pointerRevision(ctx, tx, record.ID)
	if err != nil {
		return Version{}, err
	}
	if err := insertEvent(ctx, tx, record.ID, current.ID, "update", StateDraft, StateDraft, revision, now); err != nil {
		return Version{}, err
	}
	return getVersion(ctx, tx, record.ID, current.ID)
}

func normalizePatch(patch OverridePatch) OverridePatch {
	if patch == nil {
		return OverridePatch{}
	}
	return patch
}

func validateGenerationIdentity(strategyID, profileID int64, version, digest string) error {
	if strategyID <= 0 || profileID <= 0 || version == "" || !generationDigestPattern.MatchString(digest) {
		return fmt.Errorf("invalid strategy generation metadata")
	}
	return nil
}
