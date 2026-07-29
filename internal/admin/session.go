package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const (
	sessionTTL        = 24 * time.Hour
	sessionTimeLayout = "2006-01-02T15:04:05.000000000Z"
)

var (
	ErrInvalidSession = errors.New("invalid session")
	ErrInvalidCSRF    = errors.New("invalid csrf token")
)

type Session struct {
	Token       string
	CSRFToken   string
	AuthVersion int64
	ExpiresAt   time.Time
}

type SessionStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewSessionStore(db *sql.DB, now func() time.Time) *SessionStore {
	return &SessionStore{db: db, now: now}
}

func (s *SessionStore) Create(ctx context.Context, authVersion int64, ttl time.Duration) (Session, error) {
	token, err := newToken()
	if err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	csrfToken, err := newToken()
	if err != nil {
		return Session{}, fmt.Errorf("generate csrf token: %w", err)
	}

	now := s.now().UTC()
	session := Session{
		Token:       token,
		CSRFToken:   csrfToken,
		AuthVersion: authVersion,
		ExpiresAt:   now.Add(ttl),
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_sessions (token_hash, csrf_hash, auth_version, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		tokenHash(token),
		tokenHash(csrfToken),
		authVersion,
		now.Format(sessionTimeLayout),
		session.ExpiresAt.Format(sessionTimeLayout),
	); err != nil {
		return Session{}, fmt.Errorf("create admin session: %w", err)
	}
	return session, nil
}

func (s *SessionStore) Authenticate(ctx context.Context, token string, authVersion int64) (Session, error) {
	storedTokenHash, _, storedAuthVersion, expiresAt, err := s.find(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidSession
	}
	if err != nil {
		return Session{}, err
	}
	if subtle.ConstantTimeCompare(storedTokenHash, tokenHash(token)) != 1 ||
		storedAuthVersion != authVersion ||
		!expiresAt.After(s.now()) {
		return Session{}, ErrInvalidSession
	}
	return Session{AuthVersion: storedAuthVersion, ExpiresAt: expiresAt}, nil
}

func (s *SessionStore) VerifyCSRF(ctx context.Context, token, csrfToken string) error {
	storedTokenHash, storedCSRFHash, _, expiresAt, err := s.find(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidSession
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(storedTokenHash, tokenHash(token)) != 1 || !expiresAt.After(s.now()) {
		return ErrInvalidSession
	}
	if subtle.ConstantTimeCompare(storedCSRFHash, tokenHash(csrfToken)) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (s *SessionStore) Revoke(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash(token)); err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	return nil
}

func (s *SessionStore) CleanupExpired(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ?`, s.now().UTC().Format(sessionTimeLayout)); err != nil {
		return fmt.Errorf("clean up expired admin sessions: %w", err)
	}
	return nil
}

func (s *SessionStore) find(ctx context.Context, token string) ([]byte, []byte, int64, time.Time, error) {
	var storedTokenHash, csrfHash []byte
	var authVersion int64
	var expiresAtText string
	err := s.db.QueryRowContext(ctx, `
		SELECT token_hash, csrf_hash, auth_version, expires_at
		FROM admin_sessions
		WHERE token_hash = ?`, tokenHash(token)).Scan(&storedTokenHash, &csrfHash, &authVersion, &expiresAtText)
	if err != nil {
		return nil, nil, 0, time.Time{}, fmt.Errorf("find admin session: %w", err)
	}
	expiresAt, err := time.Parse(sessionTimeLayout, expiresAtText)
	if err != nil {
		return nil, nil, 0, time.Time{}, fmt.Errorf("parse admin session expiration: %w", err)
	}
	return storedTokenHash, csrfHash, authVersion, expiresAt, nil
}

func newToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func tokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
