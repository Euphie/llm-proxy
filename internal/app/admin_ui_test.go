package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/admin"
)

func TestAdminUIAssetsAreServedByApplication(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	assets := map[string]string{
		"/_admin/":                    "text/html; charset=utf-8",
		"/_admin/assets/api.js":       "text/javascript; charset=utf-8",
		"/_admin/assets/app.js":       "text/javascript; charset=utf-8",
		"/_admin/assets/auth.js":      "text/javascript; charset=utf-8",
		"/_admin/assets/profiles.js":  "text/javascript; charset=utf-8",
		"/_admin/assets/generator.js": "text/javascript; charset=utf-8",
		"/_admin/assets/stats.js":     "text/javascript; charset=utf-8",
		"/_admin/assets/system.js":    "text/javascript; charset=utf-8",
		"/_admin/assets/styles.css":   "text/css; charset=utf-8",
	}
	for path, contentType := range assets {
		t.Run(path, func(t *testing.T) {
			response := request(application, http.MethodGet, path, "", nil, "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != contentType {
				t.Fatalf("Content-Type=%q, want %q", got, contentType)
			}
			if response.Body.Len() == 0 {
				t.Fatal("empty response body")
			}
		})
	}
}

func TestAdminUIExactRootUsesSPAContract(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	exactRoot := request(application, http.MethodGet, "/_admin", "", nil, "")
	if exactRoot.Code != http.StatusOK ||
		!strings.Contains(exactRoot.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(exactRoot.Body.String(), `<main id="app"`) {
		t.Fatalf(
			"GET exact root status=%d type=%q body=%q",
			exactRoot.Code,
			exactRoot.Header().Get("Content-Type"),
			exactRoot.Body.String(),
		)
	}
	if location := exactRoot.Header().Get("Location"); location != "" {
		t.Fatalf("GET exact root Location=%q", location)
	}
	assertAdminPathSecurityHeaders(t, exactRoot)

	wrongMethod := request(application, http.MethodPost, "/_admin", "", nil, "")
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf(
			"POST exact root status=%d body=%q",
			wrongMethod.Code,
			wrongMethod.Body.String(),
		)
	}
	if allow := wrongMethod.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("POST exact root Allow=%q, want %q", allow, http.MethodGet)
	}
	assertAdminPathSecurityHeaders(t, wrongMethod)
}

func TestRestartPersistenceManagementLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	primaryUpstream := newLifecycleUpstream(t, "primary", "model-primary", 3, 4)
	secondaryUpstream := newLifecycleUpstream(t, "secondary", "model-secondary", 12, 34)

	first, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first application: %v", err)
		}
	})

	initialLogin := request(first, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	requireLifecycleStatus(t, initialLogin, http.StatusOK)
	preRotationCookies := initialLogin.Result().Cookies()
	preRotationSession := findCookie(t, preRotationCookies, admin.SessionCookieName).Value
	initialCSRF := findCookie(t, preRotationCookies, admin.CSRFCookieName).Value

	passwordChange := request(
		first,
		http.MethodPost,
		"/_admin/api/password",
		`{"current_password":"admin","new_password":"rotated-admin-password"}`,
		preRotationCookies,
		initialCSRF,
	)
	requireLifecycleStatus(t, passwordChange, http.StatusOK)
	activeCookies := passwordChange.Result().Cookies()
	activeSession := findCookie(t, activeCookies, admin.SessionCookieName).Value
	activeCSRF := findCookie(t, activeCookies, admin.CSRFCookieName).Value
	requireLifecycleSessionRotation(t, preRotationSession, activeSession)

	staleBeforeRestart := request(
		first,
		http.MethodGet,
		"/_admin/api/session",
		"",
		preRotationCookies,
		"",
	)
	requireLifecycleStatus(t, staleBeforeRestart, http.StatusUnauthorized)

	freshBeforeRestart := request(
		first,
		http.MethodGet,
		"/_admin/api/session",
		"",
		activeCookies,
		"",
	)
	requireLifecycleStatus(t, freshBeforeRestart, http.StatusOK)

	primary := createLifecycleProfile(
		t,
		first,
		activeCookies,
		activeCSRF,
		"primary",
		"Primary",
		primaryUpstream.URL,
		true,
	)
	secondary := createLifecycleProfile(
		t,
		first,
		activeCookies,
		activeCSRF,
		"secondary",
		"Secondary",
		secondaryUpstream.URL,
		false,
	)
	if primary.ID == secondary.ID {
		t.Fatalf("Profile IDs were not distinct: %d", primary.ID)
	}

	defaultChange := request(
		first,
		http.MethodPut,
		"/_admin/api/default-profile",
		fmt.Sprintf(`{"profile_id":%d}`, secondary.ID),
		activeCookies,
		activeCSRF,
	)
	requireLifecycleStatus(t, defaultChange, http.StatusOK)

	proxied := request(
		first,
		http.MethodPost,
		"/v1/messages",
		`{"model":"client-model","messages":[]}`,
		nil,
		"",
	)
	requireLifecycleStatus(t, proxied, http.StatusOK)
	if !strings.Contains(proxied.Body.String(), `"upstream":"secondary"`) {
		t.Fatalf("default upstream body=%s", proxied.Body.String())
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second application: %v", err)
		}
	})

	oldPassword := request(second, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	requireLifecycleStatus(t, oldPassword, http.StatusUnauthorized)

	staleSession := request(
		second,
		http.MethodGet,
		"/_admin/api/session",
		"",
		preRotationCookies,
		"",
	)
	requireLifecycleStatus(t, staleSession, http.StatusUnauthorized)

	newLogin := request(second, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"rotated-admin-password"}`, nil, "")
	requireLifecycleStatus(t, newLogin, http.StatusOK)
	restartedCookies := newLogin.Result().Cookies()

	profilesResponse := request(
		second,
		http.MethodGet,
		"/_admin/api/profiles",
		"",
		restartedCookies,
		"",
	)
	requireLifecycleStatus(t, profilesResponse, http.StatusOK)
	var profiles struct {
		DefaultProfileID int64 `json:"default_profile_id"`
		Profiles         []struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		} `json:"profiles"`
	}
	decodeLifecycleJSON(t, profilesResponse, &profiles)
	if profiles.DefaultProfileID != secondary.ID || len(profiles.Profiles) != 2 {
		t.Fatalf("Profiles=%+v", profiles)
	}
	if profiles.Profiles[0].Slug != "primary" ||
		profiles.Profiles[1].Slug != "secondary" {
		t.Fatalf("Profile order/content=%+v", profiles.Profiles)
	}

	statsResponse := request(
		second,
		http.MethodGet,
		"/_admin/api/stats?kind=main&model=model-secondary",
		"",
		restartedCookies,
		"",
	)
	requireLifecycleStatus(t, statsResponse, http.StatusOK)
	var usage struct {
		Summary struct {
			Requests     int `json:"requests"`
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"summary"`
	}
	decodeLifecycleJSON(t, statsResponse, &usage)
	if usage.Summary.Requests != 1 ||
		usage.Summary.InputTokens != 12 ||
		usage.Summary.OutputTokens != 34 ||
		usage.Summary.TotalTokens != 46 {
		t.Fatalf("historical usage=%+v", usage.Summary)
	}

	restartedProxy := request(
		second,
		http.MethodPost,
		"/v1/messages",
		`{"model":"client-model","messages":[]}`,
		nil,
		"",
	)
	requireLifecycleStatus(t, restartedProxy, http.StatusOK)
	if !strings.Contains(restartedProxy.Body.String(), `"upstream":"secondary"`) {
		t.Fatalf("restarted default upstream body=%s", restartedProxy.Body.String())
	}
}

type lifecycleProfile struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

func createLifecycleProfile(
	t *testing.T,
	application *App,
	cookies []*http.Cookie,
	csrf, slug, displayName, upstream string,
	makeDefault bool,
) lifecycleProfile {
	t.Helper()
	body := fmt.Sprintf(`{
		"slug": %q,
		"display_name": %q,
		"enabled": true,
		"make_default": %t,
		"config": {
			"version": 1,
			"protocol": "anthropic",
			"upstream": %q,
			"vision": {
				"enabled": false,
				"model": "sonnet",
				"max_tokens": 2048,
				"timeout": "2m",
				"max_concurrency": 4,
				"cache_ttl": "30m",
				"cache_max_entries": 512,
				"prompt": ""
			},
			"overload_rules": []
		}
	}`, slug, displayName, makeDefault, upstream)
	response := request(
		application,
		http.MethodPost,
		"/_admin/api/profiles",
		body,
		cookies,
		csrf,
	)
	requireLifecycleStatus(t, response, http.StatusCreated)
	var profile lifecycleProfile
	decodeLifecycleJSON(t, response, &profile)
	return profile
}

func newLifecycleUpstream(
	t *testing.T,
	name, model string,
	inputTokens, outputTokens int,
) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"upstream":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d}}`,
			name,
			model,
			inputTokens,
			outputTokens,
		)
	}))
	t.Cleanup(server.Close)
	return server
}

func requireLifecycleStatus(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
) {
	t.Helper()
	if response.Code != status {
		t.Fatalf(
			"status=%d, want %d; body=%s",
			response.Code,
			status,
			response.Body.String(),
		)
	}
}

func requireLifecycleSessionRotation(t *testing.T, before, after string) {
	t.Helper()
	if before == after {
		t.Fatal("password change did not rotate the Session token")
	}
}

func decodeLifecycleJSON(
	t *testing.T,
	response *httptest.ResponseRecorder,
	target any,
) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, response.Body.String())
	}
}
