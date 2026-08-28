package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/admin"
	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/stats"
)

// Break caught: enabling the known bootstrap credential without warning on every affected startup.
func TestNewWarnsOnEveryStartupWhileDefaultCredentialIsActive(t *testing.T) {
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	dataDir := filepath.Join(t.TempDir(), "startup-secret-sentinel")
	for range 2 {
		application, err := New(Options{DataDir: dataDir})
		if err != nil {
			t.Fatal(err)
		}
		if err := application.Close(); err != nil {
			t.Fatal(err)
		}
	}

	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	accounts := admin.NewAccountStore(db)
	account, err := accounts.Get(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(
		context.Background(),
		account.ID,
		"admin",
		"strong-admin-password",
	); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	application, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	output := logs.String()
	if strings.Count(output, "admin/admin") != 2 {
		t.Fatal("bootstrap credential warning count matched: false")
	}
	if !strings.Contains(strings.ToLower(output), "high risk") ||
		!strings.Contains(strings.ToLower(output), "change") ||
		!strings.Contains(strings.ToLower(output), "immediately") {
		t.Fatal("bootstrap credential warning guidance matched: false")
	}
	for _, secret := range []string{"startup-secret-sentinel", "strong-admin-password"} {
		if strings.Contains(output, secret) {
			t.Fatal("startup warning leaked unrelated secret")
		}
	}
}

// Break caught: wiring the data router without the bootstrap admin API or publishing a runtime before initialization.
func TestNewStartsUnconfiguredAndServesAdminAPI(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	proxyReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	proxyRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(proxyRes, proxyReq)
	if proxyRes.Code != http.StatusServiceUnavailable ||
		!strings.Contains(proxyRes.Body.String(), "proxy_not_configured") {
		t.Fatalf("proxy status=%d body=%q", proxyRes.Code, proxyRes.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
		strings.NewReader(`{"username":"admin","password":"admin"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "192.0.2.1:1234"
	loginRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(loginRes, loginReq)
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%q", loginRes.Code, loginRes.Body.String())
	}
}

func TestSystemAPIUsesConfiguredDataDirectoryAndVersion(t *testing.T) {
	dataDir := t.TempDir()
	application, err := New(Options{
		DataDir: dataDir,
		Version: "app-test-version",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	loginCookies := login.Result().Cookies()
	changed := request(
		application,
		http.MethodPost,
		"/_admin/api/password",
		`{"current_password":"admin","new_password":"long-enough-password"}`,
		loginCookies,
		findCookie(t, loginCookies, admin.CSRFCookieName).Value,
	)
	if changed.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", changed.Code, changed.Body.String())
	}

	response := request(
		application,
		http.MethodGet,
		"/_admin/api/system",
		"",
		changed.Result().Cookies(),
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("system status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	databaseInfo, err := os.Stat(filepath.Join(dataDir, "llm-proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 7 ||
		body["version"] != "app-test-version" ||
		body["data_dir"] != dataDir ||
		body["database_file"] != "llm-proxy.db" ||
		body["database_bytes"] != float64(databaseInfo.Size()) ||
		body["schema_version"] != float64(5) ||
		body["default_profile_id"] != float64(0) ||
		body["password_must_change"] != false {
		t.Fatalf("system=%+v", body)
	}
}

// Break caught: mounting the SPA over Admin API routes or leaving non-API admin paths on the JSON handler.
func TestNewMountsAdminUIWithoutShadowingAPI(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%q", login.Code, login.Body.String())
	}
	loginCookies := login.Result().Cookies()

	session := request(
		application,
		http.MethodGet,
		"/_admin/api/session",
		"",
		loginCookies,
		"",
	)
	if session.Code != http.StatusOK ||
		session.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		!strings.Contains(session.Body.String(), `"username":"admin"`) {
		t.Fatalf(
			"session status=%d type=%q body=%q",
			session.Code,
			session.Header().Get("Content-Type"),
			session.Body.String(),
		)
	}

	loginCSRF := findCookie(t, loginCookies, "llm_proxy_csrf").Value
	password := request(application, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		loginCookies, loginCSRF)
	if password.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%q", password.Code, password.Body.String())
	}
	authenticatedCookies := password.Result().Cookies()
	authenticatedCSRF := findCookie(t, authenticatedCookies, "llm_proxy_csrf").Value

	missingAPI := request(
		application,
		http.MethodGet,
		"/_admin/api/not-found",
		"",
		authenticatedCookies,
		"",
	)
	if missingAPI.Code != http.StatusNotFound ||
		missingAPI.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		!strings.Contains(missingAPI.Body.String(), `"code":"not_found"`) {
		t.Fatalf(
			"missing API status=%d type=%q body=%q",
			missingAPI.Code,
			missingAPI.Header().Get("Content-Type"),
			missingAPI.Body.String(),
		)
	}

	wrongMethod := request(
		application,
		http.MethodPost,
		"/_admin/api/session",
		`{}`,
		authenticatedCookies,
		authenticatedCSRF,
	)
	if wrongMethod.Code != http.StatusMethodNotAllowed ||
		wrongMethod.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		!strings.Contains(wrongMethod.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf(
			"wrong API method status=%d type=%q body=%q",
			wrongMethod.Code,
			wrongMethod.Header().Get("Content-Type"),
			wrongMethod.Body.String(),
		)
	}

	spa := request(application, http.MethodGet, "/_admin/profiles", "", nil, "")
	if spa.Code != http.StatusOK ||
		!strings.Contains(spa.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(spa.Body.String(), `<main id="app"`) {
		t.Fatalf(
			"SPA status=%d type=%q body=%q",
			spa.Code,
			spa.Header().Get("Content-Type"),
			spa.Body.String(),
		)
	}
}

// Break caught: allowing outer ServeMux cleaning to redirect an API-origin request into the SPA.
func TestNewKeepsNonCanonicalAPIPathsInsideJSONContract(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	for _, tc := range []struct {
		name       string
		requestURI string
		path       string
		rawPath    string
	}{
		{
			name:       "dot segment",
			requestURI: "/_admin/api/../profiles",
			path:       "/_admin/api/../profiles",
		},
		{
			name:       "repeated slash",
			requestURI: "/_admin/api//session",
			path:       "/_admin/api//session",
		},
		{
			name:       "encoded dot segment",
			requestURI: "/_admin/api/%2e%2e/profiles",
			path:       "/_admin/api/../profiles",
			rawPath:    "/_admin/api/%2e%2e/profiles",
		},
		{
			name:       "encoded slash",
			requestURI: "/_admin/api%2Fsession",
			path:       "/_admin/api/session",
			rawPath:    "/_admin/api%2Fsession",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := rawRequest(application, tc.requestURI, tc.path, tc.rawPath)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type=%q", contentType)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("Location=%q", location)
			}
			if !strings.Contains(response.Body.String(), `"code":"not_found"`) ||
				strings.Contains(response.Body.String(), `<main id="app"`) {
				t.Fatalf("body=%q", response.Body.String())
			}
			assertAdminPathSecurityHeaders(t, response)
		})
	}
}

// Break caught: allowing outer ServeMux cleaning to redirect an asset-origin request into the Admin API.
func TestNewKeepsNonCanonicalAssetPathsOutOfAPI(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	for _, tc := range []struct {
		name       string
		requestURI string
		path       string
		rawPath    string
	}{
		{
			name:       "dot segment",
			requestURI: "/_admin/assets/../api/session",
			path:       "/_admin/assets/../api/session",
		},
		{
			name:       "encoded dot segment",
			requestURI: "/_admin/assets/%2e%2e/api/session",
			path:       "/_admin/assets/../api/session",
			rawPath:    "/_admin/assets/%2e%2e/api/session",
		},
		{
			name:       "encoded slash",
			requestURI: "/_admin/assets%2F..%2Fapi/session",
			path:       "/_admin/assets/../api/session",
			rawPath:    "/_admin/assets%2F..%2Fapi/session",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := rawRequest(application, tc.requestURI, tc.path, tc.rawPath)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
				t.Fatalf("Content-Type=%q", contentType)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("Location=%q", location)
			}
			if strings.Contains(response.Body.String(), `"code":`) ||
				strings.Contains(response.Body.String(), `<main id="app"`) {
				t.Fatalf("body=%q", response.Body.String())
			}
			assertAdminPathSecurityHeaders(t, response)
		})
	}
}

// Break caught: treating an empty DataDir as invalid instead of using the documented local runtime directory.
func TestNewDefaultsBlankDataDirectory(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := t.TempDir()
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	application, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	if _, err := os.Stat(filepath.Join(workingDirectory, "data", "llm-proxy.db")); err != nil {
		t.Fatalf("default runtime database was not created: %v", err)
	}
}

// Break caught: leaving an eligible persisted default Profile offline after the bootstrap password changes.
func TestChangingBootstrapPasswordActivatesPersistedDefaultProfile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store := profile.NewStore(db)
	if _, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        "persisted",
		DisplayName: "Persisted",
		Enabled:     true,
		Config:      profile.NewConfig(profile.ProtocolAnthropic, upstream.URL),
	}, true); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	application, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	response := request(application, http.MethodPost, "/v1/messages", `{}`, nil, "")
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "proxy_not_configured") {
		t.Fatalf("proxy status=%d body=%q", response.Code, response.Body.String())
	}

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	csrf := findCookie(t, cookies, "llm_proxy_csrf").Value
	changed := request(application, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		cookies, csrf)
	if changed.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", changed.Code, changed.Body.String())
	}

	response = request(application, http.MethodPost, "/v1/messages", `{}`, nil, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("activated proxy status=%d body=%q", response.Code, response.Body.String())
	}
}

// Break caught: reporting ready after a committed password change when failed runtime activation was never retried.
func TestLoginRecoversRuntimeAfterCommittedPasswordActivationFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	validConfig := profile.NewConfig(profile.ProtocolAnthropic, upstream.URL)
	store := profile.NewStore(db)
	record, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        "persisted",
		DisplayName: "Persisted",
		Enabled:     true,
		Config:      validConfig,
	}, true)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	application, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	invalidConfig := validConfig
	invalidConfig.Vision.Timeout = "not-a-duration"
	encodedInvalid, err := json.Marshal(invalidConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.db.Exec(
		`UPDATE profiles SET config_json = ? WHERE id = ?`,
		string(encodedInvalid),
		record.ID,
	); err != nil {
		t.Fatal(err)
	}

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatal("bootstrap login succeeded: false")
	}
	loginCookies := login.Result().Cookies()
	loginCSRF := findCookie(t, loginCookies, admin.CSRFCookieName).Value
	changed := request(application, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		loginCookies, loginCSRF)
	if changed.Code != http.StatusServiceUnavailable ||
		!strings.Contains(changed.Body.String(), "runtime_sync_failed") {
		t.Fatal("activation failure reported committed password change: false")
	}
	var sessions int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatal("activation failure created no replacement Session: false")
	}

	encodedValid, err := json.Marshal(validConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.db.Exec(
		`UPDATE profiles SET config_json = ? WHERE id = ?`,
		string(encodedValid),
		record.ID,
	); err != nil {
		t.Fatal(err)
	}

	recovered := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"strong-admin-password"}`, nil, "")
	if recovered.Code != http.StatusOK {
		t.Fatal("post-bootstrap login recovered runtime: false")
	}
	var status struct {
		Initialized         bool   `json:"initialized"`
		InitializationState string `json:"initialization_state"`
	}
	if err := json.NewDecoder(recovered.Body).Decode(&status); err != nil {
		t.Fatal("recovery response decoded: false")
	}
	if !status.Initialized || status.InitializationState != "ready" {
		t.Fatal("recovery response reported ready: false")
	}

	proxyResponse := request(application, http.MethodPost, "/v1/messages", `{}`, nil, "")
	if proxyResponse.Code != http.StatusNoContent {
		t.Fatal("recovered runtime served data request: false")
	}
}

// Break caught: skipping persisted Profile runtime validation while the bootstrap password is active.
func TestNewRejectsUnresolvablePersistedProfileBeforePasswordChange(t *testing.T) {
	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store := profile.NewStore(db)
	record, err := store.Save(context.Background(), profile.SaveInput{
		Slug:        "persisted",
		DisplayName: "Persisted",
		Enabled:     true,
		Config:      profile.NewConfig(profile.ProtocolAnthropic, "https://example.test"),
	}, true)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	invalidConfig := record.Config
	invalidConfig.Vision.Timeout = "not-a-duration"
	encoded, err := json.Marshal(invalidConfig)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE profiles SET config_json = ? WHERE id = ?`,
		string(encoded),
		record.ID,
	); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	application, err := New(Options{DataDir: dataDir})
	if application != nil {
		_ = application.Close()
	}
	if !errors.Is(err, profile.ErrInvalidConfig) {
		t.Fatal("startup rejected unresolvable persisted Profile: false")
	}
}

// Break caught: treating password completion alone as enough to configure an empty data plane.
func TestChangingBootstrapPasswordWithoutProfileKeepsDataPlaneOffline(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	csrf := findCookie(t, cookies, "llm_proxy_csrf").Value
	changed := request(application, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		cookies, csrf)
	if changed.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", changed.Code, changed.Body.String())
	}

	response := request(application, http.MethodPost, "/v1/messages", `{}`, nil, "")
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "proxy_not_configured") {
		t.Fatalf("proxy status=%d body=%q", response.Code, response.Body.String())
	}
}

// Break caught: recreating bootstrap credentials or failing to publish persisted Profiles on restart.
func TestReopeningDataDirectoryPreservesPasswordAndProfiles(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"profile":"persisted"}`)
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	first, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	firstClosed := false
	defer func() {
		if !firstClosed {
			_ = first.Close()
		}
	}()

	login := request(first, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("initial login status=%d body=%s", login.Code, login.Body.String())
	}
	loginCookies := login.Result().Cookies()
	loginCSRF := findCookie(t, loginCookies, "llm_proxy_csrf").Value

	password := request(first, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		loginCookies, loginCSRF)
	if password.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", password.Code, password.Body.String())
	}
	authenticatedCookies := password.Result().Cookies()
	authenticatedCSRF := findCookie(t, authenticatedCookies, "llm_proxy_csrf").Value

	profileBody := fmt.Sprintf(
		`{"slug":"persisted","display_name":"Persisted","enabled":true,"config":{"version":1,"protocol":"anthropic","upstream":%q,"vision":{"enabled":false,"model":"sonnet","max_tokens":2048,"timeout":"2m","max_concurrency":4,"cache_ttl":"30m","cache_max_entries":512},"overload_rules":[]},"make_default":true}`,
		upstream.URL,
	)
	created := request(first, http.MethodPost, "/_admin/api/profiles",
		profileBody, authenticatedCookies, authenticatedCSRF)
	if created.Code != http.StatusCreated {
		t.Fatalf("create profile status=%d body=%s", created.Code, created.Body.String())
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstClosed = true

	second, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	oldPassword := request(second, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if oldPassword.Code != http.StatusUnauthorized {
		t.Fatalf("old password status=%d body=%s", oldPassword.Code, oldPassword.Body.String())
	}
	newPassword := request(second, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"strong-admin-password"}`, nil, "")
	if newPassword.Code != http.StatusOK {
		t.Fatalf("new password status=%d body=%s", newPassword.Code, newPassword.Body.String())
	}

	proxyResponse := request(second, http.MethodPost, "/v1/messages", `{}`, nil, "")
	if proxyResponse.Code != http.StatusOK ||
		!strings.Contains(proxyResponse.Body.String(), `"profile":"persisted"`) {
		t.Fatalf(
			"persisted profile status=%d body=%q",
			proxyResponse.Code,
			proxyResponse.Body.String(),
		)
	}
}

// Break caught: closing the shared database before accepted usage writes drain, or accepting requests after shutdown starts.
func TestCloseStopsRequestsThenDrainsUsageBeforeClosingDatabase(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	parserEntered := make(chan struct{})
	parserRelease := make(chan struct{})
	parserReleased := false
	defer func() {
		if !parserReleased {
			close(parserRelease)
		}
	}()
	application.usage.RecordAsync(stats.RequestMeta{
		ProfileSlug: "closing",
		Protocol:    "anthropic",
		Kind:        "main",
		Path:        "/v1/messages",
	}, nil, blockingParser{entered: parserEntered, release: parserRelease})
	waitForSignal(t, parserEntered, "usage parser")

	usageCloseStarted := make(chan struct{})
	databaseCloseStarted := make(chan struct{})
	closeUsage := application.closeUsage
	closeDatabase := application.closeDatabase
	application.closeUsage = func() error {
		close(usageCloseStarted)
		return closeUsage()
	}
	application.closeDatabase = func() error {
		close(databaseCloseStarted)
		return closeDatabase()
	}

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- application.Close()
	}()
	waitForSignal(t, usageCloseStarted, "usage drain")

	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("request accepted during shutdown: status=%d body=%s", login.Code, login.Body.String())
	}
	select {
	case <-databaseCloseStarted:
		t.Fatal("database close started before usage drain completed")
	default:
	}
	if err := application.db.Ping(); err != nil {
		t.Fatalf("database closed while usage drain was blocked: %v", err)
	}
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before usage drain completed: %v", err)
	default:
	}

	close(parserRelease)
	parserReleased = true
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after usage drain completed")
	}
	waitForSignal(t, databaseCloseStarted, "database close")
	if err := application.db.Ping(); err == nil {
		t.Fatal("database remained open after application close")
	}
}

type blockingParser struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (p blockingParser) Parse([]byte) (stats.Usage, bool) {
	close(p.entered)
	<-p.release
	return stats.Usage{Model: "sonnet", InputTokens: 1, OutputTokens: 2}, true
}

func request(
	application *App,
	method, target, body string,
	cookies []*http.Cookie,
	csrf string,
) *httptest.ResponseRecorder {
	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, requestBody)
	req.RemoteAddr = "192.0.2.10:1234"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res := httptest.NewRecorder()
	application.Handler().ServeHTTP(res, req)
	return res
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func rawRequest(
	application *App,
	requestURI string,
	path string,
	rawPath string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	req.RequestURI = requestURI
	req.URL.Path = path
	req.URL.RawPath = rawPath
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, req)
	return response
}

func assertAdminPathSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
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

func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not start", name)
	}
}
