package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Break caught: issuing browser credentials without the required cookie isolation, scope, or lifetime.
func TestAuthLoginCookiesProtectSessionAndExposeCSRF(t *testing.T) {
	service := newAuthServiceFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
		strings.NewReader(`{"username":"admin","password":"admin"}`))
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Content-Type", "application/json")

	principal, session, err := service.Login(request.Context(), request.RemoteAddr, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if principal.Username != "admin" || !principal.MustChangePassword {
		t.Fatalf("principal=%+v", principal)
	}
	response := httptest.NewRecorder()
	SetAuthCookies(response, request, session)
	cookies := response.Result().Cookies()

	sessionCookie := cookieByName(t, cookies, SessionCookieName)
	if !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie HttpOnly=%v SameSite=%v", sessionCookie.HttpOnly, sessionCookie.SameSite)
	}
	csrfCookie := cookieByName(t, cookies, CSRFCookieName)
	if csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("csrf cookie HttpOnly=%v SameSite=%v", csrfCookie.HttpOnly, csrfCookie.SameSite)
	}
	for _, cookie := range []*http.Cookie{sessionCookie, csrfCookie} {
		if cookie.Path != "/_admin" || cookie.MaxAge != 24*60*60 {
			t.Fatalf("%s Path=%q MaxAge=%d", cookie.Name, cookie.Path, cookie.MaxAge)
		}
	}
	if sessionCookie.Value != session.Token || csrfCookie.Value != session.CSRFToken {
		t.Fatal("cookie values do not contain the issued credentials")
	}
}

// Break caught: sending either credential cookie over plaintext when TLS terminates locally or at the trusted transport proxy.
func TestAuthCookiesAreSecureForHTTPSDelivery(t *testing.T) {
	session := Session{Token: "session-token", CSRFToken: "csrf-token"}
	tests := []struct {
		name    string
		request *http.Request
	}{
		{
			name:    "local TLS",
			request: httptest.NewRequest(http.MethodPost, "https://example.com/_admin/api/login", nil),
		},
		{
			name: "forwarded HTTPS",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodPost, "http://example.com/_admin/api/login", nil)
				request.Header.Set("X-Forwarded-Proto", "https")
				return request
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			SetAuthCookies(response, tt.request, session)
			for _, cookie := range response.Result().Cookies() {
				if !cookie.Secure {
					t.Fatalf("%s cookie is not Secure", cookie.Name)
				}
			}
		})
	}
}

// Break caught: requiring CSRF for a read or allowing a protected read without an authenticated Session.
func TestAuthMiddlewareProtectedGETRequiresOnlySession(t *testing.T) {
	service := newAuthServiceFixture(t)
	_, session, err := service.Login(context.Background(), "192.0.2.10:1234", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.Username != "admin" {
			t.Fatalf("principal=%+v ok=%v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	unauthenticated := httptest.NewRequest(http.MethodGet, "/_admin/api/session", nil)
	unauthenticated.Header.Set("X-Forwarded-Proto", "https")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want %d", unauthenticatedResponse.Code, http.StatusUnauthorized)
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/_admin/api/session", nil)
	authenticated.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticated)
	if authenticatedResponse.Code != http.StatusNoContent {
		t.Fatalf("authenticated status=%d, want %d", authenticatedResponse.Code, http.StatusNoContent)
	}
}

// Break caught: accepting an authenticated state-changing request without the Session-bound CSRF header.
func TestAuthMiddlewareStateChangingMethodsRequireCSRF(t *testing.T) {
	service := newChangedPasswordAuthServiceFixture(t)
	_, session, err := service.Login(context.Background(), "192.0.2.10:1234", "admin", "long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			missing := httptest.NewRequest(method, "/_admin/api/password", nil)
			missing.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
			missingResponse := httptest.NewRecorder()
			handler.ServeHTTP(missingResponse, missing)
			if missingResponse.Code != http.StatusForbidden {
				t.Fatalf("missing CSRF status=%d, want %d", missingResponse.Code, http.StatusForbidden)
			}

			valid := httptest.NewRequest(method, "/_admin/api/password", nil)
			valid.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
			valid.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: session.CSRFToken})
			valid.Header.Set("X-CSRF-Token", session.CSRFToken)
			validResponse := httptest.NewRecorder()
			handler.ServeHTTP(validResponse, valid)
			if validResponse.Code != http.StatusNoContent {
				t.Fatalf("valid CSRF status=%d, want %d", validResponse.Code, http.StatusNoContent)
			}
		})
	}
}

// Break caught: accepting a Session whose stored authentication version predates the current account password.
func TestAuthAuthenticateRejectsSessionCreatedBeforePasswordChange(t *testing.T) {
	db := openTestDB(t)
	accounts := NewAccountStore(db)
	ctx := context.Background()
	account, _, err := accounts.EnsureDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(db, time.Now)
	service := NewAuthService(accounts, sessions, NewLoginLimiter(time.Now))
	session, err := sessions.Create(ctx, account.AuthVersion, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(ctx, account.ID, "admin", "long-enough-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Authenticate(ctx, session.Token, "", false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error=%v, want ErrUnauthorized", err)
	}
}

// Break caught: allowing an initial-password Session to access management endpoints beyond status, password change, and logout.
func TestAuthMustChangeSessionHasNarrowRouteAccess(t *testing.T) {
	service := newAuthServiceFixture(t)
	_, session, err := service.Login(context.Background(), "192.0.2.10:1234", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}

	allowed := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/_admin/api/session"},
		{method: http.MethodPost, path: "/_admin/api/password"},
		{method: http.MethodPost, path: "/_admin/api/logout"},
	}
	for _, tt := range allowed {
		request := httptest.NewRequest(tt.method, tt.path, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
		if requestRequiresCSRF(tt.method) {
			request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: session.CSRFToken})
			request.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		if _, err := service.AuthenticateRequest(request); err != nil {
			t.Fatalf("%s %s error=%v", tt.method, tt.path, err)
		}
	}

	restricted := httptest.NewRequest(http.MethodGet, "/_admin/api/profiles", nil)
	restricted.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
	if _, err := service.AuthenticateRequest(restricted); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("restricted error=%v, want ErrPasswordChangeRequired", err)
	}
}

// Break caught: bypassing the per-address limit by changing the source port.
func TestAuthLoginLimitsRemoteAddressHost(t *testing.T) {
	service := newAuthServiceFixture(t)
	for i := 0; i < 5; i++ {
		_, _, err := service.Login(context.Background(), "192.0.2.10:"+string(rune('1'+i)), "unknown", "wrong")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error=%v, want ErrInvalidCredentials", i+1, err)
		}
	}

	if _, _, err := service.Login(context.Background(), "192.0.2.10:9", "unknown", "wrong"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("sixth error=%v, want ErrRateLimited", err)
	}
}

// Break caught: creating replacement Sessions without pruning expired rows after successful credential verification.
func TestAuthLoginCleansExpiredSessionsBeforeCreatingSession(t *testing.T) {
	db := openTestDB(t)
	accounts := NewAccountStore(db)
	if _, _, err := accounts.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	sessions := NewSessionStore(db, func() time.Time { return now })
	if _, err := sessions.Create(context.Background(), 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	service := NewAuthService(accounts, sessions, NewLoginLimiter(func() time.Time { return now }))

	if _, _, err := service.Login(
		context.Background(),
		"192.0.2.10:1234",
		"admin",
		"admin",
	); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("session row count=%d, want 1", count)
	}
}

// Break caught: issuing a new Session after expired-Session maintenance has failed.
func TestAuthLoginFailsBeforeSessionCreationWhenCleanupFails(t *testing.T) {
	db := openTestDB(t)
	accounts := NewAccountStore(db)
	if _, _, err := accounts.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	sessions := NewSessionStore(db, func() time.Time { return now })
	if _, err := sessions.Create(context.Background(), 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := db.Exec(`
		CREATE TRIGGER reject_expired_session_cleanup
		BEFORE DELETE ON admin_sessions
		BEGIN
			SELECT RAISE(ABORT, 'cleanup failed');
		END
	`); err != nil {
		t.Fatal(err)
	}
	service := NewAuthService(accounts, sessions, NewLoginLimiter(func() time.Time { return now }))

	_, _, err := service.Login(
		context.Background(),
		"192.0.2.10:1234",
		"admin",
		"admin",
	)
	if err == nil || !strings.Contains(err.Error(), "clean up expired admin sessions") {
		t.Fatal("cleanup failure returned from login: false")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("session row count=%d, want 1", count)
	}
}

// Break caught: leaving a logged-out Session authenticated.
func TestAuthLogoutRevokesSession(t *testing.T) {
	service := newAuthServiceFixture(t)
	_, session, err := service.Login(context.Background(), "192.0.2.10:1234", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(context.Background(), session.Token); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Authenticate(context.Background(), session.Token, "", false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error=%v, want ErrUnauthorized", err)
	}
}

func newAuthServiceFixture(t *testing.T) *AuthService {
	t.Helper()
	db := openTestDB(t)
	accounts := NewAccountStore(db)
	if _, _, err := accounts.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewAuthService(
		accounts,
		NewSessionStore(db, time.Now),
		NewLoginLimiter(time.Now),
	)
}

func newChangedPasswordAuthServiceFixture(t *testing.T) *AuthService {
	t.Helper()
	db := openTestDB(t)
	accounts := NewAccountStore(db)
	account, _, err := accounts.EnsureDefault(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(context.Background(), account.ID, "admin", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	return NewAuthService(
		accounts,
		NewSessionStore(db, time.Now),
		NewLoginLimiter(time.Now),
	)
}

func cookieByName(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}
