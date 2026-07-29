package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/admin"
	"github.com/Euphie/llm-proxy/internal/profile"
)

const (
	initializationPasswordSentinel      = "password-sentinel-6f27"
	initializationAuthorizationSentinel = "authorization-sentinel-92c1"
	initializationSessionSentinel       = "session-sentinel-1d54"
	initializationCSRFSentinel          = "csrf-sentinel-0b88"
	initializationPassword              = "strong-admin-password"
)

// Break caught: wiring the initialization APIs without publishing the created default runtime or recording its main usage.
func TestInitializationLifecycle(t *testing.T) {
	type wireObservation struct {
		pathOK          bool
		authorizationOK bool
	}
	wire := make(chan wireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire <- wireObservation{
			pathOK:          r.URL.RequestURI() == "/v1/messages",
			authorizationOK: r.Header.Get("Authorization") == initializationAuthorizationSentinel,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model":"sonnet",
			"content":[{"type":"text","text":"ready"}],
			"usage":{"input_tokens":11,"output_tokens":7}
		}`)
	}))
	t.Cleanup(upstream.Close)

	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	baseSecrets := [][]byte{
		[]byte(initializationPasswordSentinel),
		[]byte(initializationAuthorizationSentinel),
		[]byte(initializationSessionSentinel),
		[]byte(initializationCSRFSentinel),
		[]byte(initializationPassword),
	}

	fakeSession := &http.Cookie{
		Name:  admin.SessionCookieName,
		Value: initializationSessionSentinel,
	}
	fakeCSRF := &http.Cookie{
		Name:  admin.CSRFCookieName,
		Value: initializationCSRFSentinel,
	}
	fakeProfileRequest := authenticatedRequest(
		t,
		http.MethodPost,
		"/_admin/api/profiles",
		strings.NewReader(`{}`),
		fakeSession,
		fakeCSRF,
	)
	fakeProfileResponse := serveInitializationRequest(application, fakeProfileRequest)
	fakeProfileCapture := captureInitializationResponse(fakeProfileResponse)
	assertInitializationCaptureSecrets(
		t,
		"invalid Session response",
		fakeProfileCapture,
		baseSecrets,
		nil,
	)
	assertInitializationAPIError(
		t,
		"invalid Session response",
		fakeProfileResponse,
		http.StatusUnauthorized,
		"unauthorized",
	)

	failedLogin := httptest.NewRequest(
		http.MethodPost,
		"/_admin/api/login",
		strings.NewReader(
			`{"username":"admin","password":"`+initializationPasswordSentinel+`"}`,
		),
	)
	failedLogin.Header.Set("Content-Type", "application/json")
	failedLogin.RemoteAddr = "192.0.2.20:1234"
	failedLoginResponse := serveInitializationRequest(application, failedLogin)
	failedLoginCapture := captureInitializationResponse(failedLoginResponse)
	assertInitializationCaptureSecrets(
		t,
		"failed login response",
		failedLoginCapture,
		baseSecrets,
		nil,
	)
	assertInitializationAPIError(
		t,
		"failed login response",
		failedLoginResponse,
		http.StatusUnauthorized,
		"invalid_credentials",
	)

	login := httptest.NewRequest(
		http.MethodPost,
		"/_admin/api/login",
		strings.NewReader(`{"username":"admin","password":"admin"}`),
	)
	login.Header.Set("Content-Type", "application/json")
	login.RemoteAddr = "192.0.2.21:1234"
	loginResponse := serveInitializationRequest(application, login)
	loginCapture := captureInitializationResponse(loginResponse)
	assertInitializationCaptureSecrets(t, "login response", loginCapture, baseSecrets, nil)
	initialSession := initializationCookie(
		t,
		loginResponse.Result().Cookies(),
		admin.SessionCookieName,
	)
	initialCSRF := initializationCookie(
		t,
		loginResponse.Result().Cookies(),
		admin.CSRFCookieName,
	)
	initialSecrets := appendSecrets(
		baseSecrets,
		[]byte(initialSession.Value),
		[]byte(initialCSRF.Value),
	)
	assertInitializationCaptureSecrets(
		t,
		"login response",
		loginCapture,
		initialSecrets,
		map[int]string{
			len(baseSecrets):     admin.SessionCookieName,
			len(baseSecrets) + 1: admin.CSRFCookieName,
		},
	)
	assertInitializationStatus(t, "bootstrap login", loginResponse, http.StatusOK)

	config := profile.NewConfig(profile.ProtocolAnthropic, upstream.URL)
	config.OverloadRules = []profile.RetryRule{}
	profileBody, err := json.Marshal(struct {
		Slug        string         `json:"slug"`
		DisplayName string         `json:"display_name"`
		Enabled     bool           `json:"enabled"`
		Config      profile.Config `json:"config"`
		MakeDefault bool           `json:"make_default"`
	}{
		Slug:        "primary",
		DisplayName: "Primary",
		Enabled:     true,
		Config:      config,
		MakeDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	invalidCSRFRequest := authenticatedRequest(
		t,
		http.MethodPost,
		"/_admin/api/profiles",
		bytes.NewReader(profileBody),
		initialSession,
		fakeCSRF,
	)
	invalidCSRFResponse := serveInitializationRequest(application, invalidCSRFRequest)
	invalidCSRFCapture := captureInitializationResponse(invalidCSRFResponse)
	assertInitializationCaptureSecrets(
		t,
		"invalid CSRF response",
		invalidCSRFCapture,
		initialSecrets,
		nil,
	)
	assertInitializationAPIError(
		t,
		"invalid CSRF response",
		invalidCSRFResponse,
		http.StatusForbidden,
		"csrf_failed",
	)
	assertInitializationProfileCount(t, application.db, 0)

	blockedProfileRequest := authenticatedRequest(
		t,
		http.MethodPost,
		"/_admin/api/profiles",
		bytes.NewReader(profileBody),
		initialSession,
		initialCSRF,
	)
	blockedProfileResponse := serveInitializationRequest(application, blockedProfileRequest)
	blockedProfileCapture := captureInitializationResponse(blockedProfileResponse)
	assertInitializationCaptureSecrets(
		t,
		"password-gated Profile response",
		blockedProfileCapture,
		initialSecrets,
		nil,
	)
	assertInitializationAPIError(
		t,
		"password-gated Profile response",
		blockedProfileResponse,
		http.StatusForbidden,
		"password_change_required",
	)

	passwordRequest := authenticatedRequest(
		t,
		http.MethodPost,
		"/_admin/api/password",
		strings.NewReader(
			`{"current_password":"admin","new_password":"`+initializationPassword+`"}`,
		),
		initialSession,
		initialCSRF,
	)
	passwordResponse := serveInitializationRequest(application, passwordRequest)
	passwordCapture := captureInitializationResponse(passwordResponse)
	assertInitializationCaptureSecrets(
		t,
		"password response",
		passwordCapture,
		initialSecrets,
		nil,
	)
	rotatedSession := initializationCookie(
		t,
		passwordResponse.Result().Cookies(),
		admin.SessionCookieName,
	)
	rotatedCSRF := initializationCookie(
		t,
		passwordResponse.Result().Cookies(),
		admin.CSRFCookieName,
	)
	allSecrets := appendSecrets(
		initialSecrets,
		[]byte(rotatedSession.Value),
		[]byte(rotatedCSRF.Value),
	)
	assertInitializationCaptureSecrets(
		t,
		"password response",
		passwordCapture,
		allSecrets,
		map[int]string{
			len(initialSecrets):     admin.SessionCookieName,
			len(initialSecrets) + 1: admin.CSRFCookieName,
		},
	)
	assertInitializationStatus(t, "password change", passwordResponse, http.StatusOK)
	if rotatedSession.Value == initialSession.Value || rotatedCSRF.Value == initialCSRF.Value {
		t.Fatal("password change did not rotate both authentication cookies")
	}

	oldSessionRequest := authenticatedRequest(
		t,
		http.MethodGet,
		"/_admin/api/session",
		nil,
		initialSession,
		initialCSRF,
	)
	oldSessionResponse := serveInitializationRequest(application, oldSessionRequest)
	oldSessionCapture := captureInitializationResponse(oldSessionResponse)
	assertInitializationCaptureSecrets(
		t,
		"old Session response",
		oldSessionCapture,
		allSecrets,
		nil,
	)
	assertInitializationAPIError(
		t,
		"old Session response",
		oldSessionResponse,
		http.StatusUnauthorized,
		"unauthorized",
	)

	createProfileRequest := authenticatedRequest(
		t,
		http.MethodPost,
		"/_admin/api/profiles",
		bytes.NewReader(profileBody),
		rotatedSession,
		rotatedCSRF,
	)
	createProfileResponse := serveInitializationRequest(application, createProfileRequest)
	createProfileCapture := captureInitializationResponse(createProfileResponse)
	assertInitializationCaptureSecrets(
		t,
		"Profile creation response",
		createProfileCapture,
		allSecrets,
		nil,
	)
	assertInitializationStatus(
		t,
		"Profile creation",
		createProfileResponse,
		http.StatusCreated,
	)
	var created struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(createProfileResponse.Body).Decode(&created); err != nil {
		t.Fatal("Profile response decoded: false")
	}
	if created.ID <= 0 || created.Slug != "primary" || !created.Enabled {
		t.Fatalf(
			"created Profile metadata invalid: idPositive=%v slugOK=%v enabled=%v",
			created.ID > 0,
			created.Slug == "primary",
			created.Enabled,
		)
	}

	proxyRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(
			`{"model":"main-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
		),
	)
	proxyRequest.Header.Set("Content-Type", "application/json")
	proxyRequest.Header.Set("Authorization", initializationAuthorizationSentinel)
	proxyResponse := serveInitializationRequest(application, proxyRequest)
	proxyCapture := captureInitializationResponse(proxyResponse)
	assertInitializationCaptureSecrets(
		t,
		"proxy response",
		proxyCapture,
		allSecrets,
		nil,
	)
	assertInitializationDataSecrets(t, "logs", logs.Bytes(), allSecrets)
	assertInitializationStatus(t, "proxy", proxyResponse, http.StatusOK)

	select {
	case observed := <-wire:
		if !observed.pathOK || !observed.authorizationOK {
			t.Fatalf(
				"upstream request mismatch: pathOK=%v authorizationOK=%v",
				observed.pathOK,
				observed.authorizationOK,
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not observed")
	}

	waitForInitializationUsage(t, application.db, created.ID)
	assertInitializationDataSecrets(t, "logs after usage write", logs.Bytes(), allSecrets)
}

func TestInitializationSecretGuardRejectsHeaderReflection(t *testing.T) {
	secret := []byte("issued-token")
	tests := []struct {
		name           string
		body           []byte
		headers        http.Header
		allowedCookies map[int]string
		wantLeak       bool
	}{
		{
			name:     "body reflection",
			body:     []byte("issued-token"),
			headers:  make(http.Header),
			wantLeak: true,
		},
		{
			name: "ordinary header reflection",
			headers: http.Header{
				"X-Reflected-Credential": {"issued-token"},
			},
			wantLeak: true,
		},
		{
			name: "exact issuing cookie exemption",
			headers: http.Header{
				"Set-Cookie": {"llm_proxy_session=issued-token; Path=/_admin; HttpOnly"},
			},
			allowedCookies: map[int]string{0: admin.SessionCookieName},
			wantLeak:       false,
		},
		{
			name: "issuing cookie does not exempt another header",
			headers: http.Header{
				"Set-Cookie":             {"llm_proxy_session=issued-token; Path=/_admin; HttpOnly"},
				"X-Reflected-Credential": {"issued-token"},
			},
			allowedCookies: map[int]string{0: admin.SessionCookieName},
			wantLeak:       true,
		},
		{
			name: "issuing cookie exemption removes one exact pair",
			headers: http.Header{
				"Set-Cookie": {"llm_proxy_session=issued-token; Path=/_admin; Note=issued-token"},
			},
			allowedCookies: map[int]string{0: admin.SessionCookieName},
			wantLeak:       true,
		},
		{
			name: "issuing cookie exemption is consumed once",
			headers: http.Header{
				"Set-Cookie": {
					"llm_proxy_session=issued-token; Path=/_admin; HttpOnly",
					"llm_proxy_session=issued-token; Path=/_admin; HttpOnly",
				},
			},
			allowedCookies: map[int]string{0: admin.SessionCookieName},
			wantLeak:       true,
		},
		{
			name: "wrong cookie name is not exempt",
			headers: http.Header{
				"Set-Cookie": {"other=issued-token; Path=/_admin"},
			},
			allowedCookies: map[int]string{0: admin.SessionCookieName},
			wantLeak:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, leaked := findInitializationSecretLeak(
				tt.body,
				tt.headers,
				[][]byte{secret},
				tt.allowedCookies,
			)
			if leaked != tt.wantLeak {
				t.Fatalf("leak=%v, want %v", leaked, tt.wantLeak)
			}
		})
	}

	t.Run("CSRF sentinel header reflection", func(t *testing.T) {
		_, _, leaked := findInitializationSecretLeak(
			nil,
			http.Header{"X-CSRF-Reflection": {initializationCSRFSentinel}},
			[][]byte{[]byte(initializationCSRFSentinel)},
			nil,
		)
		if !leaked {
			t.Fatal("CSRF-specific header leak detected=false")
		}
	})
}

// Break caught: scanning the recorder's mutable HeaderMap instead of the headers committed to the client.
func TestCaptureInitializationResponseUsesCommittedHeaders(t *testing.T) {
	secret := []byte("committed-header-secret")
	response := httptest.NewRecorder()
	response.Header().Set("X-Reflected-Credential", string(secret))
	response.WriteHeader(http.StatusNoContent)
	response.Header().Del("X-Reflected-Credential")

	capture := captureInitializationResponse(response)
	_, _, leaked := findInitializationSecretLeak(
		capture.body,
		capture.headers,
		[][]byte{secret},
		nil,
	)
	if !leaked {
		t.Fatal("committed response header leak detected=false")
	}
}

func authenticatedRequest(
	t *testing.T,
	method, target string,
	body io.Reader,
	session, csrf *http.Cookie,
) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "192.0.2.30:1234"
	req.AddCookie(session)
	req.AddCookie(csrf)
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", csrf.Value)
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serveInitializationRequest(
	application *App,
	request *http.Request,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	return response
}

func initializationCookie(
	t *testing.T,
	cookies []*http.Cookie,
	name string,
) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatal("authentication cookie was issued: false")
	return nil
}

func assertInitializationAPIError(
	t *testing.T,
	surface string,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("%s status matched expectation: false", surface)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("%s error response decoded: false", surface)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("%s error code matched expectation: false", surface)
	}
}

func assertInitializationStatus(
	t *testing.T,
	surface string,
	response *httptest.ResponseRecorder,
	wantStatus int,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("%s status matched expectation: false", surface)
	}
}

func assertInitializationProfileCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&count); err != nil {
		t.Fatal("Profile side-effect query succeeded: false")
	}
	if count != want {
		t.Fatal("invalid CSRF request had no Profile side effect: false")
	}
}

func waitForInitializationUsage(t *testing.T, db *sql.DB, profileID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var slug, protocol, kind, path, model string
		var inputTokens, outputTokens int
		err := db.QueryRow(`
			SELECT profile_slug, protocol, request_kind, path, model,
			       input_tokens, output_tokens
			FROM usage
			WHERE profile_id = ? AND request_kind = 'main'
			ORDER BY id DESC
			LIMIT 1`,
			profileID,
		).Scan(
			&slug,
			&protocol,
			&kind,
			&path,
			&model,
			&inputTokens,
			&outputTokens,
		)
		if err == nil {
			if slug != "primary" || protocol != "anthropic" || kind != "main" ||
				path != "/v1/messages" || model != "sonnet" ||
				inputTokens != 11 || outputTokens != 7 {
				t.Fatal("usage metadata matched expectations: false")
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("main usage query succeeded: false")
		}
		if time.Now().After(deadline) {
			t.Fatal("main usage was not recorded before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type initializationResponseCapture struct {
	body    []byte
	headers http.Header
}

func captureInitializationResponse(
	response *httptest.ResponseRecorder,
) initializationResponseCapture {
	body := append([]byte(nil), response.Body.Bytes()...)
	result := response.Result()
	headers := result.Header.Clone()
	_ = result.Body.Close()
	return initializationResponseCapture{
		body:    body,
		headers: headers,
	}
}

func appendSecrets(base [][]byte, additional ...[]byte) [][]byte {
	combined := make([][]byte, 0, len(base)+len(additional))
	combined = append(combined, base...)
	combined = append(combined, additional...)
	return combined
}

func assertInitializationCaptureSecrets(
	t *testing.T,
	surface string,
	capture initializationResponseCapture,
	secrets [][]byte,
	allowedCookies map[int]string,
) {
	t.Helper()
	secretIndex, location, leaked := findInitializationSecretLeak(
		capture.body,
		capture.headers,
		secrets,
		allowedCookies,
	)
	if leaked {
		t.Fatalf(
			"%s %s leaked management secret %d",
			surface,
			location,
			secretIndex+1,
		)
	}
}

func assertInitializationDataSecrets(
	t *testing.T,
	surface string,
	data []byte,
	secrets [][]byte,
) {
	t.Helper()
	secretIndex, location, leaked := findInitializationSecretLeak(
		data,
		nil,
		secrets,
		nil,
	)
	if leaked {
		t.Fatalf(
			"%s %s leaked management secret %d",
			surface,
			location,
			secretIndex+1,
		)
	}
}

func findInitializationSecretLeak(
	body []byte,
	headers http.Header,
	secrets [][]byte,
	allowedCookies map[int]string,
) (int, string, bool) {
	for secretIndex, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		allowedCookie, cookieExemptionAvailable := allowedCookies[secretIndex]
		if bytes.Contains(body, secret) {
			return secretIndex, "body", true
		}
		for headerName, values := range headers {
			for _, value := range values {
				inspected := []byte(value)
				if strings.EqualFold(headerName, "Set-Cookie") &&
					cookieExemptionAvailable {
					cookiePair := []byte(allowedCookie + "=" + string(secret))
					if bytes.HasPrefix(inspected, cookiePair) &&
						(len(inspected) == len(cookiePair) ||
							inspected[len(cookiePair)] == ';') {
						inspected = inspected[len(cookiePair):]
						cookieExemptionAvailable = false
					}
				}
				if bytes.Contains(inspected, secret) {
					return secretIndex, "header", true
				}
			}
		}
	}
	return 0, "", false
}
