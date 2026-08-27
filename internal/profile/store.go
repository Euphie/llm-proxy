package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrSlugConflict    = errors.New("profile slug conflict")
	ErrNotFound        = errors.New("profile not found")
	ErrDefaultRequired = errors.New("default profile is required")
)

type SaveInput struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Config      Config
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Save(
	ctx context.Context,
	input SaveInput,
	makeDefault bool,
) (record Record, err error) {
	record = Record{
		ID:          input.ID,
		Slug:        input.Slug,
		DisplayName: input.DisplayName,
		Enabled:     input.Enabled,
		Config:      input.Config,
	}
	if _, err := record.Resolve(); err != nil {
		return Record{}, err
	}
	if input.Config.Version != 1 {
		return Record{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidConfig, input.Config.Version)
	}
	if makeDefault && !input.Enabled {
		return Record{}, ErrDefaultRequired
	}
	configJSON, err := json.Marshal(input.Config)
	if err != nil {
		return Record{}, fmt.Errorf("%w: encode config: %v", ErrInvalidConfig, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin save profile: %w", err)
	}
	defer rollbackOnError(tx, &err)

	defaultID, err := readDefaultID(ctx, tx)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if input.ID == 0 {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO profiles (
			slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
			input.Slug, input.DisplayName, input.Enabled, string(configJSON), now, now)
		if execErr != nil {
			return Record{}, translateWriteError(ctx, tx, input.Slug, 0, execErr)
		}
		record.ID, err = result.LastInsertId()
		if err != nil {
			return Record{}, fmt.Errorf("read inserted profile ID: %w", err)
		}
	} else {
		if defaultID.Valid && defaultID.Int64 == input.ID && !input.Enabled {
			return Record{}, ErrDefaultRequired
		}
		result, execErr := tx.ExecContext(ctx, `UPDATE profiles
			SET slug = ?, display_name = ?, enabled = ?, config_json = ?, updated_at = ?
			WHERE id = ?`,
			input.Slug, input.DisplayName, input.Enabled, string(configJSON), now, input.ID)
		if execErr != nil {
			return Record{}, translateWriteError(ctx, tx, input.Slug, input.ID, execErr)
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return Record{}, fmt.Errorf("read updated profile count: %w", execErr)
		}
		if affected == 0 {
			return Record{}, ErrNotFound
		}
	}

	if !makeDefault {
		if err := requireEnabledDefault(ctx, tx, defaultID); err != nil {
			return Record{}, err
		}
	}
	if makeDefault {
		if err := writeDefaultID(ctx, tx, record.ID, now); err != nil {
			return Record{}, err
		}
	}
	record, err = getRecord(ctx, tx, record.ID)
	if err != nil {
		return Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit save profile: %w", err)
	}
	return record, nil
}

func (s *Store) LoadSnapshot(ctx context.Context) (
	records []Record,
	defaultID int64,
	err error,
) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("begin profile snapshot: %w", err)
	}
	defer rollbackOnError(tx, &err)

	rows, err := tx.QueryContext(ctx, `SELECT
		id, slug, display_name, enabled, config_json, created_at, updated_at
		FROM profiles ORDER BY id`)
	if err != nil {
		return nil, 0, fmt.Errorf("load profiles: %w", err)
	}
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			rows.Close()
			return nil, 0, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close profiles: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate profiles: %w", err)
	}

	storedDefault, err := readDefaultID(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	if storedDefault.Valid {
		defaultID = storedDefault.Int64
		if !containsEnabledRecord(records, defaultID) {
			return nil, 0, ErrDefaultRequired
		}
	} else if len(records) != 0 {
		return nil, 0, ErrDefaultRequired
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit profile snapshot: %w", err)
	}
	return records, defaultID, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Record, error) {
	return getRecord(ctx, s.db, id)
}

func (s *Store) Copy(
	ctx context.Context,
	id int64,
	slug string,
	displayName string,
) (Record, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	return s.Save(ctx, SaveInput{
		Slug:        slug,
		DisplayName: displayName,
		Enabled:     source.Enabled,
		Config:      source.Config,
	}, false)
}

func (s *Store) SetDefault(ctx context.Context, id int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set default profile: %w", err)
	}
	defer rollbackOnError(tx, &err)

	var enabled bool
	if err := tx.QueryRowContext(
		ctx,
		`SELECT enabled FROM profiles WHERE id = ?`,
		id,
	).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load default profile: %w", err)
	}
	if !enabled {
		return ErrDefaultRequired
	}
	if err := writeDefaultID(ctx, tx, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit default profile: %w", err)
	}
	return nil
}

func (s *Store) Delete(
	ctx context.Context,
	id int64,
	replacementDefaultID int64,
) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete profile: %w", err)
	}
	defer rollbackOnError(tx, &err)

	var exists int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM profiles WHERE id = ?`,
		id,
	).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load deleted profile: %w", err)
	}
	defaultID, err := readDefaultID(ctx, tx)
	if err != nil {
		return err
	}
	if !defaultID.Valid {
		return ErrDefaultRequired
	}
	if defaultID.Int64 == id {
		if replacementDefaultID == 0 || replacementDefaultID == id {
			return ErrDefaultRequired
		}
		var enabled bool
		if err := tx.QueryRowContext(
			ctx,
			`SELECT enabled FROM profiles WHERE id = ?`,
			replacementDefaultID,
		).Scan(&enabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load replacement default profile: %w", err)
		}
		if !enabled {
			return ErrDefaultRequired
		}
		if err := writeDefaultID(
			ctx,
			tx,
			replacementDefaultID,
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	} else if err := requireEnabledDefault(ctx, tx, defaultID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete profile: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRecord(ctx context.Context, queryer queryRower, id int64) (Record, error) {
	record, err := scanRecord(queryer.QueryRowContext(ctx, `SELECT
		id, slug, display_name, enabled, config_json, created_at, updated_at
		FROM profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func scanRecord(row rowScanner) (Record, error) {
	var (
		record               Record
		configJSON           string
		createdAt, updatedAt string
	)
	if err := row.Scan(
		&record.ID,
		&record.Slug,
		&record.DisplayName,
		&record.Enabled,
		&configJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(configJSON), &record.Config); err != nil {
		return Record{}, fmt.Errorf("%w: decode config: %v", ErrInvalidConfig, err)
	}
	if record.Config.Version != 1 && record.Config.Version != 2 {
		return Record{}, fmt.Errorf(
			"%w: unsupported version %d",
			ErrInvalidConfig,
			record.Config.Version,
		)
	}
	var err error
	record.CreatedAt, err = parseStoredTime(createdAt)
	if err != nil {
		return Record{}, fmt.Errorf("parse profile created time: %w", err)
	}
	record.UpdatedAt, err = parseStoredTime(updatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("parse profile updated time: %w", err)
	}
	return record, nil
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func readDefaultID(ctx context.Context, tx *sql.Tx) (sql.NullInt64, error) {
	var id sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT default_profile_id FROM app_settings WHERE id = 1`,
	).Scan(&id); err != nil {
		return sql.NullInt64{}, fmt.Errorf("load default profile setting: %w", err)
	}
	return id, nil
}

func writeDefaultID(
	ctx context.Context,
	tx *sql.Tx,
	id int64,
	updatedAt string,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE app_settings
		SET default_profile_id = ?, updated_at = ? WHERE id = 1`,
		id,
		updatedAt,
	)
	if err != nil {
		return fmt.Errorf("update default profile setting: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read app settings update count: %w", err)
	}
	if affected != 1 {
		return ErrDefaultRequired
	}
	return nil
}

func requireEnabledDefault(
	ctx context.Context,
	tx *sql.Tx,
	defaultID sql.NullInt64,
) error {
	if !defaultID.Valid {
		return ErrDefaultRequired
	}
	var enabled bool
	if err := tx.QueryRowContext(
		ctx,
		`SELECT enabled FROM profiles WHERE id = ?`,
		defaultID.Int64,
	).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDefaultRequired
		}
		return fmt.Errorf("load current default profile: %w", err)
	}
	if !enabled {
		return ErrDefaultRequired
	}
	return nil
}

func containsEnabledRecord(records []Record, id int64) bool {
	for _, record := range records {
		if record.ID == id {
			return record.Enabled
		}
	}
	return false
}

func translateWriteError(
	ctx context.Context,
	tx *sql.Tx,
	slug string,
	currentID int64,
	writeErr error,
) error {
	var sqliteErr *sqlite.Error
	if errors.As(writeErr, &sqliteErr) &&
		sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		var exists int
		err := tx.QueryRowContext(
			ctx,
			`SELECT 1 FROM profiles WHERE slug = ? AND id <> ?`,
			slug,
			currentID,
		).Scan(&exists)
		if err == nil {
			return fmt.Errorf("%w: %v", ErrSlugConflict, writeErr)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf(
				"write profile: %w",
				errors.Join(writeErr, fmt.Errorf("check slug conflict: %w", err)),
			)
		}
	}
	return fmt.Errorf("write profile: %w", writeErr)
}

func rollbackOnError(tx *sql.Tx, err *error) {
	if *err != nil {
		_ = tx.Rollback()
	}
}
