package routing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SessionIDHeader   = "X-LLM-Proxy-Session-ID"
	SessionPurposeLLM = "answer"
)

var sessionAuthHeaders = []string{
	"Authorization",
	"X-Api-Key",
	"Api-Key",
	"Anthropic-Api-Key",
}

type SessionBinding struct {
	ProfileID       int64
	Route           string
	Purpose         string
	Model           string
	QualityScoreBPS int
	Strategy        string
	ExpiresAt       time.Time
}

type SessionKey [sha256.Size]byte

type SessionStore struct {
	db  *sql.DB
	now func() time.Time
	key []byte
}

func NewSessionStore(db *sql.DB, now func() time.Time) (*SessionStore, error) {
	if db == nil {
		return nil, errors.New("routing Session database is required")
	}
	if now == nil {
		now = time.Now
	}
	var key []byte
	if err := db.QueryRow(
		`SELECT routing_session_hmac_key FROM app_settings WHERE id = 1`,
	).Scan(&key); err != nil {
		return nil, fmt.Errorf("load routing Session HMAC key: %w", err)
	}
	if len(key) != sha256.Size {
		return nil, errors.New("routing Session HMAC key is invalid")
	}
	return &SessionStore{db: db, now: now, key: append([]byte(nil), key...)}, nil
}

func (s *SessionStore) Key(
	headers http.Header,
	rawSessionID string,
	profileID int64,
	route string,
	purpose string,
) (SessionKey, bool) {
	var empty SessionKey
	if s == nil || profileID <= 0 || route == "" || purpose == "" ||
		!validRawSessionID(rawSessionID) {
		return empty, false
	}
	authDomain, ok := sessionAuthDomain(headers)
	if !ok {
		return empty, false
	}
	mac := hmac.New(sha256.New, s.key)
	writeSessionPart(mac, rawSessionID)
	writeSessionPart(mac, fmt.Sprint(profileID))
	writeSessionPart(mac, route)
	writeSessionPart(mac, purpose)
	_, _ = mac.Write(authDomain[:])
	var key SessionKey
	copy(key[:], mac.Sum(nil))
	return key, true
}

func (s *SessionStore) CanaryBucket(
	headers http.Header,
	rawSessionID string,
	profileID int64,
	strategyID int64,
) (int, bool) {
	if s == nil || profileID <= 0 || strategyID <= 0 {
		return 0, false
	}
	if rawSessionID != "" && !validRawSessionID(rawSessionID) {
		return 0, false
	}
	authDomain, ok := sessionAuthDomain(headers)
	if !ok {
		return 0, false
	}
	mac := hmac.New(sha256.New, s.key)
	writeSessionPart(mac, "canary")
	writeSessionPart(mac, rawSessionID)
	writeSessionPart(mac, fmt.Sprint(profileID))
	writeSessionPart(mac, fmt.Sprint(strategyID))
	_, _ = mac.Write(authDomain[:])
	sum := mac.Sum(nil)
	return int(binary.BigEndian.Uint64(sum[:8]) % 10_000), true
}

func (s *SessionStore) Get(
	ctx context.Context,
	key SessionKey,
) (SessionBinding, bool, error) {
	if s == nil {
		return SessionBinding{}, false, nil
	}
	var binding SessionBinding
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT profile_id, route_id, purpose, model, quality_score_bps,
		       strategy_name, expires_at
		FROM routing_session_bindings
		WHERE key_hash = ?
	`, key[:]).Scan(
		&binding.ProfileID,
		&binding.Route,
		&binding.Purpose,
		&binding.Model,
		&binding.QualityScoreBPS,
		&binding.Strategy,
		&expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionBinding{}, false, nil
	}
	if err != nil {
		return SessionBinding{}, false, fmt.Errorf("read routing Session binding: %w", err)
	}
	binding.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return SessionBinding{}, false, fmt.Errorf("parse routing Session expiry: %w", err)
	}
	if !binding.ExpiresAt.After(s.now().UTC()) {
		if _, err := s.db.ExecContext(
			ctx,
			`DELETE FROM routing_session_bindings WHERE key_hash = ?`,
			key[:],
		); err != nil {
			return SessionBinding{}, false, fmt.Errorf("delete expired routing Session binding: %w", err)
		}
		return SessionBinding{}, false, nil
	}
	return binding, true, nil
}

func (s *SessionStore) Bind(
	ctx context.Context,
	key SessionKey,
	binding SessionBinding,
	ttl time.Duration,
) error {
	if s == nil {
		return nil
	}
	if binding.ProfileID <= 0 || binding.Route == "" || binding.Purpose == "" ||
		binding.Model == "" || binding.Strategy == "" ||
		binding.QualityScoreBPS < 0 || binding.QualityScoreBPS > 10_000 || ttl <= 0 {
		return errors.New("routing Session binding is invalid")
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(
		ctx,
		`DELETE FROM routing_session_bindings WHERE expires_at <= ?`,
		formatSessionTime(now),
	); err != nil {
		return fmt.Errorf("clean expired routing Session bindings: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO routing_session_bindings (
			key_hash, profile_id, route_id, purpose, model, quality_score_bps,
			strategy_name, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key_hash) DO UPDATE SET
			profile_id = excluded.profile_id,
			route_id = excluded.route_id,
			purpose = excluded.purpose,
			model = excluded.model,
			quality_score_bps = excluded.quality_score_bps,
			strategy_name = excluded.strategy_name,
			updated_at = excluded.updated_at,
			expires_at = excluded.expires_at
		WHERE excluded.quality_score_bps >= routing_session_bindings.quality_score_bps
	`,
		key[:],
		binding.ProfileID,
		binding.Route,
		binding.Purpose,
		binding.Model,
		binding.QualityScoreBPS,
		binding.Strategy,
		formatSessionTime(now),
		formatSessionTime(now),
		formatSessionTime(now.Add(ttl)),
	)
	if err != nil {
		return fmt.Errorf("save routing Session binding: %w", err)
	}
	return nil
}

func formatSessionTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func validRawSessionID(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func sessionAuthDomain(headers http.Header) ([sha256.Size]byte, bool) {
	hash := sha256.New()
	found := false
	for _, name := range sessionAuthHeaders {
		values := headers.Values(name)
		for _, value := range values {
			if value == "" {
				continue
			}
			found = true
			writeSessionPart(hash, strings.ToLower(name))
			writeSessionPart(hash, value)
		}
	}
	var domain [sha256.Size]byte
	if !found {
		return domain, false
	}
	copy(domain[:], hash.Sum(nil))
	return domain, true
}

type sessionHashWriter interface {
	Write([]byte) (int, error)
}

func writeSessionPart(writer sessionHashWriter, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}
