package admin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// Break caught: persisting browser session or CSRF plaintext instead of one-way hashes.
func TestSessionStoreKeepsOnlyTokenHashes(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(db, func() time.Time { return now })

	session, err := store.Create(context.Background(), 3, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var tokenHash, csrfHash []byte
	if err := db.QueryRow(`SELECT token_hash, csrf_hash FROM admin_sessions`).Scan(&tokenHash, &csrfHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tokenHash, []byte(session.Token)) || bytes.Contains(csrfHash, []byte(session.CSRFToken)) {
		t.Fatal("plaintext token persisted")
	}

	got, err := store.Authenticate(context.Background(), session.Token, 3)
	if err != nil || got.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("session expiry=%v err=%v", got.ExpiresAt, err)
	}
	if err := store.VerifyCSRF(context.Background(), session.Token, session.CSRFToken); err != nil {
		t.Fatal(err)
	}
}

// Break caught: accepting an unrecognised session token.
func TestSessionStoreAuthenticateRejectsWrongToken(t *testing.T) {
	store := NewSessionStore(openTestDB(t), time.Now)

	if _, err := store.Authenticate(context.Background(), "wrong-token", 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("error=%v, want ErrInvalidSession", err)
	}
}

// Break caught: returning raw Session lookup failures without operation context or obscuring sql.ErrNoRows.
func TestSessionStoreFindWrapsDatabaseFailureAndPreservesNoRows(t *testing.T) {
	store := NewSessionStore(openTestDB(t), time.Now)
	_, _, _, _, err := store.find(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing Session preserved sql.ErrNoRows: false")
	}

	db := openTestDB(t)
	failingStore := NewSessionStore(db, time.Now)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = failingStore.find(context.Background(), "session-token")
	if err == nil || !strings.Contains(err.Error(), "find admin session") {
		t.Fatal("Session lookup failure included operation context: false")
	}
}

// Break caught: allowing a CSRF token not bound to the authenticated session.
func TestSessionStoreVerifyCSRFRejectsWrongToken(t *testing.T) {
	store := NewSessionStore(openTestDB(t), time.Now)
	session, err := store.Create(context.Background(), 1, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.VerifyCSRF(context.Background(), session.Token, "wrong-token"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("error=%v, want ErrInvalidCSRF", err)
	}
}

// Break caught: retaining a session created before an account password change.
func TestSessionStoreAuthenticateRejectsAuthVersionMismatch(t *testing.T) {
	store := NewSessionStore(openTestDB(t), time.Now)
	session, err := store.Create(context.Background(), 1, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Authenticate(context.Background(), session.Token, 2); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("error=%v, want ErrInvalidSession", err)
	}
}

// Break caught: accepting a session exactly at its fixed expiration time.
func TestSessionStoreAuthenticateRejectsExpirationBoundary(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(openTestDB(t), func() time.Time { return now })
	session, err := store.Create(context.Background(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)

	if _, err := store.Authenticate(context.Background(), session.Token, 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("error=%v, want ErrInvalidSession", err)
	}
}

// Break caught: leaving a logged-out session usable.
func TestSessionStoreRevokeInvalidatesSession(t *testing.T) {
	store := NewSessionStore(openTestDB(t), time.Now)
	session, err := store.Create(context.Background(), 1, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), session.Token); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Authenticate(context.Background(), session.Token, 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("error=%v, want ErrInvalidSession", err)
	}
}

// Break caught: keeping expired sessions after routine maintenance.
func TestSessionStoreCleanupExpiredDeletesOnlyExpiredSessions(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(openTestDB(t), func() time.Time { return now })
	expired, err := store.Create(context.Background(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.Create(context.Background(), 1, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)

	if err := store.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(context.Background(), expired.Token, 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error=%v, want ErrInvalidSession", err)
	}
	if _, err := store.Authenticate(context.Background(), active.Token, 1); err != nil {
		t.Fatalf("active session rejected: %v", err)
	}
}

// Break caught: comparing variable-width RFC3339Nano text can reverse fractional-second ordering.
func TestSessionStoreCleanupExpiredHandlesFractionalSecondBoundaries(t *testing.T) {
	base := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		createdAt time.Time
		ttl       time.Duration
		cleanupAt time.Time
		wantRows  int
	}{
		{
			name:      "immediately before expiry",
			createdAt: base,
			ttl:       100 * time.Millisecond,
			cleanupAt: base,
			wantRows:  1,
		},
		{
			name:      "at expiry",
			createdAt: base,
			ttl:       100 * time.Millisecond,
			cleanupAt: base.Add(100 * time.Millisecond),
			wantRows:  0,
		},
		{
			name:      "immediately after expiry",
			createdAt: base.Add(-100 * time.Millisecond),
			ttl:       100 * time.Millisecond,
			cleanupAt: base.Add(100 * time.Millisecond),
			wantRows:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			now := tt.createdAt
			store := NewSessionStore(db, func() time.Time { return now })
			if _, err := store.Create(context.Background(), 1, tt.ttl); err != nil {
				t.Fatal(err)
			}
			now = tt.cleanupAt

			if err := store.CleanupExpired(context.Background()); err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != tt.wantRows {
				t.Fatalf("remaining sessions=%d, want %d", rows, tt.wantRows)
			}
		})
	}
}
