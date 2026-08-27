package modeldirectory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (store *Store) List(ctx context.Context, profileID int64) ([]Record, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT profile_id, model_id, capability_json, status, status_reason,
		       created_at, updated_at, retired_at
		FROM profile_models
		WHERE profile_id = ?
		ORDER BY model_id
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list Profile models: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Profile models: %w", err)
	}
	return records, nil
}

func (store *Store) Get(ctx context.Context, profileID int64, modelID string) (Record, error) {
	record, err := scanRecord(store.db.QueryRowContext(ctx, `
		SELECT profile_id, model_id, capability_json, status, status_reason,
		       created_at, updated_at, retired_at
		FROM profile_models
		WHERE profile_id = ? AND model_id = ?
	`, profileID, modelID))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var capability []byte
	var status string
	var createdAt, updatedAt string
	var retiredAt sql.NullString
	if err := row.Scan(
		&record.ProfileID, &record.ModelID, &capability, &status, &record.StatusReason,
		&createdAt, &updatedAt, &retiredAt,
	); err != nil {
		return Record{}, err
	}
	record.Status = Status(status)
	if record.ProfileID <= 0 || record.ModelID == "" || !record.Status.valid() || !json.Valid(capability) {
		return Record{}, errors.New("persisted Profile model is invalid")
	}
	var err error
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Record{}, fmt.Errorf("parse Profile model created time: %w", err)
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("parse Profile model updated time: %w", err)
	}
	if retiredAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, retiredAt.String)
		if parseErr != nil {
			return Record{}, fmt.Errorf("parse Profile model retired time: %w", parseErr)
		}
		record.RetiredAt = &value
	}
	record.CapabilityJSON = append(record.CapabilityJSON[:0], capability...)
	return record, nil
}
