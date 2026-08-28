package aggregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type Store struct {
	db *sql.DB
}

type Snapshot struct {
	Providers []ProviderAccount
	Gateways  []Gateway
	Keys      []IssuedKey
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) SaveProvider(ctx context.Context, input ProviderAccountInput) (record ProviderAccount, err error) {
	input, err = NormalizeProviderInput(input)
	if err != nil {
		return ProviderAccount{}, err
	}
	if input.ID == 0 && input.Secret == "" {
		return ProviderAccount{}, fmt.Errorf("%w: provider secret is required", ErrInvalidConfig)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderAccount{}, fmt.Errorf("begin save provider account: %w", err)
	}
	defer rollbackOnError(tx, &err)

	now := storedNow()
	if input.ID == 0 {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO provider_accounts (
				slug, display_name, enabled, protocol, upstream, auth_header, secret_value, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			input.Slug, input.DisplayName, input.Enabled, input.Protocol, input.Upstream,
			input.AuthHeader, input.Secret, now, now)
		if execErr != nil {
			return ProviderAccount{}, translateAggregateWriteError(ErrProviderSlugConflict, input.Slug, execErr)
		}
		input.ID, err = result.LastInsertId()
		if err != nil {
			return ProviderAccount{}, fmt.Errorf("read inserted provider account ID: %w", err)
		}
	} else if input.Secret == "" {
		result, execErr := tx.ExecContext(ctx, `UPDATE provider_accounts
				SET slug = ?, display_name = ?, enabled = ?, protocol = ?, upstream = ?,
				    auth_header = ?, updated_at = ?
				WHERE id = ?`,
			input.Slug, input.DisplayName, input.Enabled, input.Protocol, input.Upstream,
			input.AuthHeader, now, input.ID)
		if execErr != nil {
			return ProviderAccount{}, translateAggregateWriteError(ErrProviderSlugConflict, input.Slug, execErr)
		}
		if err := requireRowsAffected(result, ErrNotFound); err != nil {
			return ProviderAccount{}, err
		}
	} else {
		result, execErr := tx.ExecContext(ctx, `UPDATE provider_accounts
			SET slug = ?, display_name = ?, enabled = ?, protocol = ?, upstream = ?,
			    auth_header = ?, secret_value = ?, updated_at = ?
			WHERE id = ?`,
			input.Slug, input.DisplayName, input.Enabled, input.Protocol, input.Upstream,
			input.AuthHeader, input.Secret, now, input.ID)
		if execErr != nil {
			return ProviderAccount{}, translateAggregateWriteError(ErrProviderSlugConflict, input.Slug, execErr)
		}
		if err := requireRowsAffected(result, ErrNotFound); err != nil {
			return ProviderAccount{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_models WHERE provider_account_id = ?`, input.ID); err != nil {
		return ProviderAccount{}, fmt.Errorf("replace provider models: %w", err)
	}
	for _, model := range input.Models {
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_models (
			provider_account_id, model_id, display_name, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
			input.ID, model.ModelID, model.DisplayName, model.Enabled, now, now); err != nil {
			return ProviderAccount{}, fmt.Errorf("insert provider model: %w", err)
		}
	}
	record, err = getProvider(ctx, tx, input.ID)
	if err != nil {
		return ProviderAccount{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProviderAccount{}, fmt.Errorf("commit save provider account: %w", err)
	}
	return record, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]ProviderAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, slug, display_name, enabled, protocol, upstream, auth_header, secret_value, created_at, updated_at
		FROM provider_accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list provider accounts: %w", err)
	}
	defer rows.Close()
	var providers []ProviderAccount
	for rows.Next() {
		provider, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		models, err := s.listProviderModels(ctx, provider.ID)
		if err != nil {
			return nil, err
		}
		provider.Models = models
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider accounts: %w", err)
	}
	return providers, nil
}

func (s *Store) GetProvider(ctx context.Context, id int64) (ProviderAccount, error) {
	return getProvider(ctx, s.db, id)
}

func (s *Store) SaveGateway(ctx context.Context, input GatewayInput) (record Gateway, err error) {
	input, err = NormalizeGatewayInput(input)
	if err != nil {
		return Gateway{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Gateway{}, fmt.Errorf("begin save aggregate gateway: %w", err)
	}
	defer rollbackOnError(tx, &err)

	providers, err := loadProvidersByID(ctx, tx)
	if err != nil {
		return Gateway{}, err
	}
	for _, route := range input.Routes {
		provider := providers[route.ProviderAccountID]
		if provider == nil {
			return Gateway{}, ErrNotFound
		}
		if provider.Protocol != input.Protocol {
			return Gateway{}, fmt.Errorf("%w: route provider protocol mismatch", ErrInvalidConfig)
		}
		if !provider.HasModel(route.ProviderModel) {
			return Gateway{}, fmt.Errorf("%w: route provider model %q is not configured", ErrInvalidConfig, route.ProviderModel)
		}
	}

	now := storedNow()
	if input.ID == 0 {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO aggregate_gateways (
			slug, display_name, enabled, protocol, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
			input.Slug, input.DisplayName, input.Enabled, input.Protocol, now, now)
		if execErr != nil {
			return Gateway{}, translateAggregateWriteError(ErrGatewaySlugConflict, input.Slug, execErr)
		}
		input.ID, err = result.LastInsertId()
		if err != nil {
			return Gateway{}, fmt.Errorf("read inserted aggregate gateway ID: %w", err)
		}
	} else {
		result, execErr := tx.ExecContext(ctx, `UPDATE aggregate_gateways
			SET slug = ?, display_name = ?, enabled = ?, protocol = ?, updated_at = ?
			WHERE id = ?`,
			input.Slug, input.DisplayName, input.Enabled, input.Protocol, now, input.ID)
		if execErr != nil {
			return Gateway{}, translateAggregateWriteError(ErrGatewaySlugConflict, input.Slug, execErr)
		}
		if err := requireRowsAffected(result, ErrNotFound); err != nil {
			return Gateway{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM aggregate_model_routes WHERE gateway_id = ?`, input.ID); err != nil {
			return Gateway{}, fmt.Errorf("replace aggregate gateway routes: %w", err)
		}
	}
	for _, route := range input.Routes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO aggregate_model_routes (
			gateway_id, public_model, provider_account_id, provider_model,
			enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			input.ID, route.PublicModel, route.ProviderAccountID, route.ProviderModel,
			route.Enabled, now, now); err != nil {
			return Gateway{}, fmt.Errorf("insert aggregate model route: %w", err)
		}
	}
	record, err = getGateway(ctx, tx, input.ID)
	if err != nil {
		return Gateway{}, err
	}
	if err = tx.Commit(); err != nil {
		return Gateway{}, fmt.Errorf("commit save aggregate gateway: %w", err)
	}
	return record, nil
}

func (s *Store) ListGateways(ctx context.Context) ([]Gateway, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, slug, display_name, enabled, protocol, created_at, updated_at
		FROM aggregate_gateways ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list aggregate gateways: %w", err)
	}
	defer rows.Close()
	var gateways []Gateway
	for rows.Next() {
		gateway, err := scanGatewayBase(rows)
		if err != nil {
			return nil, err
		}
		routes, err := s.listRoutes(ctx, gateway.ID)
		if err != nil {
			return nil, err
		}
		gateway.Routes = routes
		gateways = append(gateways, gateway)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate aggregate gateways: %w", err)
	}
	return gateways, nil
}

func (s *Store) GetGateway(ctx context.Context, id int64) (Gateway, error) {
	return getGateway(ctx, s.db, id)
}

func (s *Store) CreateKey(ctx context.Context, input IssuedKeyInput) (IssuedKey, error) {
	input, err := NormalizeIssuedKeyInput(input)
	if err != nil {
		return IssuedKey{}, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM aggregate_gateways WHERE id = ?`, input.GatewayID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedKey{}, ErrNotFound
		}
		return IssuedKey{}, fmt.Errorf("load aggregate gateway for key: %w", err)
	}
	key, hash, prefix, lastFour, err := GenerateKey()
	if err != nil {
		return IssuedKey{}, err
	}
	var expiresAt any
	if input.ExpiresAt != nil {
		expiresAt = input.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	now := storedNow()
	result, err := s.db.ExecContext(ctx, `INSERT INTO aggregate_issued_keys (
		gateway_id, name, prefix, last_four, token_hash, enabled, expires_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.GatewayID, input.Name, prefix, lastFour, hash, input.Enabled, expiresAt, now, now)
	if err != nil {
		return IssuedKey{}, fmt.Errorf("create aggregate gateway key: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return IssuedKey{}, fmt.Errorf("read aggregate gateway key ID: %w", err)
	}
	record, err := s.GetKey(ctx, id)
	if err != nil {
		return IssuedKey{}, err
	}
	record.Key = key
	return record, nil
}

func (s *Store) ListKeys(ctx context.Context, gatewayID int64) ([]IssuedKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, gateway_id, name, prefix, last_four, token_hash, enabled,
		expires_at, last_used_at, created_at, updated_at
		FROM aggregate_issued_keys
		WHERE gateway_id = ? ORDER BY id`, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list aggregate gateway keys: %w", err)
	}
	defer rows.Close()
	var keys []IssuedKey
	for rows.Next() {
		key, err := scanIssuedKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate aggregate gateway keys: %w", err)
	}
	return keys, nil
}

func (s *Store) GetKey(ctx context.Context, id int64) (IssuedKey, error) {
	key, err := scanIssuedKey(s.db.QueryRowContext(ctx, `SELECT
		id, gateway_id, name, prefix, last_four, token_hash, enabled,
		expires_at, last_used_at, created_at, updated_at
		FROM aggregate_issued_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return IssuedKey{}, ErrNotFound
	}
	return key, err
}

func (s *Store) RecordKeyUse(ctx context.Context, id int64, when time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE aggregate_issued_keys
		SET last_used_at = ? WHERE id = ?`, when.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record aggregate gateway key use: %w", err)
	}
	return nil
}

func (s *Store) LoadSnapshot(ctx context.Context) (Snapshot, error) {
	providers, err := s.ListProviders(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	gateways, err := s.ListGateways(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, gateway_id, name, prefix, last_four, token_hash, enabled,
		expires_at, last_used_at, created_at, updated_at
		FROM aggregate_issued_keys ORDER BY id`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list aggregate gateway issued keys: %w", err)
	}
	defer rows.Close()
	var keys []IssuedKey
	for rows.Next() {
		key, err := scanIssuedKey(rows)
		if err != nil {
			return Snapshot{}, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("iterate aggregate gateway issued keys: %w", err)
	}
	return Snapshot{Providers: providers, Gateways: gateways, Keys: keys}, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type queryRowerQueryer interface {
	queryRower
	queryer
}

type rowScanner interface {
	Scan(dest ...any) error
}

func getProvider(ctx context.Context, q queryRowerQueryer, id int64) (ProviderAccount, error) {
	provider, err := scanProvider(q.QueryRowContext(ctx, `SELECT
			id, slug, display_name, enabled, protocol, upstream, auth_header, secret_value, created_at, updated_at
			FROM provider_accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderAccount{}, ErrNotFound
	}
	if err != nil {
		return ProviderAccount{}, err
	}
	models, err := listProviderModels(ctx, q, provider.ID)
	if err != nil {
		return ProviderAccount{}, err
	}
	provider.Models = models
	return provider, nil
}

func getGateway(ctx context.Context, q queryRowerQueryer, id int64) (Gateway, error) {
	gateway, err := scanGatewayBase(q.QueryRowContext(ctx, `SELECT
		id, slug, display_name, enabled, protocol, created_at, updated_at
		FROM aggregate_gateways WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Gateway{}, ErrNotFound
	}
	if err != nil {
		return Gateway{}, err
	}
	routes, err := listRoutes(ctx, q, gateway.ID)
	if err != nil {
		return Gateway{}, err
	}
	gateway.Routes = routes
	return gateway, nil
}

func (s *Store) listRoutes(ctx context.Context, gatewayID int64) ([]ModelRoute, error) {
	return listRoutes(ctx, s.db, gatewayID)
}

func (s *Store) listProviderModels(ctx context.Context, providerID int64) ([]ProviderModel, error) {
	return listProviderModels(ctx, s.db, providerID)
}

func listProviderModels(ctx context.Context, q queryer, providerID int64) ([]ProviderModel, error) {
	rows, err := q.QueryContext(ctx, `SELECT
		id, provider_account_id, model_id, display_name, enabled, created_at, updated_at
		FROM provider_models WHERE provider_account_id = ?
		ORDER BY id`, providerID)
	if err != nil {
		return nil, fmt.Errorf("list provider models: %w", err)
	}
	defer rows.Close()
	var models []ProviderModel
	for rows.Next() {
		model, err := scanProviderModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider models: %w", err)
	}
	return models, nil
}

func listRoutes(ctx context.Context, q queryer, gatewayID int64) ([]ModelRoute, error) {
	rows, err := q.QueryContext(ctx, `SELECT
		id, gateway_id, public_model, provider_account_id, provider_model,
		enabled, created_at, updated_at
		FROM aggregate_model_routes WHERE gateway_id = ?
		ORDER BY public_model, id`, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list aggregate model routes: %w", err)
	}
	defer rows.Close()
	var routes []ModelRoute
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate aggregate model routes: %w", err)
	}
	return routes, nil
}

func loadProvidersByID(ctx context.Context, q queryer) (map[int64]*ProviderAccount, error) {
	rows, err := q.QueryContext(ctx, `SELECT
		id, slug, display_name, enabled, protocol, upstream, auth_header, secret_value, created_at, updated_at
		FROM provider_accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load provider accounts: %w", err)
	}
	defer rows.Close()
	providers := make(map[int64]*ProviderAccount)
	for rows.Next() {
		provider, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		models, err := listProviderModels(ctx, q, provider.ID)
		if err != nil {
			return nil, err
		}
		provider.Models = models
		item := provider
		providers[item.ID] = &item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider accounts: %w", err)
	}
	return providers, nil
}

func scanProvider(row rowScanner) (ProviderAccount, error) {
	var provider ProviderAccount
	var createdAt, updatedAt string
	if err := row.Scan(
		&provider.ID,
		&provider.Slug,
		&provider.DisplayName,
		&provider.Enabled,
		&provider.Protocol,
		&provider.Upstream,
		&provider.AuthHeader,
		&provider.Secret,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ProviderAccount{}, err
	}
	if err := parseTimes(createdAt, updatedAt, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
		return ProviderAccount{}, err
	}
	return provider, nil
}

func scanProviderModel(row rowScanner) (ProviderModel, error) {
	var model ProviderModel
	var createdAt, updatedAt string
	if err := row.Scan(
		&model.ID,
		&model.ProviderAccountID,
		&model.ModelID,
		&model.DisplayName,
		&model.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ProviderModel{}, err
	}
	if err := parseTimes(createdAt, updatedAt, &model.CreatedAt, &model.UpdatedAt); err != nil {
		return ProviderModel{}, err
	}
	return model, nil
}

func scanGatewayBase(row rowScanner) (Gateway, error) {
	var gateway Gateway
	var createdAt, updatedAt string
	if err := row.Scan(
		&gateway.ID,
		&gateway.Slug,
		&gateway.DisplayName,
		&gateway.Enabled,
		&gateway.Protocol,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Gateway{}, err
	}
	if err := parseTimes(createdAt, updatedAt, &gateway.CreatedAt, &gateway.UpdatedAt); err != nil {
		return Gateway{}, err
	}
	return gateway, nil
}

func scanRoute(row rowScanner) (ModelRoute, error) {
	var route ModelRoute
	var createdAt, updatedAt string
	if err := row.Scan(
		&route.ID,
		&route.GatewayID,
		&route.PublicModel,
		&route.ProviderAccountID,
		&route.ProviderModel,
		&route.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ModelRoute{}, err
	}
	if err := parseTimes(createdAt, updatedAt, &route.CreatedAt, &route.UpdatedAt); err != nil {
		return ModelRoute{}, err
	}
	return route, nil
}

func scanIssuedKey(row rowScanner) (IssuedKey, error) {
	var key IssuedKey
	var expiresAt, lastUsedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&key.ID,
		&key.GatewayID,
		&key.Name,
		&key.Prefix,
		&key.LastFour,
		&key.TokenHash,
		&key.Enabled,
		&expiresAt,
		&lastUsedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return IssuedKey{}, err
	}
	if expiresAt.Valid {
		parsed, err := parseStoredTime(expiresAt.String)
		if err != nil {
			return IssuedKey{}, fmt.Errorf("parse aggregate key expiration: %w", err)
		}
		key.ExpiresAt = &parsed
	}
	if lastUsedAt.Valid {
		parsed, err := parseStoredTime(lastUsedAt.String)
		if err != nil {
			return IssuedKey{}, fmt.Errorf("parse aggregate key last use: %w", err)
		}
		key.LastUsedAt = &parsed
	}
	if err := parseTimes(createdAt, updatedAt, &key.CreatedAt, &key.UpdatedAt); err != nil {
		return IssuedKey{}, err
	}
	return key, nil
}

func parseTimes(createdAt, updatedAt string, created, updated *time.Time) error {
	var err error
	*created, err = parseStoredTime(createdAt)
	if err != nil {
		return fmt.Errorf("parse created time: %w", err)
	}
	*updated, err = parseStoredTime(updatedAt)
	if err != nil {
		return fmt.Errorf("parse updated time: %w", err)
	}
	return nil
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func storedNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func requireRowsAffected(result sql.Result, notFound error) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated row count: %w", err)
	}
	if affected == 0 {
		return notFound
	}
	return nil
}

func rollbackOnError(tx *sql.Tx, err *error) {
	if *err != nil {
		_ = tx.Rollback()
	}
}

func translateAggregateWriteError(conflict error, slug string, err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return fmt.Errorf("%w: %q", conflict, slug)
	}
	return fmt.Errorf("write aggregate gateway record: %w", err)
}
