package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestRouterSelectsDefaultAndNamedProfiles(t *testing.T) {
	registry := NewRegistry()
	builder := func(record profile.Record) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, record.Slug+" "+r.RequestURI)
		}), nil
	}
	records := []profile.Record{
		testRecord(1, "default", true),
		testRecord(2, "coding", true),
	}
	if err := registry.Load(records, 1, builder); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(registry)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/v1?x=1", "default /v1?x=1"},
		{"/v1/messages?x=1", "default /v1/messages?x=1"},
		{"/coding/v1/messages?x=1", "coding /v1/messages?x=1"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != http.StatusOK || res.Body.String() != tc.want {
				t.Fatalf("status=%d body=%q, want status=200 body=%q", res.Code, res.Body.String(), tc.want)
			}
		})
	}
}

func TestRouterErrors(t *testing.T) {
	t.Run("proxy not configured", func(t *testing.T) {
		router := NewRouter(NewRegistry())
		for _, path := range []string{"/v1/messages", "/coding/v1/messages"} {
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
			assertJSONError(t, res, http.StatusServiceUnavailable, "proxy_not_configured", "Proxy not configured")
		}
	})

	t.Run("Profile not found", func(t *testing.T) {
		registry := NewRegistry()
		record := testRecord(1, "default", true)
		if err := registry.Load([]profile.Record{record}, record.ID, responseBuilder(nil)); err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		NewRouter(registry).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/missing/v1/messages", nil))
		assertJSONError(t, res, http.StatusNotFound, "profile_not_found", "Profile not found")
	})

	t.Run("Profile disabled", func(t *testing.T) {
		registry := NewRegistry()
		record := testRecord(1, "paused", false)
		if err := registry.Load([]profile.Record{record}, record.ID, responseBuilder(nil)); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/v1/messages", "/paused/v1/messages"} {
			res := httptest.NewRecorder()
			NewRouter(registry).ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
			assertJSONError(t, res, http.StatusServiceUnavailable, "profile_disabled", "Profile disabled")
		}
	})
}

func TestRouterDoesNotConsumeAdminPaths(t *testing.T) {
	registry := NewRegistry()
	admin := testRecord(1, "_admin", true)
	if err := registry.Load([]profile.Record{admin}, admin.ID, responseBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(registry)

	for _, path := range []string{"/_admin", "/_admin/", "/_admin/profiles?x=1"} {
		t.Run(path, func(t *testing.T) {
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
			if res.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", res.Code)
			}
		})
	}
}

func TestRouterPreservesEscapedPathAndRawQuery(t *testing.T) {
	registry := NewRegistry()
	defaultRecord := testRecord(1, "default", true)
	record := testRecord(2, "coding", true)
	var gotPath, gotRawPath, gotRequestURI, gotRawQuery string
	if err := registry.Load([]profile.Record{defaultRecord, record}, defaultRecord.ID, func(got profile.Record) (http.Handler, error) {
		if got.ID == defaultRecord.ID {
			return responseHandler("default"), nil
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotRawPath = r.URL.RawPath
			gotRequestURI = r.RequestURI
			gotRawQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/coding/v1%2Fmessages?x=%2B&x=a%20b&empty=",
		nil,
	)
	originalPath := req.URL.Path
	originalRawPath := req.URL.RawPath
	originalRequestURI := req.RequestURI

	res := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("Path = %q, want %q", gotPath, "/v1/messages")
	}
	if gotRawPath != "/v1%2Fmessages" {
		t.Fatalf("RawPath = %q, want %q", gotRawPath, "/v1%2Fmessages")
	}
	if gotRequestURI != "/v1%2Fmessages?x=%2B&x=a%20b&empty=" {
		t.Fatalf("RequestURI = %q", gotRequestURI)
	}
	if gotRawQuery != "x=%2B&x=a%20b&empty=" {
		t.Fatalf("RawQuery = %q", gotRawQuery)
	}
	if req.URL.Path != originalPath || req.URL.RawPath != originalRawPath || req.RequestURI != originalRequestURI {
		t.Fatal("router mutated the original request")
	}
}

func TestRouterRejectsEncodedSlashInFirstSegment(t *testing.T) {
	registry := NewRegistry()
	defaultRecord := testRecord(1, "default", true)
	codingRecord := testRecord(2, "coding", true)
	called := false
	if err := registry.Load([]profile.Record{defaultRecord, codingRecord}, defaultRecord.ID, func(profile.Record) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}), nil
	}); err != nil {
		t.Fatal(err)
	}

	router := NewRouter(registry)
	for _, path := range []string{"/coding%2Fv1/messages", "/v1%2Fmessages"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
		assertJSONError(t, res, http.StatusNotFound, "profile_not_found", "Profile not found")
	}
	if called {
		t.Fatal("encoded slash in the first segment reached a Profile handler")
	}
}

func TestRouterAcceptsEncodedSlugWithCanonicalRemainder(t *testing.T) {
	registry := NewRegistry()
	defaultRecord := testRecord(1, "default", true)
	codingRecord := testRecord(2, "coding", true)
	var gotPath, gotRawPath, gotRequestURI string
	if err := registry.Load([]profile.Record{defaultRecord, codingRecord}, defaultRecord.ID, func(record profile.Record) (http.Handler, error) {
		if record.ID == defaultRecord.ID {
			return responseHandler("default"), nil
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotRawPath = r.URL.RawPath
			gotRequestURI = r.RequestURI
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/%63oding/v1/messages?x=%2B", nil)
	originalPath := req.URL.Path
	originalRawPath := req.URL.RawPath
	originalRequestURI := req.RequestURI

	res := httptest.NewRecorder()
	NewRouter(registry).ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if gotPath != "/v1/messages" || gotRawPath != "" {
		t.Fatalf("Path=%q RawPath=%q", gotPath, gotRawPath)
	}
	if gotRequestURI != "/v1/messages?x=%2B" {
		t.Fatalf("RequestURI = %q", gotRequestURI)
	}
	if req.URL.Path != originalPath || req.URL.RawPath != originalRawPath || req.RequestURI != originalRequestURI {
		t.Fatal("router mutated the original request")
	}
}

func assertJSONError(t *testing.T, res *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if res.Code != status {
		t.Fatalf("status = %d, want %d", res.Code, status)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%q", err, res.Body.String())
	}
	if body.Error.Code != code || body.Error.Message != message {
		t.Fatalf("error = %#v, want code=%q message=%q", body.Error, code, message)
	}
}
