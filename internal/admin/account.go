package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrWeakPassword       = errors.New("password must be at least 10 characters")
)

type Account struct {
	ID                 int64
	Username           string
	MustChangePassword bool
	AuthVersion        int64
	UpdatedAt          time.Time
}

type AccountStore struct {
	db *sql.DB
}

func NewAccountStore(db *sql.DB) *AccountStore {
	return &AccountStore{db: db}
}

func (s *AccountStore) EnsureDefault(ctx context.Context) (Account, bool, error) {
	account, err := s.Get(ctx)
	if err == nil {
		return account, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, err
	}

	hash, err := HashPassword("admin")
	if err != nil {
		return Account{}, false, fmt.Errorf("hash default admin password: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_account (id, username, password_hash, must_change_password, auth_version, updated_at)
		VALUES (1, 'admin', ?, 1, 1, ?)
		ON CONFLICT(id) DO NOTHING`, hash, now)
	if err != nil {
		return Account{}, false, fmt.Errorf("create default admin account: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return Account{}, false, fmt.Errorf("check default admin account creation: %w", err)
	}
	account, err = s.Get(ctx)
	if err != nil {
		return Account{}, false, err
	}
	return account, created == 1, nil
}

func (s *AccountStore) Get(ctx context.Context) (Account, error) {
	return getAccount(ctx, s.db)
}

func (s *AccountStore) Authenticate(ctx context.Context, username, password string) (Account, error) {
	if username != "admin" {
		return Account{}, ErrInvalidCredentials
	}
	account, hash, err := getAccountWithHash(ctx, s.db)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrInvalidCredentials
	}
	if err != nil {
		return Account{}, err
	}
	if account.Username != "admin" || !VerifyPassword(hash, password) {
		return Account{}, ErrInvalidCredentials
	}
	return account, nil
}

func (s *AccountStore) ChangePassword(
	ctx context.Context,
	accountID int64,
	currentPassword, newPassword string,
) (err error) {
	if !utf8.ValidString(newPassword) || utf8.RuneCountInString(newPassword) < 10 {
		return ErrWeakPassword
	}
	if accountID != 1 {
		return ErrInvalidCredentials
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password change: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	account, hash, err := getAccountWithHash(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	if account.Username != "admin" || !VerifyPassword(hash, currentPassword) {
		return ErrInvalidCredentials
	}

	newHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash replacement password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE admin_account
		SET password_hash = ?, must_change_password = 0, auth_version = auth_version + 1, updated_at = ?
		WHERE id = 1`, newHash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("update admin password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions`); err != nil {
		return fmt.Errorf("delete admin sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password change: %w", err)
	}
	return nil
}

type accountQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getAccount(ctx context.Context, q accountQuerier) (Account, error) {
	account, _, err := getAccountWithHash(ctx, q)
	return account, err
}

func getAccountWithHash(ctx context.Context, q accountQuerier) (Account, string, error) {
	var account Account
	var hash, updatedAt string
	err := q.QueryRowContext(ctx, `
		SELECT id, username, password_hash, must_change_password, auth_version, updated_at
		FROM admin_account WHERE id = 1`).Scan(
		&account.ID,
		&account.Username,
		&hash,
		&account.MustChangePassword,
		&account.AuthVersion,
		&updatedAt,
	)
	if err != nil {
		return Account{}, "", err
	}
	account.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Account{}, "", fmt.Errorf("parse admin account timestamp: %w", err)
	}
	return account, hash, nil
}
