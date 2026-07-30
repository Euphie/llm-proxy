package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/stats"
)

const initialCredentialWarning = "High risk: the default admin/admin credentials are active. Change the password immediately."

type apiTestFixture struct {
	handler http.Handler
	db      *sql.DB
	dataDir string
	logs    *bytes.Buffer
	now     time.Time
}

// Break caught: hiding the bootstrap credential risk or allowing Profile access before the mandatory password change.
func TestLoginResponseAndProfileGate(t *testing.T) {
	fixture := newTestAPI(t)
	login := fixture.request(t, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}

	var body sessionResponse
	decodeTestJSON(t, login, &body)
	if body.Username != "admin" || !body.MustChangePassword || body.Initialized ||
		!body.HighRiskDefaultCredentials ||
		body.InitializationState != "password_change_required" ||
		body.Warning != initialCredentialWarning {
		t.Fatalf("login body=%+v", body)
	}
	assertResponseDoesNotContain(t, login.Body.String(),
		"session_token", "csrf_token", "password_hash", `"password":"admin"`)

	cookies := login.Result().Cookies()
	session := cookieByName(t, cookies, SessionCookieName)
	csrf := cookieByName(t, cookies, CSRFCookieName)

	refresh := fixture.request(t, http.MethodGet, "/_admin/api/session", "", cookies, "")
	if refresh.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var refreshed sessionResponse
	decodeTestJSON(t, refresh, &refreshed)
	if refreshed != body {
		t.Fatalf("session=%+v login=%+v", refreshed, body)
	}

	profiles := fixture.request(t, http.MethodGet, "/_admin/api/profiles", "", cookies, "")
	assertAPIError(t, profiles, http.StatusForbidden, "password_change_required")

	logout := fixture.request(t, http.MethodPost, "/_admin/api/logout", `{}`, cookies, csrf.Value)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	cleared := logout.Result().Cookies()
	if cookieByName(t, cleared, SessionCookieName).MaxAge >= 0 ||
		cookieByName(t, cleared, CSRFCookieName).MaxAge >= 0 {
		t.Fatalf("logout cookies=%+v", cleared)
	}
	afterLogout := fixture.request(t, http.MethodGet, "/_admin/api/session", "",
		[]*http.Cookie{session}, "")
	assertAPIError(t, afterLogout, http.StatusUnauthorized, "unauthorized")
}

// Break caught: accepting malformed, oversized, non-JSON, or secret-bearing request models.
func TestAPIRejectsInvalidJSONAndUnknownFields(t *testing.T) {
	fixture := newTestAPI(t)
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{
			name:        "malformed",
			body:        `{"username":`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "unknown api key",
			body:        `{"username":"admin","password":"admin","api_key":"secret"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "trailing value",
			body:        `{"username":"admin","password":"admin"} {}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "null",
			body:        `null`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "array",
			body:        `[]`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "scalar",
			body:        `"credentials"`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "wrong content type",
			body:        `{"username":"admin","password":"admin"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "unsupported_media_type",
		},
		{
			name:        "oversized",
			body:        `{"username":"` + strings.Repeat("x", 1<<20) + `","password":"admin"}`,
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "request_too_large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/_admin/api/login", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", tt.contentType)
			request.RemoteAddr = "192.0.2.25:1234"
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, request)
			assertAPIError(t, response, tt.wantStatus, tt.wantCode)
		})
	}

	cookies, csrf := fixture.changePassword(t)
	secretFields := []string{"api_key", "Authorization"}
	for _, field := range secretFields {
		body := fmt.Sprintf(
			`{"slug":"coding","display_name":"Coding","enabled":true,"config":%s,"make_default":true,%q:"secret"}`,
			profileConfigJSON("anthropic", "https://coding.example"),
			field,
		)
		response := fixture.request(t, http.MethodPost, "/_admin/api/profiles", body, cookies, csrf)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_json")
	}
}

// Break caught: allowing a top-level null to reach logout, password, or Profile side effects.
func TestAPINullWriteBodiesAreRejectedBeforeSideEffects(t *testing.T) {
	t.Run("logout keeps session", func(t *testing.T) {
		fixture := newTestAPI(t)
		login := fixture.login(t, "admin")
		cookies := login.Result().Cookies()
		csrf := cookieByName(t, cookies, CSRFCookieName).Value

		response := fixture.request(t, http.MethodPost, "/_admin/api/logout", `null`, cookies, csrf)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_json")

		status := fixture.request(t, http.MethodGet, "/_admin/api/session", "", cookies, "")
		if status.Code != http.StatusOK {
			t.Fatalf("session revoked after rejected null logout: status=%d body=%s",
				status.Code, status.Body.String())
		}
	})

	t.Run("password remains unchanged", func(t *testing.T) {
		fixture := newTestAPI(t)
		login := fixture.login(t, "admin")
		cookies := login.Result().Cookies()
		csrf := cookieByName(t, cookies, CSRFCookieName).Value

		response := fixture.request(t, http.MethodPost, "/_admin/api/password", `null`, cookies, csrf)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_json")

		if _, err := NewAccountStore(fixture.db).Authenticate(
			context.Background(),
			"admin",
			"admin",
		); err != nil {
			t.Fatalf("password changed after rejected null body: %v", err)
		}
	})

	t.Run("profile is not created", func(t *testing.T) {
		fixture := newTestAPI(t)
		cookies, csrf := fixture.changePassword(t)

		response := fixture.request(t, http.MethodPost, "/_admin/api/profiles", `null`, cookies, csrf)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_json")

		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("profiles=%d after rejected null body, want 0", count)
		}
	})
}

// Break caught: treating non-JSON Unicode whitespace as legal while rejecting RFC 8259 whitespace.
func TestAPIJSONWhitespaceIsRFC8259Strict(t *testing.T) {
	t.Run("legal whitespace", func(t *testing.T) {
		fixture := newTestAPI(t)
		response := fixture.request(
			t,
			http.MethodPost,
			"/_admin/api/login",
			" \t\n\r"+`{"username":"admin","password":"admin"}`+"\r\n\t ",
			nil,
			"",
		)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	nbsp := string([]byte{0xc2, 0xa0})
	tests := []struct {
		name string
		body string
	}{
		{name: "leading raw NBSP", body: nbsp + `{}`},
		{name: "trailing raw NBSP", body: `{}` + nbsp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTestAPI(t)
			login := fixture.login(t, "admin")
			cookies := login.Result().Cookies()
			csrf := cookieByName(t, cookies, CSRFCookieName).Value

			response := fixture.request(t, http.MethodPost, "/_admin/api/logout", tt.body, cookies, csrf)
			if response.Code != http.StatusBadRequest {
				t.Errorf("status=%d body=%s, want 400", response.Code, response.Body.String())
			} else {
				assertAPIError(t, response, http.StatusBadRequest, "invalid_json")
			}

			status := fixture.request(t, http.MethodGet, "/_admin/api/session", "", cookies, "")
			if status.Code != http.StatusOK {
				t.Errorf("session changed after invalid JSON: status=%d body=%s",
					status.Code, status.Body.String())
			}
		})
	}
}

// Break caught: losing the browser session on password rotation or leaving a pre-change Session usable.
func TestAPIPasswordChangeRotatesSessionAndClearsBootstrapState(t *testing.T) {
	fixture := newTestAPI(t)
	login := fixture.login(t, "admin")
	oldCookies := login.Result().Cookies()
	oldSession := cookieByName(t, oldCookies, SessionCookieName)
	oldCSRF := cookieByName(t, oldCookies, CSRFCookieName)

	withoutCSRF := fixture.request(t, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"long-enough-password"}`,
		oldCookies, "")
	assertAPIError(t, withoutCSRF, http.StatusForbidden, "csrf_failed")

	changed := fixture.request(t, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"long-enough-password"}`,
		oldCookies, oldCSRF.Value)
	if changed.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", changed.Code, changed.Body.String())
	}
	var body sessionResponse
	decodeTestJSON(t, changed, &body)
	if body.Username != "admin" || body.MustChangePassword || body.Initialized ||
		body.HighRiskDefaultCredentials || body.InitializationState != "profile_setup_required" ||
		body.Warning != "" {
		t.Fatalf("password body=%+v", body)
	}

	newCookies := changed.Result().Cookies()
	newSession := cookieByName(t, newCookies, SessionCookieName)
	newCSRF := cookieByName(t, newCookies, CSRFCookieName)
	if newSession.Value == oldSession.Value || newCSRF.Value == oldCSRF.Value {
		t.Fatal("password change did not rotate both browser credentials")
	}

	oldStatus := fixture.request(t, http.MethodGet, "/_admin/api/session", "",
		[]*http.Cookie{oldSession}, "")
	assertAPIError(t, oldStatus, http.StatusUnauthorized, "unauthorized")
	newStatus := fixture.request(t, http.MethodGet, "/_admin/api/session", "", newCookies, "")
	if newStatus.Code != http.StatusOK {
		t.Fatalf("new session status=%d body=%s", newStatus.Code, newStatus.Body.String())
	}
	var refreshed sessionResponse
	decodeTestJSON(t, newStatus, &refreshed)
	if refreshed != body {
		t.Fatalf("refreshed=%+v password=%+v", refreshed, body)
	}

	oldLogin := fixture.request(t, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	assertAPIError(t, oldLogin, http.StatusUnauthorized, "invalid_credentials")
}

// Break caught: creating a replacement Session or implying rollback after Profile activation fails following a committed password change.
func TestAPIPasswordActivationFailureReportsCommittedChangeWithoutSession(t *testing.T) {
	activationErr := errors.New("activation failed")
	var fixture *apiTestFixture
	var activationObserved bool
	fixture = newTestAPIWithActivation(t, func(ctx context.Context) error {
		activationObserved = true
		account, err := NewAccountStore(fixture.db).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if account.MustChangePassword {
			t.Fatal("activation ran before the password transaction committed")
		}
		var sessions int
		if err := fixture.db.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM admin_sessions`,
		).Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if sessions != 0 {
			t.Fatalf("activation saw %d Sessions, want all old Sessions revoked", sessions)
		}
		return activationErr
	})

	login := fixture.login(t, "admin")
	oldCookies := login.Result().Cookies()
	oldCSRF := cookieByName(t, oldCookies, CSRFCookieName)
	changed := fixture.request(t, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"long-enough-password"}`,
		oldCookies, oldCSRF.Value)
	assertAPIError(t, changed, http.StatusServiceUnavailable, "runtime_sync_failed")
	if !activationObserved {
		t.Fatal("Profile activation hook was not called")
	}

	cleared := changed.Result().Cookies()
	if cookieByName(t, cleared, SessionCookieName).MaxAge >= 0 ||
		cookieByName(t, cleared, CSRFCookieName).MaxAge >= 0 {
		t.Fatalf("activation failure cookies=%+v", cleared)
	}
	var sessions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("replacement Sessions=%d, want 0", sessions)
	}

	accounts := NewAccountStore(fixture.db)
	if _, err := accounts.Authenticate(context.Background(), "admin", "admin"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password error=%v, want ErrInvalidCredentials", err)
	}
	if _, err := accounts.Authenticate(
		context.Background(),
		"admin",
		"long-enough-password",
	); err != nil {
		t.Fatalf("committed password rejected: %v", err)
	}
	oldStatus := fixture.request(t, http.MethodGet, "/_admin/api/session", "", oldCookies, "")
	assertAPIError(t, oldStatus, http.StatusUnauthorized, "unauthorized")
}

// Break caught: accepting a correct CSRF header without the matching double-submit cookie.
func TestAPICSRFRequiresMatchingDoubleSubmitCookie(t *testing.T) {
	tests := []struct {
		name       string
		csrfCookie *http.Cookie
	}{
		{name: "missing cookie"},
		{
			name: "replaced cookie",
			csrfCookie: &http.Cookie{
				Name:  CSRFCookieName,
				Value: "attacker-controlled-cookie",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTestAPI(t)
			login := fixture.login(t, "admin")
			issued := login.Result().Cookies()
			sessionCookie := cookieByName(t, issued, SessionCookieName)
			csrfHeader := cookieByName(t, issued, CSRFCookieName).Value
			cookies := []*http.Cookie{sessionCookie}
			if tt.csrfCookie != nil {
				cookies = append(cookies, tt.csrfCookie)
			}

			response := fixture.request(t, http.MethodPost, "/_admin/api/password",
				`{"current_password":"admin","new_password":"long-enough-password"}`,
				cookies, csrfHeader)
			assertAPIError(t, response, http.StatusForbidden, "csrf_failed")

			if _, err := NewAccountStore(fixture.db).Authenticate(
				context.Background(),
				"admin",
				"admin",
			); err != nil {
				t.Fatalf("password changed after rejected CSRF: %v", err)
			}
		})
	}
}

// Break caught: revealing CSRF validity before rejecting a missing, random, or revoked Session.
func TestAPIInvalidSessionTakesPriorityOverCSRF(t *testing.T) {
	requestBody := `{"current_password":"admin","new_password":"long-enough-password"}`

	t.Run("missing session with mismatched csrf", func(t *testing.T) {
		fixture := newTestAPI(t)
		response := fixture.request(
			t,
			http.MethodPost,
			"/_admin/api/password",
			requestBody,
			[]*http.Cookie{{Name: CSRFCookieName, Value: "csrf-cookie"}},
			"csrf-header",
		)
		assertAPIError(t, response, http.StatusUnauthorized, "unauthorized")
	})

	t.Run("random session with missing csrf cookie", func(t *testing.T) {
		fixture := newTestAPI(t)
		response := fixture.request(
			t,
			http.MethodPost,
			"/_admin/api/password",
			requestBody,
			[]*http.Cookie{{Name: SessionCookieName, Value: "random-session"}},
			"csrf-header",
		)
		assertAPIError(t, response, http.StatusUnauthorized, "unauthorized")
	})

	t.Run("revoked session with mismatched csrf", func(t *testing.T) {
		fixture := newTestAPI(t)
		login := fixture.login(t, "admin")
		issued := login.Result().Cookies()
		issuedCSRF := cookieByName(t, issued, CSRFCookieName).Value
		logout := fixture.request(t, http.MethodPost, "/_admin/api/logout", `{}`, issued, issuedCSRF)
		if logout.Code != http.StatusOK {
			t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
		}

		response := fixture.request(
			t,
			http.MethodPost,
			"/_admin/api/password",
			requestBody,
			[]*http.Cookie{
				cookieByName(t, issued, SessionCookieName),
				{Name: CSRFCookieName, Value: "csrf-cookie"},
			},
			"csrf-header",
		)
		assertAPIError(t, response, http.StatusUnauthorized, "unauthorized")
	})
}

// Break caught: bypassing ProfileService mutations, returning stale CRUD state, or collapsing domain errors into 500.
func TestAPIProfileCRUDAndDomainErrors(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)

	unauthorized := fixture.request(t, http.MethodGet, "/_admin/api/profiles", "", nil, "")
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "unauthorized")

	firstCreate := fixture.request(t, http.MethodPost, "/_admin/api/profiles",
		profileSaveJSON("coding", "Coding", "anthropic", "https://coding.example", true, true),
		cookies, csrf)
	if firstCreate.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", firstCreate.Code, firstCreate.Body.String())
	}
	var first profileResponse
	decodeTestJSON(t, firstCreate, &first)
	if first.ID == 0 || first.Slug != "coding" || first.DisplayName != "Coding" ||
		!first.Enabled || first.Config.Upstream != "https://coding.example" {
		t.Fatalf("first=%+v", first)
	}

	duplicate := fixture.request(t, http.MethodPost, "/_admin/api/profiles",
		profileSaveJSON("coding", "Duplicate", "anthropic", "https://duplicate.example", true, false),
		cookies, csrf)
	assertAPIError(t, duplicate, http.StatusConflict, "profile_slug_conflict")

	invalid := fixture.request(t, http.MethodPost, "/_admin/api/profiles",
		profileSaveJSON("invalid", "Invalid", "anthropic", "ftp://invalid.example", true, false),
		cookies, csrf)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "validation_error")

	secondCreate := fixture.request(t, http.MethodPost, "/_admin/api/profiles",
		profileSaveJSON("research", "Research", "openai", "https://research.example", true, false),
		cookies, csrf)
	if secondCreate.Code != http.StatusCreated {
		t.Fatalf("second create status=%d body=%s", secondCreate.Code, secondCreate.Body.String())
	}
	var second profileResponse
	decodeTestJSON(t, secondCreate, &second)

	updated := fixture.request(t, http.MethodPut, "/_admin/api/profiles/"+strconv.FormatInt(second.ID, 10),
		profileSaveJSON("research", "Research Updated", "openai", "https://updated.example", true, false),
		cookies, csrf)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	var updatedBody profileResponse
	decodeTestJSON(t, updated, &updatedBody)
	if updatedBody.ID != second.ID || updatedBody.DisplayName != "Research Updated" ||
		updatedBody.Config.Upstream != "https://updated.example" {
		t.Fatalf("updated=%+v", updatedBody)
	}

	copied := fixture.request(t, http.MethodPost,
		"/_admin/api/profiles/"+strconv.FormatInt(second.ID, 10)+"/copy",
		`{"slug":"research-copy","display_name":"Research Copy"}`, cookies, csrf)
	if copied.Code != http.StatusCreated {
		t.Fatalf("copy status=%d body=%s", copied.Code, copied.Body.String())
	}
	var copiedBody profileResponse
	decodeTestJSON(t, copied, &copiedBody)
	if copiedBody.ID == second.ID || copiedBody.Slug != "research-copy" ||
		copiedBody.Config.Upstream != "https://updated.example" {
		t.Fatalf("copied=%+v", copiedBody)
	}

	defaulted := fixture.request(t, http.MethodPut, "/_admin/api/default-profile",
		fmt.Sprintf(`{"profile_id":%d}`, second.ID), cookies, csrf)
	if defaulted.Code != http.StatusOK {
		t.Fatalf("set default status=%d body=%s", defaulted.Code, defaulted.Body.String())
	}

	deleteDefault := fixture.request(t, http.MethodDelete,
		"/_admin/api/profiles/"+strconv.FormatInt(second.ID, 10), `{}`, cookies, csrf)
	assertAPIError(t, deleteDefault, http.StatusConflict, "default_profile_required")

	deleted := fixture.request(t, http.MethodDelete,
		"/_admin/api/profiles/"+strconv.FormatInt(first.ID, 10), `{}`, cookies, csrf)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := fixture.request(t, http.MethodGet,
		"/_admin/api/profiles/"+strconv.FormatInt(first.ID, 10), "", cookies, "")
	assertAPIError(t, missing, http.StatusNotFound, "profile_not_found")

	got := fixture.request(t, http.MethodGet,
		"/_admin/api/profiles/"+strconv.FormatInt(second.ID, 10), "", cookies, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body.String())
	}
	var gotBody profileResponse
	decodeTestJSON(t, got, &gotBody)
	if !reflect.DeepEqual(gotBody, updatedBody) {
		t.Fatalf("get=%+v updated=%+v", gotBody, updatedBody)
	}
}

// Break caught: omitting zero-value usage objects, counting data outside 30 days, or querying usage separately per Profile.
func TestAPIProfileListIncludesThirtyDayUsageSummaries(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)
	first := fixture.createProfile(t, cookies, csrf, "coding", true)
	second := fixture.createProfile(t, cookies, csrf, "research", false)

	insertAPIUsage(t, fixture.db, fixture.now.Add(-24*time.Hour), first.ID,
		"coding", "anthropic", "main", "sonnet", 100, 50)
	insertAPIUsage(t, fixture.db, fixture.now.Add(-2*time.Hour), first.ID,
		"coding", "anthropic", "vision", "sonnet", 10, 5)
	insertAPIUsage(t, fixture.db, fixture.now.Add(-31*24*time.Hour), first.ID,
		"coding", "anthropic", "main", "old", 999, 999)

	response := fixture.request(t, http.MethodGet, "/_admin/api/profiles", "", cookies, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		DefaultProfileID int64             `json:"default_profile_id"`
		Profiles         []profileResponse `json:"profiles"`
	}
	decodeTestJSON(t, response, &body)
	if body.DefaultProfileID != first.ID || len(body.Profiles) != 2 {
		t.Fatalf("body=%+v", body)
	}
	if body.Profiles[0].Usage30D != (stats.ProfileSummary{
		Requests: 2, InputTokens: 110, OutputTokens: 55,
	}) {
		t.Fatalf("first usage=%+v", body.Profiles[0].Usage30D)
	}
	if body.Profiles[1].ID != second.ID ||
		body.Profiles[1].Usage30D != (stats.ProfileSummary{}) {
		t.Fatalf("second=%+v", body.Profiles[1])
	}
}

// Break caught: ignoring any supported stats filter or returning unsafe System internals.
func TestAPIStatsFiltersAndSystemRedaction(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)
	first := fixture.createProfile(t, cookies, csrf, "coding", true)
	second := fixture.createProfile(t, cookies, csrf, "research", false)

	from := fixture.now.Add(-2 * time.Hour).UTC()
	to := fixture.now.Add(2 * time.Hour).UTC()
	insertAPIUsage(t, fixture.db, fixture.now, first.ID,
		"coding", "anthropic", "main", "sonnet", 10, 20)
	insertAPIUsage(t, fixture.db, fixture.now, second.ID,
		"research", "openai", "vision", "gpt", 100, 200)

	query := fmt.Sprintf(
		"/_admin/api/stats?profile_id=%d&protocol=anthropic&model=SONNET&kind=main&from=%s&to=%s",
		first.ID,
		from.Format(time.RFC3339),
		to.Format(time.RFC3339),
	)
	response := fixture.request(t, http.MethodGet, query, "", cookies, "")
	if response.Code != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", response.Code, response.Body.String())
	}
	var statsBody stats.Response
	decodeTestJSON(t, response, &statsBody)
	if statsBody.Summary.Requests != 1 || statsBody.Summary.InputTokens != 10 ||
		statsBody.Summary.OutputTokens != 20 || statsBody.Summary.TotalTokens != 30 {
		t.Fatalf("stats=%+v", statsBody)
	}

	if _, err := fixture.db.Exec(`
		INSERT INTO admin_sessions (
			token_hash, csrf_hash, auth_version, created_at, expires_at
		) VALUES (?, ?, 999, ?, ?)`,
		[]byte("DO_NOT_LEAK_TOKEN_HASH"),
		[]byte("DO_NOT_LEAK_CSRF_HASH"),
		fixture.now.Format(time.RFC3339Nano),
		fixture.now.Add(time.Hour).Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	system := fixture.request(t, http.MethodGet, "/_admin/api/system", "", cookies, "")
	if system.Code != http.StatusOK {
		t.Fatalf("system status=%d body=%s", system.Code, system.Body.String())
	}
	var systemBody map[string]any
	decodeTestJSON(t, system, &systemBody)
	databaseInfo, err := os.Stat(filepath.Join(fixture.dataDir, "llm-proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	expectedSystem := map[string]any{
		"version":              "test-version",
		"data_dir":             fixture.dataDir,
		"database_file":        "llm-proxy.db",
		"database_bytes":       float64(databaseInfo.Size()),
		"schema_version":       float64(1),
		"default_profile_id":   float64(first.ID),
		"password_must_change": false,
	}
	if !reflect.DeepEqual(systemBody, expectedSystem) {
		t.Fatalf("system=%+v", systemBody)
	}
	assertResponseDoesNotContain(t, system.Body.String(),
		"password_hash", "admin_sessions", "token_hash", "csrf_hash",
		"DO_NOT_LEAK", "authorization", "headers", "api_key",
		"go_version", "profile_count", "usage_count", "initialized",
		"initialization_state", "high_risk_default_credentials",
		"username", "dsn", "environment")
}

// Break caught: treating an empty or repeated stats scalar filter as if one valid value was supplied.
func TestAPIStatsRejectsEmptyAndRepeatedScalarFilters(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, _ := fixture.changePassword(t)
	filters := []struct {
		name  string
		value string
	}{
		{name: "profile_id", value: "1"},
		{name: "protocol", value: "anthropic"},
		{name: "model", value: "sonnet"},
		{name: "kind", value: "main"},
		{name: "from", value: "2026-07-29T10:00:00Z"},
		{name: "to", value: "2026-07-29T12:00:00Z"},
	}

	for _, filter := range filters {
		t.Run(filter.name+"/empty", func(t *testing.T) {
			path := "/_admin/api/stats?" + filter.name + "="
			response := fixture.request(t, http.MethodGet, path, "", cookies, "")
			assertAPIInvalidRequestField(t, response, filter.name)
		})
		t.Run(filter.name+"/repeated", func(t *testing.T) {
			path := fmt.Sprintf(
				"/_admin/api/stats?%s=%s&%s=%s",
				filter.name,
				filter.value,
				filter.name,
				filter.value,
			)
			response := fixture.request(t, http.MethodGet, path, "", cookies, "")
			assertAPIInvalidRequestField(t, response, filter.name)
		})
	}
}

// Break caught: returning framework text errors or omitting browser hardening on non-success responses.
func TestAPIErrorsAndRoutingAlwaysUseJSONAndSecurityHeaders(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		cookies    []*http.Cookie
		csrf       string
		wantStatus int
		wantCode   string
	}{
		{
			name:   "unauthorized",
			method: http.MethodGet, path: "/_admin/api/profiles",
			wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
		},
		{
			name:   "not found",
			method: http.MethodGet, path: "/_admin/api/missing", cookies: cookies,
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		{
			name:   "method not allowed",
			method: http.MethodPatch, path: "/_admin/api/session", body: `{}`,
			cookies: cookies, csrf: csrf,
			wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := fixture.request(t, tt.method, tt.path, tt.body, tt.cookies, tt.csrf)
			assertAPIError(t, response, tt.wantStatus, tt.wantCode)
			assertSecurityHeaders(t, response)
		})
	}

	success := fixture.request(t, http.MethodGet, "/_admin/api/session", "", cookies, "")
	if success.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", success.Code, success.Body.String())
	}
	assertSecurityHeaders(t, success)
}

// Break caught: known routes returning 405 without the complete, route-specific Allow method set.
func TestAPIKnownRoutesReturnAccurateAllowHeader(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, _ := fixture.changePassword(t)
	tests := []struct {
		name  string
		path  string
		allow []string
	}{
		{name: "login", path: "/_admin/api/login", allow: []string{http.MethodPost}},
		{name: "logout", path: "/_admin/api/logout", allow: []string{http.MethodPost}},
		{name: "session", path: "/_admin/api/session", allow: []string{http.MethodGet, http.MethodHead}},
		{name: "password", path: "/_admin/api/password", allow: []string{http.MethodPost}},
		{
			name: "profile collection",
			path: "/_admin/api/profiles",
			allow: []string{
				http.MethodGet,
				http.MethodHead,
				http.MethodPost,
			},
		},
		{
			name: "profile item",
			path: "/_admin/api/profiles/123",
			allow: []string{
				http.MethodGet,
				http.MethodHead,
				http.MethodPut,
				http.MethodDelete,
			},
		},
		{name: "profile copy", path: "/_admin/api/profiles/123/copy", allow: []string{http.MethodPost}},
		{name: "default profile", path: "/_admin/api/default-profile", allow: []string{http.MethodPut}},
		{name: "stats", path: "/_admin/api/stats", allow: []string{http.MethodGet, http.MethodHead}},
		{name: "system", path: "/_admin/api/system", allow: []string{http.MethodGet, http.MethodHead}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := fixture.request(t, http.MethodOptions, tt.path, "", cookies, "")
			assertAPIError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
			assertAllowMethods(t, response, tt.allow...)
		})
	}
}

// Break caught: letting ServeMux redirect path aliases or rejecting a valid escaped path segment.
func TestAPICanonicalPathBoundary(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)
	profile := fixture.createProfile(t, cookies, csrf, "coding", true)
	if profile.ID != 1 {
		t.Fatalf("profile ID=%d, escaped-path fixture requires first ID 1", profile.ID)
	}

	aliases := []string{
		"/_admin/api//profiles",
		"/_admin/api/./profiles",
		"/_admin/api/profiles/../profiles",
		"/_admin/api/profiles//1",
	}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			response := fixture.request(t, http.MethodGet, alias, "", cookies, "")
			assertAPIError(t, response, http.StatusNotFound, "not_found")
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("Location=%q, want no canonical redirect", location)
			}
			assertSecurityHeaders(t, response)
		})
	}

	escaped := fixture.request(t, http.MethodGet, "/_admin/api/profiles/%31", "", cookies, "")
	if escaped.Code != http.StatusOK {
		t.Fatalf("escaped path status=%d body=%s", escaped.Code, escaped.Body.String())
	}
	var body profileResponse
	decodeTestJSON(t, escaped, &body)
	if body.ID != profile.ID || body.Slug != "coding" {
		t.Fatalf("escaped profile=%+v", body)
	}
}

// Break caught: leaking model resolution details or mapping invalid model fields to 500.
func TestAPIInvalidModelCapabilitiesUseStructuredProfileValidationError(t *testing.T) {
	tests := []struct {
		name   string
		models string
	}{
		{
			name:   "blank ID",
			models: `[{"id":"","supports_vision":false}]`,
		},
		{
			name:   "missing vision choice",
			models: `[{"id":"missing-choice"}]`,
		},
		{
			name: "duplicate exact ID",
			models: `[{"id":"same","supports_vision":false},` +
				`{"id":"same","supports_vision":true}]`,
		},
		{
			name: "output not below context",
			models: `[{"id":"invalid-limits","context_window":128000,` +
				`"max_output_tokens":128000,"supports_vision":false}]`,
		},
		{
			name: "context exceeds browser safe integer",
			models: `[{"id":"unsafe-context","context_window":9007199254740992,` +
				`"supports_vision":false}]`,
		},
		{
			name: "output exceeds browser safe integer",
			models: `[{"id":"unsafe-output","max_output_tokens":9007199254740992,` +
				`"supports_vision":false}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTestAPI(t)
			cookies, csrf := fixture.changePassword(t)
			config := profileConfigJSON("anthropic", "https://invalid.example")
			config = strings.Replace(config, `"vision":`, `"models":`+tt.models+`,"vision":`, 1)
			body := fmt.Sprintf(
				`{"slug":"invalid","display_name":"Invalid","enabled":true,"config":%s,"make_default":false}`,
				config,
			)

			response := fixture.request(
				t,
				http.MethodPost,
				"/_admin/api/profiles",
				body,
				cookies,
				csrf,
			)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var got errorResponse
			decodeTestJSON(t, response, &got)
			if got.Error.Code != "validation_error" ||
				got.Error.Message != "Profile validation failed." ||
				len(got.Error.Fields) != 1 ||
				got.Error.Fields["profile"] != "Profile configuration is invalid." {
				t.Fatalf("error=%+v", got.Error)
			}
			for _, internal := range []string{
				"supports_vision",
				"duplicate model capability",
				"max output tokens",
			} {
				if strings.Contains(response.Body.String(), internal) {
					t.Fatalf("response leaks %q: %s", internal, response.Body.String())
				}
			}
		})
	}
}

// Break caught: changing rate-limit domain failures into generic authentication failures.
func TestAPILoginRateLimitErrorMapping(t *testing.T) {
	fixture := newTestAPI(t)
	for attempt := 1; attempt <= 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
			strings.NewReader(`{"username":"admin","password":"wrong"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "192.0.2.99:4321"
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		if attempt <= 5 {
			assertAPIError(t, response, http.StatusUnauthorized, "invalid_credentials")
		} else {
			assertAPIError(t, response, http.StatusTooManyRequests, "rate_limited")
		}
	}
}

func newTestAPI(t *testing.T) *apiTestFixture {
	return newTestAPIWithActivation(t, nil)
}

func newTestAPIWithActivation(
	t *testing.T,
	activateProfiles func(context.Context) error,
) *apiTestFixture {
	t.Helper()
	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	accounts := NewAccountStore(db)
	if _, _, err := accounts.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	auth := NewAuthService(
		accounts,
		NewSessionStore(db, func() time.Time { return now }),
		NewLoginLimiter(func() time.Time { return now }),
	)
	store := profile.NewStore(db)
	registry := gateway.NewRegistry()
	coordinator := gateway.NewCoordinator(store, registry, func(record profile.Record) (http.Handler, error) {
		if _, err := record.Resolve(); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	logs := &bytes.Buffer{}
	handler := NewAPI(Dependencies{
		Auth:             auth,
		Profiles:         NewProfileService(store, coordinator),
		Stats:            stats.New(db),
		DB:               db,
		Version:          "test-version",
		DataDir:          dataDir,
		Logger:           slog.New(slog.NewTextHandler(logs, nil)),
		ActivateProfiles: activateProfiles,
	})
	return &apiTestFixture{
		handler: handler,
		db:      db,
		dataDir: dataDir,
		logs:    logs,
		now:     now,
	}
}

func (f *apiTestFixture) request(
	t *testing.T,
	method, path, body string,
	cookies []*http.Cookie,
	csrf string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "192.0.2.10:1234"
	if method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *apiTestFixture) login(t *testing.T, password string) *httptest.ResponseRecorder {
	t.Helper()
	response := f.request(t, http.MethodPost, "/_admin/api/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, password), nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}

func (f *apiTestFixture) changePassword(t *testing.T) ([]*http.Cookie, string) {
	t.Helper()
	login := f.login(t, "admin")
	cookies := login.Result().Cookies()
	csrf := cookieByName(t, cookies, CSRFCookieName).Value
	changed := f.request(t, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"long-enough-password"}`,
		cookies, csrf)
	if changed.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", changed.Code, changed.Body.String())
	}
	rotated := changed.Result().Cookies()
	return rotated, cookieByName(t, rotated, CSRFCookieName).Value
}

func (f *apiTestFixture) createProfile(
	t *testing.T,
	cookies []*http.Cookie,
	csrf, slug string,
	makeDefault bool,
) profileResponse {
	t.Helper()
	response := f.request(t, http.MethodPost, "/_admin/api/profiles",
		profileSaveJSON(slug, strings.ToUpper(slug[:1])+slug[1:], "anthropic",
			"https://"+slug+".example", true, makeDefault),
		cookies, csrf)
	if response.Code != http.StatusCreated {
		t.Fatalf("create %s status=%d body=%s", slug, response.Code, response.Body.String())
	}
	var body profileResponse
	decodeTestJSON(t, response, &body)
	return body
}

func profileSaveJSON(
	slug, displayName, protocol, upstream string,
	enabled, makeDefault bool,
) string {
	return fmt.Sprintf(
		`{"slug":%q,"display_name":%q,"enabled":%t,"config":%s,"make_default":%t}`,
		slug, displayName, enabled, profileConfigJSON(protocol, upstream), makeDefault,
	)
}

func profileConfigJSON(protocol, upstream string) string {
	return fmt.Sprintf(
		`{"version":1,"protocol":%q,"upstream":%q,"vision":{"enabled":false,"model":"sonnet","max_tokens":2048,"timeout":"2m","max_concurrency":4,"cache_ttl":"30m","cache_max_entries":512},"overload_rules":[]}`,
		protocol, upstream,
	)
}

func insertAPIUsage(
	t *testing.T,
	db *sql.DB,
	createdAt time.Time,
	profileID int64,
	slug, protocol, kind, model string,
	inputTokens, outputTokens int,
) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO usage (
			created_at, profile_id, profile_slug, protocol, request_kind,
			model, path, input_tokens, output_tokens
		) VALUES (?, ?, ?, ?, ?, ?, '/v1/messages', ?, ?)`,
		createdAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		profileID, slug, protocol, kind, model, inputTokens, outputTokens,
	); err != nil {
		t.Fatal(err)
	}
}

func decodeTestJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode body %q: %v", response.Body.String(), err)
	}
}

func assertAPIError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), wantStatus)
	}
	var body errorResponse
	decodeTestJSON(t, response, &body)
	if body.Error.Code != wantCode || body.Error.Message == "" {
		t.Fatalf("error=%+v, want code=%q", body.Error, wantCode)
	}
}

func assertAPIInvalidRequestField(
	t *testing.T,
	response *httptest.ResponseRecorder,
	field string,
) {
	t.Helper()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", response.Code, response.Body.String())
	}
	var body errorResponse
	decodeTestJSON(t, response, &body)
	if body.Error.Code != "invalid_request" || len(body.Error.Fields) != 1 ||
		body.Error.Fields[field] == "" {
		t.Fatalf("error=%+v, want invalid_request field %q", body.Error, field)
	}
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
	for name, value := range want {
		if got := response.Header().Get(name); got != value {
			t.Fatalf("%s=%q, want %q", name, got, value)
		}
	}
}

func assertAllowMethods(
	t *testing.T,
	response *httptest.ResponseRecorder,
	want ...string,
) {
	t.Helper()
	values := response.Header().Values("Allow")
	if len(values) != 1 {
		t.Fatalf("Allow header count=%d, want 1", len(values))
	}
	got := strings.Split(values[0], ",")
	if len(got) != len(want) {
		t.Fatalf("Allow=%q, want methods %v", values[0], want)
	}
	seen := make(map[string]struct{}, len(got))
	for _, method := range got {
		method = strings.TrimSpace(method)
		if _, duplicate := seen[method]; duplicate {
			t.Fatalf("Allow=%q contains duplicate method", values[0])
		}
		seen[method] = struct{}{}
	}
	for _, method := range want {
		if _, ok := seen[method]; !ok {
			t.Fatalf("Allow=%q, missing %s", values[0], method)
		}
	}
}

func assertResponseDoesNotContain(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, value := range forbidden {
		if strings.Contains(lower, strings.ToLower(value)) {
			t.Fatalf("response contains %q: %s", value, body)
		}
	}
}
