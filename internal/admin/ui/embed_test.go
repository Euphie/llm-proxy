package adminui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const expectedCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// Break caught: serving only the root document, omitting an embedded asset, or using an incorrect MIME type.
func TestHandlerServesSPAAndAssets(t *testing.T) {
	handler := NewHandler()
	for _, tc := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/_admin/", "text/html", `<main id="app"`},
		{"/_admin/profiles", "text/html", `<main id="app"`},
		{"/_admin/help/overview", "text/html", `<main id="app"`},
		{"/_admin/help/glossary", "text/html", `<main id="app"`},
		{"/_admin/help/intelligent-routing", "text/html", `<main id="app"`},
		{"/_admin/assets/current/app.js", "text/javascript", "bootstrap"},
		{"/_admin/assets/current/api.js", "text/javascript", "export const api"},
		{"/_admin/assets/current/routing-guide.js", "text/javascript", "renderHelpPage"},
		{"/_admin/assets/current/styles.css", "text/css", ":root"},
		{"/_admin/assets/current/intelligent-routing-architecture.svg", "image/svg+xml", "智能路由整体架构"},
		{"/_admin/assets/current/intelligent-routing-flow.svg", "image/svg+xml", "首次请求与后续追问"},
		{"/_admin/assets/current/intelligent-routing-learning-flow.svg", "image/svg+xml", "异步评测与贝叶斯学习闭环"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			response := serveUI(handler, http.MethodGet, tc.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, tc.contentType) {
				t.Fatalf("Content-Type=%q, want it to contain %q", contentType, tc.contentType)
			}
			if !strings.Contains(response.Body.String(), tc.contains) {
				t.Fatalf("body does not contain %q", tc.contains)
			}
		})
	}
}

// Break caught: turning a missing static asset into an HTML success or returning 404 for a valid SPA route.
func TestHandlerSeparatesMissingAssetsFromSPAFallback(t *testing.T) {
	handler := NewHandler()

	for _, path := range []string{
		"/_admin/assets",
		"/_admin/assets/missing.js",
		"/_admin/assets/current/missing.js",
	} {
		missing := serveUI(handler, http.MethodGet, path)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%q", path, missing.Code, missing.Body.String())
		}
		if strings.Contains(missing.Body.String(), `<main id="app"`) {
			t.Fatalf("%s returned the SPA document", path)
		}
	}

	fallback := serveUI(handler, http.MethodGet, "/_admin/profiles/42/edit")
	if fallback.Code != http.StatusOK {
		t.Fatalf("SPA fallback status=%d body=%q", fallback.Code, fallback.Body.String())
	}
	if !strings.Contains(fallback.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(fallback.Body.String(), `<main id="app"`) {
		t.Fatalf(
			"SPA fallback type=%q body=%q",
			fallback.Header().Get("Content-Type"),
			fallback.Body.String(),
		)
	}
}

// Break caught: caching unversioned UI files or omitting the API-equivalent browser security policy.
func TestHandlerAppliesSecurityAndCachePolicy(t *testing.T) {
	handler := NewHandler()
	for _, path := range []string{
		"/_admin/",
		"/_admin/profiles",
		"/_admin/assets/current/app.js",
		"/_admin/assets/current/styles.css",
	} {
		t.Run(path, func(t *testing.T) {
			response := serveUI(handler, http.MethodGet, path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			assertUIHeaders(t, response, "no-store")
		})
	}
}

// Break caught: allowing state-changing or HEAD requests to reach embedded files or SPA fallback.
func TestHandlerRejectsNonGETMethods(t *testing.T) {
	handler := NewHandler()
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/_admin/"},
		{method: http.MethodPut, path: "/_admin/profiles"},
		{method: http.MethodDelete, path: "/_admin/assets/current/app.js"},
		{method: http.MethodHead, path: "/_admin/assets/current/styles.css"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := serveUI(handler, tc.method, tc.path)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if allow := response.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("Allow=%q, want %q", allow, http.MethodGet)
			}
			assertUIHeaders(t, response, "no-store")
		})
	}
}

// Break caught: adding inline executable/style content or loading the shell from an external origin.
func TestHandlerShellUsesOnlyLocalExternalAssets(t *testing.T) {
	response := serveUI(NewHandler(), http.MethodGet, "/_admin/")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{
		`<link rel="stylesheet" href="/_admin/assets/current/styles.css">`,
		`<script type="module" src="/_admin/assets/current/app.js"></script>`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("shell does not contain %q", required)
		}
	}
	for _, forbidden := range []string{"<style", "<script>", "http://", "https://", "//cdn"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("shell contains forbidden inline or external marker %q", forbidden)
		}
	}
}

func serveUI(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func assertUIHeaders(t *testing.T, response *httptest.ResponseRecorder, cacheControl string) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": expectedCSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           cacheControl,
	}
	for name, value := range want {
		if got := response.Header().Get(name); got != value {
			t.Fatalf("%s=%q, want %q", name, got, value)
		}
	}
}
