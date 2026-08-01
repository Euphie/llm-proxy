package strategy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type State string

const (
	StateDraft      State = "draft"
	StateEvaluating State = "evaluating"
	StateReady      State = "ready"
	StateCanary     State = "canary"
	StateActive     State = "active"
)

var (
	ErrNotFound          = errors.New("routing strategy not found")
	ErrConflict          = errors.New("routing strategy revision conflict")
	ErrInvalidTransition = errors.New("invalid routing strategy transition")
	ErrImmutable         = errors.New("published routing strategy is immutable")
	ErrNoLastKnownGood   = errors.New("last-known-good routing strategy is unavailable")
)

type Version struct {
	ID        int64                         `json:"id"`
	ProfileID int64                         `json:"profile_id"`
	State     State                         `json:"state"`
	Config    profile.RoutingStrategyConfig `json:"config"`
	Rating    Rating                        `json:"rating"`
	CreatedAt time.Time                     `json:"created_at"`
	UpdatedAt time.Time                     `json:"updated_at"`
}

type Rating struct {
	DisplayScoreBPS       int    `json:"display_score_bps"`
	Grade                 string `json:"grade"`
	QualityFloorBPS       int    `json:"quality_floor_bps"`
	SevereErrorCeilingBPS int    `json:"severe_error_ceiling_bps"`
	PassingRoutes         int    `json:"passing_routes"`
	TotalRoutes           int    `json:"total_routes"`
	Source                string `json:"source"`
}

type Snapshot struct {
	Revision      int64    `json:"revision"`
	Active        Version  `json:"active"`
	Canary        *Version `json:"canary,omitempty"`
	CanaryBPS     int      `json:"canary_bps"`
	LastKnownGood *Version `json:"last_known_good,omitempty"`
}

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

func (s *Store) Bootstrap(ctx context.Context, record profile.Record) (snapshot Snapshot, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin strategy bootstrap: %w", err)
	}
	defer rollback(tx)

	snapshot, err = readSnapshot(ctx, tx, record.ID)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return Snapshot{}, fmt.Errorf("commit strategy bootstrap read: %w", err)
		}
		return snapshot, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Snapshot{}, err
	}
	if err := validateStrategy(record, record.Config.AutoRouting.Strategy); err != nil {
		return Snapshot{}, err
	}

	now := strategyTime(s.now())
	version, err := insertVersion(ctx, tx, record.ID, StateActive, record.Config.AutoRouting.Strategy, now)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO routing_strategy_pointers (
			profile_id, active_strategy_id, canary_bps, revision, updated_at
		) VALUES (?, ?, 0, 1, ?)
	`, record.ID, version.ID, now); err != nil {
		return Snapshot{}, fmt.Errorf("create strategy pointers: %w", err)
	}
	if err = insertEvent(ctx, tx, record.ID, version.ID, "bootstrap", "", StateActive, 1, now); err != nil {
		return Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit strategy bootstrap: %w", err)
	}
	return Snapshot{Revision: 1, Active: version}, nil
}

func (s *Store) Snapshot(ctx context.Context, profileID int64) (Snapshot, error) {
	return readSnapshot(ctx, s.db, profileID)
}

func (s *Store) ResolveRecord(
	ctx context.Context,
	record profile.Record,
) (profile.Record, Snapshot, error) {
	if !record.Config.AutoRouting.Enabled {
		return record, Snapshot{}, nil
	}
	snapshot, err := s.Snapshot(ctx, record.ID)
	if errors.Is(err, ErrNotFound) {
		snapshot, err = s.Bootstrap(ctx, record)
	}
	if err != nil {
		return profile.Record{}, Snapshot{}, err
	}
	record.Config.AutoRouting.Strategy = snapshot.Active.Config
	if _, err := record.Resolve(); err != nil {
		return profile.Record{}, Snapshot{}, err
	}
	return record, snapshot, nil
}

func (s *Store) List(ctx context.Context, profileID int64) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, profile_id, state, config_json, created_at, updated_at
		FROM routing_strategies
		WHERE profile_id = ?
		ORDER BY id DESC
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list routing strategies: %w", err)
	}
	defer rows.Close()
	versions := make([]Version, 0)
	for rows.Next() {
		version, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routing strategies: %w", err)
	}
	return versions, nil
}

func (s *Store) NextName(ctx context.Context, profileID int64, date time.Time) (string, error) {
	prefix := date.UTC().Format("20060102")
	rows, err := s.db.QueryContext(ctx, `
		SELECT name FROM routing_strategies
		WHERE profile_id = ? AND name LIKE ?
	`, profileID, prefix+"-%")
	if err != nil {
		return "", fmt.Errorf("list strategy names: %w", err)
	}
	defer rows.Close()
	maxSequence := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", fmt.Errorf("scan strategy name: %w", err)
		}
		if len(name) != 12 || !strings.HasPrefix(name, prefix+"-") {
			continue
		}
		sequence, err := strconv.Atoi(name[9:])
		if err == nil && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate strategy names: %w", err)
	}
	if maxSequence >= 999 {
		return "", ErrConflict
	}
	return fmt.Sprintf("%s-%03d", prefix, maxSequence+1), nil
}

func (s *Store) CreateDraft(
	ctx context.Context,
	record profile.Record,
	config profile.RoutingStrategyConfig,
) (version Version, err error) {
	if err := validateStrategy(record, config); err != nil {
		return Version{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, fmt.Errorf("begin create strategy draft: %w", err)
	}
	defer rollback(tx)
	snapshot, err := readSnapshot(ctx, tx, record.ID)
	if err != nil {
		return Version{}, err
	}
	now := strategyTime(s.now())
	version, err = insertVersion(ctx, tx, record.ID, StateDraft, config, now)
	if err != nil {
		return Version{}, err
	}
	if err = insertEvent(ctx, tx, record.ID, version.ID, "create", "", StateDraft, snapshot.Revision, now); err != nil {
		return Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return Version{}, fmt.Errorf("commit strategy draft: %w", err)
	}
	return version, nil
}

func (s *Store) UpdateDraft(
	ctx context.Context,
	record profile.Record,
	id int64,
	config profile.RoutingStrategyConfig,
) (version Version, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, fmt.Errorf("begin update strategy draft: %w", err)
	}
	defer rollback(tx)
	current, err := getVersion(ctx, tx, record.ID, id)
	if err != nil {
		return Version{}, err
	}
	if current.State != StateDraft {
		return Version{}, ErrImmutable
	}
	if config.Name != current.Config.Name {
		return Version{}, ErrImmutable
	}
	if err := validateStrategy(record, config); err != nil {
		return Version{}, err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return Version{}, fmt.Errorf("encode routing strategy: %w", err)
	}
	now := strategyTime(s.now())
	if _, err = tx.ExecContext(ctx, `
		UPDATE routing_strategies
		SET name = ?, alias = ?, config_json = ?, updated_at = ?
		WHERE id = ? AND profile_id = ? AND state = 'draft'
	`, config.Name, config.Alias, string(encoded), now, id, record.ID); err != nil {
		return Version{}, translateWriteError(err)
	}
	version, err = getVersion(ctx, tx, record.ID, id)
	if err != nil {
		return Version{}, err
	}
	revision, err := pointerRevision(ctx, tx, record.ID)
	if err != nil {
		return Version{}, err
	}
	if err = insertEvent(ctx, tx, record.ID, id, "update", StateDraft, StateDraft, revision, now); err != nil {
		return Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return Version{}, fmt.Errorf("commit strategy draft update: %w", err)
	}
	return version, nil
}

func (s *Store) Advance(
	ctx context.Context,
	profileID int64,
	id int64,
	from State,
	to State,
) (version Version, err error) {
	if !((from == StateDraft && to == StateEvaluating) ||
		(from == StateEvaluating && to == StateReady)) {
		return Version{}, ErrInvalidTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, fmt.Errorf("begin strategy transition: %w", err)
	}
	defer rollback(tx)
	current, err := getVersion(ctx, tx, profileID, id)
	if err != nil {
		return Version{}, err
	}
	if current.State != from {
		return Version{}, ErrConflict
	}
	revision, err := pointerRevision(ctx, tx, profileID)
	if err != nil {
		return Version{}, err
	}
	now := strategyTime(s.now())
	if _, err = tx.ExecContext(ctx, `
		UPDATE routing_strategies SET state = ?, updated_at = ?
		WHERE id = ? AND profile_id = ? AND state = ?
	`, to, now, id, profileID, from); err != nil {
		return Version{}, fmt.Errorf("advance routing strategy: %w", err)
	}
	if err = insertEvent(ctx, tx, profileID, id, "advance", from, to, revision, now); err != nil {
		return Version{}, err
	}
	version, err = getVersion(ctx, tx, profileID, id)
	if err != nil {
		return Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return Version{}, fmt.Errorf("commit strategy transition: %w", err)
	}
	return version, nil
}

func (s *Store) StartCanary(
	ctx context.Context,
	profileID int64,
	id int64,
	canaryBPS int,
	expectedRevision int64,
) (Snapshot, error) {
	if canaryBPS <= 0 || canaryBPS >= 10_000 {
		return Snapshot{}, ErrInvalidTransition
	}
	return s.updatePointers(ctx, profileID, expectedRevision, func(
		tx *sql.Tx,
		current Snapshot,
		now string,
	) error {
		if current.Canary != nil {
			return ErrConflict
		}
		candidate, err := getVersion(ctx, tx, profileID, id)
		if err != nil {
			return err
		}
		if candidate.State != StateReady {
			return ErrInvalidTransition
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'canary', updated_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("mark canary strategy: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE routing_strategy_pointers
			SET canary_strategy_id = ?, canary_bps = ?, revision = revision + 1, updated_at = ?
			WHERE profile_id = ? AND revision = ?
		`, id, canaryBPS, now, profileID, expectedRevision)
		if err != nil {
			return fmt.Errorf("publish canary pointer: %w", err)
		}
		if err := requireOneCAS(result); err != nil {
			return err
		}
		return insertEvent(ctx, tx, profileID, id, "start_canary", StateReady, StateCanary, expectedRevision+1, now)
	})
}

func (s *Store) Promote(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (Snapshot, error) {
	return s.updatePointers(ctx, profileID, expectedRevision, func(
		tx *sql.Tx,
		current Snapshot,
		now string,
	) error {
		if current.Canary == nil {
			return ErrInvalidTransition
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'ready', updated_at = ? WHERE id = ?`, now, current.Active.ID); err != nil {
			return fmt.Errorf("retire active strategy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'active', updated_at = ? WHERE id = ?`, now, current.Canary.ID); err != nil {
			return fmt.Errorf("activate canary strategy: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE routing_strategy_pointers
			SET active_strategy_id = canary_strategy_id,
			    canary_strategy_id = NULL,
			    last_known_good_strategy_id = active_strategy_id,
			    canary_bps = 0,
			    revision = revision + 1,
			    updated_at = ?
			WHERE profile_id = ? AND revision = ?
		`, now, profileID, expectedRevision)
		if err != nil {
			return fmt.Errorf("promote strategy pointer: %w", err)
		}
		if err := requireOneCAS(result); err != nil {
			return err
		}
		return insertEvent(ctx, tx, profileID, current.Canary.ID, "promote", StateCanary, StateActive, expectedRevision+1, now)
	})
}

func (s *Store) CancelCanary(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (Snapshot, error) {
	return s.updatePointers(ctx, profileID, expectedRevision, func(
		tx *sql.Tx,
		current Snapshot,
		now string,
	) error {
		if current.Canary == nil {
			return ErrInvalidTransition
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'ready', updated_at = ? WHERE id = ?`, now, current.Canary.ID); err != nil {
			return fmt.Errorf("restore canary strategy to ready: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE routing_strategy_pointers
			SET canary_strategy_id = NULL,
			    canary_bps = 0,
			    revision = revision + 1,
			    updated_at = ?
			WHERE profile_id = ? AND revision = ?
		`, now, profileID, expectedRevision)
		if err != nil {
			return fmt.Errorf("cancel canary pointer: %w", err)
		}
		if err := requireOneCAS(result); err != nil {
			return err
		}
		return insertEvent(ctx, tx, profileID, current.Canary.ID, "cancel_canary", StateCanary, StateReady, expectedRevision+1, now)
	})
}

func (s *Store) Rollback(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
) (Snapshot, error) {
	return s.updatePointers(ctx, profileID, expectedRevision, func(
		tx *sql.Tx,
		current Snapshot,
		now string,
	) error {
		if current.Canary != nil {
			return ErrInvalidTransition
		}
		if current.LastKnownGood == nil || current.LastKnownGood.ID == current.Active.ID {
			return ErrNoLastKnownGood
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'ready', updated_at = ? WHERE id = ?`, now, current.Active.ID); err != nil {
			return fmt.Errorf("retire rolled back strategy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routing_strategies SET state = 'active', updated_at = ? WHERE id = ?`, now, current.LastKnownGood.ID); err != nil {
			return fmt.Errorf("restore last-known-good strategy: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE routing_strategy_pointers
			SET active_strategy_id = last_known_good_strategy_id,
			    last_known_good_strategy_id = active_strategy_id,
			    revision = revision + 1,
			    updated_at = ?
			WHERE profile_id = ? AND revision = ?
		`, now, profileID, expectedRevision)
		if err != nil {
			return fmt.Errorf("rollback strategy pointer: %w", err)
		}
		if err := requireOneCAS(result); err != nil {
			return err
		}
		return insertEvent(ctx, tx, profileID, current.LastKnownGood.ID, "rollback", StateReady, StateActive, expectedRevision+1, now)
	})
}

func (s *Store) updatePointers(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
	update func(*sql.Tx, Snapshot, string) error,
) (snapshot Snapshot, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin strategy publication: %w", err)
	}
	defer rollback(tx)
	current, err := readSnapshot(ctx, tx, profileID)
	if err != nil {
		return Snapshot{}, err
	}
	if current.Revision != expectedRevision {
		return Snapshot{}, ErrConflict
	}
	if err := update(tx, current, strategyTime(s.now())); err != nil {
		return Snapshot{}, err
	}
	snapshot, err = readSnapshot(ctx, tx, profileID)
	if err != nil {
		return Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit strategy publication: %w", err)
	}
	return snapshot, nil
}

func validateStrategy(record profile.Record, config profile.RoutingStrategyConfig) error {
	if record.ID <= 0 || !record.Config.AutoRouting.Enabled {
		return fmt.Errorf("%w: Auto routing must be enabled before managing strategies", profile.ErrInvalidConfig)
	}
	record.Config.AutoRouting.Strategy = config
	_, err := record.Resolve()
	return err
}

func insertVersion(
	ctx context.Context,
	tx *sql.Tx,
	profileID int64,
	state State,
	config profile.RoutingStrategyConfig,
	now string,
) (Version, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return Version{}, fmt.Errorf("encode routing strategy: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO routing_strategies (
			profile_id, name, alias, state, config_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, profileID, config.Name, config.Alias, state, string(encoded), now, now)
	if err != nil {
		return Version{}, translateWriteError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Version{}, fmt.Errorf("read routing strategy ID: %w", err)
	}
	return Version{
		ID: id, ProfileID: profileID, State: state, Config: config,
		Rating:    rate(config),
		CreatedAt: mustStrategyTime(now), UpdatedAt: mustStrategyTime(now),
	}, nil
}

func readSnapshot(ctx context.Context, queryer queryRower, profileID int64) (Snapshot, error) {
	var activeID int64
	var canaryID, lkgID sql.NullInt64
	var snapshot Snapshot
	err := queryer.QueryRowContext(ctx, `
		SELECT active_strategy_id, canary_strategy_id,
		       last_known_good_strategy_id, canary_bps, revision
		FROM routing_strategy_pointers WHERE profile_id = ?
	`, profileID).Scan(&activeID, &canaryID, &lkgID, &snapshot.CanaryBPS, &snapshot.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read strategy pointers: %w", err)
	}
	snapshot.Active, err = getVersion(ctx, queryer, profileID, activeID)
	if err != nil {
		return Snapshot{}, err
	}
	if canaryID.Valid {
		version, err := getVersion(ctx, queryer, profileID, canaryID.Int64)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Canary = &version
	}
	if lkgID.Valid {
		version, err := getVersion(ctx, queryer, profileID, lkgID.Int64)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.LastKnownGood = &version
	}
	return snapshot, nil
}

func getVersion(ctx context.Context, queryer queryRower, profileID, id int64) (Version, error) {
	version, err := scanVersion(queryer.QueryRowContext(ctx, `
		SELECT id, profile_id, state, config_json, created_at, updated_at
		FROM routing_strategies WHERE profile_id = ? AND id = ?
	`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return version, err
}

type rowScanner interface {
	Scan(...any) error
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanVersion(row rowScanner) (Version, error) {
	var version Version
	var state string
	var configJSON, createdAt, updatedAt string
	if err := row.Scan(
		&version.ID,
		&version.ProfileID,
		&state,
		&configJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Version{}, err
	}
	version.State = State(state)
	if err := json.Unmarshal([]byte(configJSON), &version.Config); err != nil {
		return Version{}, fmt.Errorf("decode routing strategy: %w", err)
	}
	version.Rating = rate(version.Config)
	var err error
	version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Version{}, fmt.Errorf("parse strategy creation time: %w", err)
	}
	version.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Version{}, fmt.Errorf("parse strategy update time: %w", err)
	}
	return version, nil
}

func rate(config profile.RoutingStrategyConfig) Rating {
	rating := Rating{
		Grade: "D", TotalRoutes: len(config.Routes), Source: "configured",
		QualityFloorBPS: 10_000,
	}
	if len(config.Routes) == 0 {
		rating.QualityFloorBPS = 0
		return rating
	}
	for _, route := range config.Routes {
		routePassing := false
		for _, candidate := range route.Candidates {
			if candidate.QualityScoreBPS < route.MinQualityBPS ||
				candidate.SevereErrorRateBPS > route.MaxSevereErrorRateBPS {
				continue
			}
			routePassing = true
			if candidate.QualityScoreBPS < rating.QualityFloorBPS {
				rating.QualityFloorBPS = candidate.QualityScoreBPS
			}
			if candidate.SevereErrorRateBPS > rating.SevereErrorCeilingBPS {
				rating.SevereErrorCeilingBPS = candidate.SevereErrorRateBPS
			}
		}
		if routePassing {
			rating.PassingRoutes++
		}
	}
	if rating.PassingRoutes != rating.TotalRoutes {
		rating.QualityFloorBPS = 0
		return rating
	}
	rating.DisplayScoreBPS = (rating.QualityFloorBPS*85 +
		(10_000-rating.SevereErrorCeilingBPS)*15) / 100
	switch {
	case rating.DisplayScoreBPS >= 9800 && rating.SevereErrorCeilingBPS <= 100:
		rating.Grade = "A"
	case rating.DisplayScoreBPS >= 9500 && rating.SevereErrorCeilingBPS <= 250:
		rating.Grade = "B"
	case rating.DisplayScoreBPS >= 9000 && rating.SevereErrorCeilingBPS <= 500:
		rating.Grade = "C"
	}
	return rating
}

func pointerRevision(ctx context.Context, queryer queryRower, profileID int64) (int64, error) {
	var revision int64
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT revision FROM routing_strategy_pointers WHERE profile_id = ?`,
		profileID,
	).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("read strategy revision: %w", err)
	}
	return revision, nil
}

func insertEvent(
	ctx context.Context,
	tx *sql.Tx,
	profileID int64,
	strategyID int64,
	action string,
	from State,
	to State,
	revision int64,
	now string,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO routing_strategy_events (
			profile_id, strategy_id, action, from_state, to_state, revision, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, profileID, strategyID, action, from, to, revision, now); err != nil {
		return fmt.Errorf("record strategy event: %w", err)
	}
	return nil
}

func requireOneCAS(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read strategy CAS result: %w", err)
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func translateWriteError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrConflict
	}
	return fmt.Errorf("write routing strategy: %w", err)
}

func strategyTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func mustStrategyTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}
