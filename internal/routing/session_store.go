package routing

import (
	"bytes"
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

	"github.com/Euphie/llm-proxy/internal/profile"
)

const (
	SessionIDHeader           = "X-LLM-Proxy-Session-ID"
	ClaudeCodeSessionIDHeader = "X-Claude-Code-Session-Id"
	CodexSessionIDHeader      = "Session-Id"
	CodexThreadIDHeader       = "Thread-Id"
	SessionPurposeLLM         = "answer"
)

var sessionAuthHeaders = []string{
	"Authorization",
	"X-Api-Key",
	"Api-Key",
	"Anthropic-Api-Key",
}

type SessionBinding struct {
	ProfileID                   int64
	Route                       string
	Purpose                     string
	TaskType                    string
	Difficulty                  Difficulty
	Model                       string
	QualityScoreBPS             int
	Strategy                    string
	ExpiresAt                   time.Time
	TaskFingerprint             []byte
	StableTaskCount             int
	StableConfidenceBPS         int
	ModelLocked                 bool
	LockReason                  string
	ConversationTokens          int
	CacheReadTokens             int
	ClassificationConfidenceBPS int
	ClassificationReliable      bool
	HighestModel                bool
	RuntimeRevision             int64
	PolicyVersionID             int64
	ModelCatalogRevision        int64
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
	purpose string,
) (SessionKey, bool) {
	var empty SessionKey
	if s == nil || profileID <= 0 || purpose == "" ||
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
	return s.get(ctx, key, 0, 0, 0)
}

func (s *SessionStore) GetForRuntime(
	ctx context.Context,
	key SessionKey,
	runtimeRevision, policyVersionID, modelCatalogRevision int64,
) (SessionBinding, bool, error) {
	return s.get(ctx, key, runtimeRevision, policyVersionID, modelCatalogRevision)
}

func (s *SessionStore) get(
	ctx context.Context,
	key SessionKey,
	runtimeRevision, policyVersionID, modelCatalogRevision int64,
) (SessionBinding, bool, error) {
	if s == nil {
		return SessionBinding{}, false, nil
	}
	binding, err := scanSessionBinding(s.db.QueryRowContext(ctx, `
		SELECT profile_id, route_id, purpose, task_type, difficulty, model, quality_score_bps,
		       strategy_name, expires_at, task_fingerprint, stable_task_count,
		       stable_confidence_bps, model_locked, lock_reason, last_input_tokens,
		       runtime_revision, policy_version_id, model_catalog_revision
		FROM routing_session_bindings
		WHERE key_hash = ?
	`, key[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return SessionBinding{}, false, nil
	}
	if err != nil {
		return SessionBinding{}, false, fmt.Errorf("read routing Session binding: %w", err)
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
	runtimeChanged := runtimeRevision > 0 && (binding.RuntimeRevision != runtimeRevision ||
		binding.PolicyVersionID != policyVersionID ||
		binding.ModelCatalogRevision != modelCatalogRevision)
	retainLockedModel := binding.ModelLocked &&
		binding.ModelCatalogRevision == modelCatalogRevision
	if runtimeChanged && !retainLockedModel {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM routing_session_bindings WHERE key_hash = ?`, key[:]); err != nil {
			return SessionBinding{}, false, fmt.Errorf("delete stale routing Session binding: %w", err)
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
	return s.BindWithLockPolicy(ctx, key, binding, ttl, SessionLockPolicy{
		TokenThreshold: profile.DefaultSessionLockTokenThreshold,
	})
}

func (s *SessionStore) BindWithLockPolicy(
	ctx context.Context,
	key SessionKey,
	binding SessionBinding,
	ttl time.Duration,
	lockPolicy SessionLockPolicy,
) error {
	if s == nil {
		return nil
	}
	if binding.Difficulty == "" {
		binding.Difficulty = DifficultyUnknown
	}
	if binding.ProfileID <= 0 || binding.Route == "" || binding.Purpose == "" ||
		binding.TaskType == "" || !validSessionDifficulty(binding.Difficulty) ||
		binding.Model == "" || binding.Strategy == "" ||
		binding.QualityScoreBPS < 0 || binding.QualityScoreBPS > 10_000 ||
		binding.ClassificationConfidenceBPS < 0 || binding.ClassificationConfidenceBPS > 10_000 ||
		binding.ConversationTokens < 0 || binding.CacheReadTokens < 0 ||
		lockPolicy.TokenThreshold <= 0 ||
		!validRuntimeIdentity(binding) ||
		len(binding.TaskFingerprint) != 0 && len(binding.TaskFingerprint) != sha256.Size || ttl <= 0 {
		return errors.New("routing Session binding is invalid")
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routing Session binding update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM routing_session_bindings WHERE expires_at <= ?`,
		formatSessionTime(now),
	); err != nil {
		return fmt.Errorf("clean expired routing Session bindings: %w", err)
	}
	existing, getErr := scanSessionBinding(tx.QueryRowContext(ctx, `
		SELECT profile_id, route_id, purpose, task_type, difficulty, model, quality_score_bps,
		       strategy_name, expires_at, task_fingerprint, stable_task_count,
		       stable_confidence_bps, model_locked, lock_reason, last_input_tokens,
		       runtime_revision, policy_version_id, model_catalog_revision
		FROM routing_session_bindings
		WHERE key_hash = ?
	`, key[:]))
	if getErr != nil && !errors.Is(getErr, sql.ErrNoRows) {
		return fmt.Errorf("read routing Session binding for update: %w", getErr)
	}
	binding = nextSessionBinding(existing, getErr == nil, binding, lockPolicy)
	locked := 0
	if binding.ModelLocked {
		locked = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO routing_session_bindings (
			key_hash, profile_id, route_id, purpose, task_type, difficulty, model, quality_score_bps,
			strategy_name, created_at, updated_at, expires_at, task_fingerprint,
			stable_task_count, stable_confidence_bps, model_locked, lock_reason, last_input_tokens,
			runtime_revision, policy_version_id, model_catalog_revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key_hash) DO UPDATE SET
			profile_id = excluded.profile_id,
			route_id = excluded.route_id,
			purpose = excluded.purpose,
			task_type = excluded.task_type,
			difficulty = excluded.difficulty,
			model = excluded.model,
			quality_score_bps = excluded.quality_score_bps,
			strategy_name = excluded.strategy_name,
			updated_at = excluded.updated_at,
			expires_at = excluded.expires_at,
			task_fingerprint = excluded.task_fingerprint,
			stable_task_count = excluded.stable_task_count,
			stable_confidence_bps = excluded.stable_confidence_bps,
			model_locked = excluded.model_locked,
			lock_reason = excluded.lock_reason,
			last_input_tokens = excluded.last_input_tokens,
			runtime_revision = excluded.runtime_revision,
			policy_version_id = excluded.policy_version_id,
			model_catalog_revision = excluded.model_catalog_revision
	`,
		key[:],
		binding.ProfileID,
		binding.Route,
		binding.Purpose,
		binding.TaskType,
		binding.Difficulty,
		binding.Model,
		binding.QualityScoreBPS,
		binding.Strategy,
		formatSessionTime(now),
		formatSessionTime(now),
		formatSessionTime(now.Add(ttl)),
		nullableFingerprint(binding.TaskFingerprint),
		binding.StableTaskCount,
		binding.StableConfidenceBPS,
		locked,
		binding.LockReason,
		binding.ConversationTokens,
		binding.RuntimeRevision,
		binding.PolicyVersionID,
		binding.ModelCatalogRevision,
	)
	if err != nil {
		return fmt.Errorf("save routing Session binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routing Session binding: %w", err)
	}
	return nil
}

func nextSessionBinding(
	existing SessionBinding,
	found bool,
	incoming SessionBinding,
	lockPolicy SessionLockPolicy,
) SessionBinding {
	incoming.TaskFingerprint = append([]byte(nil), incoming.TaskFingerprint...)
	runtimeChanged := found && (existing.RuntimeRevision != incoming.RuntimeRevision ||
		existing.PolicyVersionID != incoming.PolicyVersionID ||
		existing.ModelCatalogRevision != incoming.ModelCatalogRevision)
	if runtimeChanged && existing.ModelLocked && existing.Strategy == incoming.Strategy &&
		existing.ModelCatalogRevision == incoming.ModelCatalogRevision &&
		existing.Model == incoming.Model {
		incoming.StableTaskCount = existing.StableTaskCount
		incoming.StableConfidenceBPS = existing.StableConfidenceBPS
		incoming.ModelLocked = true
		incoming.LockReason = existing.LockReason
		return incoming
	}
	if !found || existing.Strategy != incoming.Strategy ||
		existing.RuntimeRevision != incoming.RuntimeRevision ||
		existing.PolicyVersionID != incoming.PolicyVersionID ||
		existing.ModelCatalogRevision != incoming.ModelCatalogRevision {
		if len(incoming.TaskFingerprint) == sha256.Size && stableSessionClassification(incoming) {
			incoming.StableTaskCount = 1
			incoming.StableConfidenceBPS = incoming.ClassificationConfidenceBPS
		}
		return lockPolicy.Apply(incoming)
	}
	sameTask := len(existing.TaskFingerprint) == sha256.Size &&
		len(incoming.TaskFingerprint) == sha256.Size &&
		bytes.Equal(existing.TaskFingerprint, incoming.TaskFingerprint)
	legacyUpdate := len(existing.TaskFingerprint) == 0 && len(incoming.TaskFingerprint) == 0
	if existing.ModelLocked && existing.LockReason == "highest_model" &&
		!hasUsableSessionClassification(existing) && stableSessionClassification(incoming) {
		incoming.StableTaskCount = 1
		incoming.StableConfidenceBPS = incoming.ClassificationConfidenceBPS
		return lockPolicy.Apply(incoming)
	}
	if existing.ModelLocked {
		if incoming.HighestModel && incoming.QualityScoreBPS >= existing.QualityScoreBPS {
			incoming.StableTaskCount = max(1, existing.StableTaskCount)
			incoming.StableConfidenceBPS = incoming.ClassificationConfidenceBPS
			return lockPolicy.Apply(incoming)
		}
		incoming.Model = existing.Model
		incoming.QualityScoreBPS = existing.QualityScoreBPS
		incoming.TaskType = existing.TaskType
		incoming.Difficulty = existing.Difficulty
		incoming.Route = existing.Route
		incoming.StableTaskCount = existing.StableTaskCount
		incoming.StableConfidenceBPS = existing.StableConfidenceBPS
		incoming.ModelLocked = true
		incoming.LockReason = existing.LockReason
		return incoming
	}
	if incoming.HighestModel {
		incoming.StableTaskCount = max(1, existing.StableTaskCount)
		incoming.StableConfidenceBPS = incoming.ClassificationConfidenceBPS
		return lockPolicy.Apply(incoming)
	}
	if sameTask || legacyUpdate {
		incoming.StableTaskCount = existing.StableTaskCount
		incoming.StableConfidenceBPS = existing.StableConfidenceBPS
		if sameTask && !hasUsableSessionClassification(incoming) && hasUsableSessionClassification(existing) {
			incoming.TaskType = existing.TaskType
			incoming.Difficulty = existing.Difficulty
			incoming.Route = existing.Route
		}
		if incoming.QualityScoreBPS < existing.QualityScoreBPS {
			incoming.Model = existing.Model
			incoming.QualityScoreBPS = existing.QualityScoreBPS
			incoming.TaskType = existing.TaskType
			incoming.Difficulty = existing.Difficulty
			incoming.Route = existing.Route
		}
		return incoming
	}
	if !stableSessionClassification(incoming) {
		incoming.StableTaskCount = 0
		incoming.StableConfidenceBPS = 0
		return incoming
	}
	if incoming.Model == existing.Model {
		incoming.StableTaskCount = existing.StableTaskCount + 1
		incoming.StableConfidenceBPS = min(existing.StableConfidenceBPS, incoming.ClassificationConfidenceBPS)
	} else {
		incoming.StableTaskCount = 1
		incoming.StableConfidenceBPS = incoming.ClassificationConfidenceBPS
	}
	return lockPolicy.Apply(incoming)
}

func hasUsableSessionClassification(binding SessionBinding) bool {
	return binding.TaskType != "" && binding.TaskType != "unknown" &&
		binding.Difficulty != "" && binding.Difficulty != DifficultyUnknown
}

func nullableFingerprint(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

type sessionRowScanner interface {
	Scan(...any) error
}

func scanSessionBinding(scanner sessionRowScanner) (SessionBinding, error) {
	var binding SessionBinding
	var expiresAt string
	var locked int
	if err := scanner.Scan(
		&binding.ProfileID, &binding.Route, &binding.Purpose, &binding.TaskType,
		&binding.Difficulty, &binding.Model, &binding.QualityScoreBPS, &binding.Strategy,
		&expiresAt, &binding.TaskFingerprint, &binding.StableTaskCount,
		&binding.StableConfidenceBPS, &locked, &binding.LockReason,
		&binding.ConversationTokens,
		&binding.RuntimeRevision, &binding.PolicyVersionID, &binding.ModelCatalogRevision,
	); err != nil {
		return SessionBinding{}, err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return SessionBinding{}, fmt.Errorf("parse routing Session expiry: %w", err)
	}
	binding.ExpiresAt = expires
	binding.ModelLocked = locked == 1
	if binding.LockReason == "stable_session_model" {
		binding.ModelLocked = false
		binding.LockReason = ""
		binding.StableTaskCount = 0
		binding.StableConfidenceBPS = 0
		binding.ConversationTokens = 0
	}
	binding.TaskFingerprint = append([]byte(nil), binding.TaskFingerprint...)
	return binding, nil
}

func validRuntimeIdentity(binding SessionBinding) bool {
	allZero := binding.RuntimeRevision == 0 && binding.PolicyVersionID == 0 &&
		binding.ModelCatalogRevision == 0
	allPositive := binding.RuntimeRevision > 0 && binding.PolicyVersionID > 0 &&
		binding.ModelCatalogRevision > 0
	return allZero || allPositive
}

func validSessionDifficulty(value Difficulty) bool {
	switch value {
	case DifficultyUnknown, DifficultyEasy, DifficultyMedium, DifficultyHard:
		return true
	default:
		return false
	}
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
