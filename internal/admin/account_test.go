package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
)

// Break caught: failing to create the required singleton admin account or persist its initial-login state.
func TestAccountStoreEnsureDefaultCreatesSingletonAccount(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)

	account, created, err := store.EnsureDefault(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("default account was not created")
	}
	if account.ID != 1 || account.Username != "admin" || !account.MustChangePassword || account.AuthVersion != 1 {
		t.Fatalf("account=%+v", account)
	}
	if account.UpdatedAt.IsZero() {
		t.Fatal("updated timestamp is empty")
	}
	if _, err := store.Authenticate(context.Background(), "admin", "admin"); err != nil {
		t.Fatalf("default credentials rejected: %v", err)
	}
}

// Break caught: recreating or resetting an existing account whenever startup calls EnsureDefault.
func TestAccountStoreEnsureDefaultIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()

	first, created, err := store.EnsureDefault(ctx)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	second, created, err := store.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing account was recreated")
	}
	if second != first {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

// Break caught: accepting an unknown username or an incorrect password.
func TestAccountStoreAuthenticateRejectsInvalidCredentials(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()
	if _, _, err := store.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}

	for _, input := range []struct {
		name     string
		username string
		password string
	}{
		{name: "unknown username", username: "other", password: "admin"},
		{name: "wrong password", username: "admin", password: "wrong"},
	} {
		if _, err := store.Authenticate(ctx, input.username, input.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("%s error=%v, want ErrInvalidCredentials", input.name, err)
		}
	}
}

// Break caught: permitting weak replacement passwords or changing one without current-password proof.
func TestAccountStoreChangePasswordValidatesCredentialsAndStrength(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()
	account, _, err := store.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ChangePassword(ctx, account.ID, "wrong", "long-enough-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password error=%v, want ErrInvalidCredentials", err)
	}
	if err := store.ChangePassword(ctx, account.ID, "admin", "too-short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password error=%v, want ErrWeakPassword", err)
	}
	if _, err := store.Authenticate(ctx, "admin", "admin"); err != nil {
		t.Fatalf("password changed after rejected requests: %v", err)
	}
}

// Break caught: counting UTF-8 bytes lets fewer than 10 Unicode characters satisfy the password minimum.
func TestAccountStoreChangePasswordCountsUnicodeCharacters(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()
	account, _, err := store.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ChangePassword(ctx, account.ID, "admin", "密码密码"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("four-character password error=%v, want ErrWeakPassword", err)
	}
}

// Break caught: treating malformed UTF-8 bytes as password characters.
func TestAccountStoreChangePasswordRejectsInvalidUTF8(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()
	account, _, err := store.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invalidPassword := string([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	if err := store.ChangePassword(ctx, account.ID, "admin", invalidPassword); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("invalid UTF-8 password error=%v, want ErrWeakPassword", err)
	}
}

// Break caught: changing a password without invalidating sessions or updating account authentication state.
func TestAccountStoreChangePasswordInvalidatesSessionsAndUpdatesAccount(t *testing.T) {
	db := openTestDB(t)
	store := NewAccountStore(db)
	ctx := context.Background()
	account, _, err := store.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_sessions (token_hash, csrf_hash, auth_version, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`, []byte("token"), []byte("csrf"), account.AuthVersion, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	if err := store.ChangePassword(ctx, account.ID, "admin", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MustChangePassword || updated.AuthVersion != account.AuthVersion+1 {
		t.Fatalf("updated=%+v", updated)
	}
	if _, err := store.Authenticate(ctx, "admin", "admin"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password error=%v, want ErrInvalidCredentials", err)
	}
	if _, err := store.Authenticate(ctx, "admin", "long-enough-password"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	var sessions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions=%d, want 0", sessions)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
