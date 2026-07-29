package admin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const (
	SessionCookieName = "llm_proxy_session"
	CSRFCookieName    = "llm_proxy_csrf"

	authCookiePath   = "/_admin"
	authCookieMaxAge = 24 * 60 * 60
)

var (
	ErrRateLimited            = errors.New("login rate limited")
	ErrUnauthorized           = errors.New("unauthorized")
	ErrCSRF                   = errors.New("csrf validation failed")
	ErrPasswordChangeRequired = errors.New("password change required")
)

type Principal struct {
	AccountID          int64
	Username           string
	AuthVersion        int64
	MustChangePassword bool
}

type AuthService struct {
	accounts *AccountStore
	sessions *SessionStore
	limiter  *LoginLimiter
}

func NewAuthService(
	accounts *AccountStore,
	sessions *SessionStore,
	limiter *LoginLimiter,
) *AuthService {
	return &AuthService{
		accounts: accounts,
		sessions: sessions,
		limiter:  limiter,
	}
}

func (s *AuthService) Login(
	ctx context.Context,
	remoteAddr, username, password string,
) (Principal, Session, error) {
	address, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		address = ""
	}
	if !s.limiter.Allow(address) {
		return Principal{}, Session{}, ErrRateLimited
	}

	account, err := s.accounts.Authenticate(ctx, username, password)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			return Principal{}, Session{}, ErrInvalidCredentials
		}
		return Principal{}, Session{}, fmt.Errorf("authenticate admin account: %w", err)
	}
	if err := s.sessions.CleanupExpired(ctx); err != nil {
		return Principal{}, Session{}, fmt.Errorf("maintain admin sessions before login: %w", err)
	}
	session, err := s.sessions.Create(ctx, account.AuthVersion, sessionTTL)
	if err != nil {
		return Principal{}, Session{}, fmt.Errorf("create admin session: %w", err)
	}
	return principalFromAccount(account), session, nil
}

func (s *AuthService) Logout(ctx context.Context, token string) error {
	if err := s.sessions.Revoke(ctx, token); err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	return nil
}

func (s *AuthService) Authenticate(
	ctx context.Context,
	sessionToken, csrfToken string,
	requireCSRF bool,
) (Principal, error) {
	if sessionToken == "" {
		return Principal{}, ErrUnauthorized
	}
	account, err := s.accounts.Get(ctx)
	if err != nil {
		return Principal{}, fmt.Errorf("load admin account: %w", err)
	}
	if _, err := s.sessions.Authenticate(ctx, sessionToken, account.AuthVersion); err != nil {
		if errors.Is(err, ErrInvalidSession) {
			return Principal{}, ErrUnauthorized
		}
		return Principal{}, fmt.Errorf("authenticate admin session: %w", err)
	}
	if requireCSRF {
		if err := s.verifyCSRF(ctx, sessionToken, csrfToken); err != nil {
			return Principal{}, err
		}
	}
	return principalFromAccount(account), nil
}

func (s *AuthService) AuthenticateRequest(r *http.Request) (Principal, error) {
	sessionCookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	requireCSRF := requestRequiresCSRF(r.Method)
	principal, err := s.Authenticate(
		r.Context(),
		sessionCookie.Value,
		"",
		false,
	)
	if err != nil {
		return Principal{}, err
	}
	if requireCSRF {
		csrfCookie, err := r.Cookie(CSRFCookieName)
		if err != nil {
			return Principal{}, ErrCSRF
		}
		csrfHeader := r.Header.Get("X-CSRF-Token")
		if csrfCookie.Value == "" || csrfHeader == "" ||
			subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(csrfHeader)) != 1 {
			return Principal{}, ErrCSRF
		}
		if err := s.verifyCSRF(r.Context(), sessionCookie.Value, csrfCookie.Value); err != nil {
			return Principal{}, err
		}
	}
	if principal.MustChangePassword && !passwordChangeRouteAllowed(r.Method, r.URL.Path) {
		return Principal{}, ErrPasswordChangeRequired
	}
	return principal, nil
}

func (s *AuthService) verifyCSRF(ctx context.Context, sessionToken, csrfToken string) error {
	if err := s.sessions.VerifyCSRF(ctx, sessionToken, csrfToken); err != nil {
		switch {
		case errors.Is(err, ErrInvalidCSRF):
			return ErrCSRF
		case errors.Is(err, ErrInvalidSession):
			return ErrUnauthorized
		default:
			return fmt.Errorf("verify admin csrf token: %w", err)
		}
	}
	return nil
}

func (s *AuthService) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/_admin/api/login" {
			next.ServeHTTP(w, r)
			return
		}
		principal, err := s.AuthenticateRequest(r)
		if err != nil {
			writeAuthenticationError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func SetAuthCookies(w http.ResponseWriter, r *http.Request, session Session) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    session.Token,
		Path:     authCookiePath,
		MaxAge:   authCookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    session.CSRFToken,
		Path:     authCookiePath,
		MaxAge:   authCookieMaxAge,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func requestRequiresCSRF(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func principalFromAccount(account Account) Principal {
	return Principal{
		AccountID:          account.ID,
		Username:           account.Username,
		AuthVersion:        account.AuthVersion,
		MustChangePassword: account.MustChangePassword,
	}
}

func passwordChangeRouteAllowed(method, path string) bool {
	switch {
	case method == http.MethodGet && path == "/_admin/api/session":
		return true
	case method == http.MethodPost && path == "/_admin/api/password":
		return true
	case method == http.MethodPost && path == "/_admin/api/logout":
		return true
	default:
		return false
	}
}

func writeAuthenticationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, ErrCSRF), errors.Is(err, ErrPasswordChangeRequired):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
