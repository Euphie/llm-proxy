package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/stats"
)

const (
	visionImageBody    = `{"model":"main-model","max_tokens":256,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"What is shown?"}]}]}`
	visionTextBody     = `{"model":"main-model","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`
	responsesImageBody = `{"model":"main-model","stream":true,"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="},{"type":"input_text","text":"What is shown?"}]}]}`
)

type visionUpstream struct {
	server      *httptest.Server
	visionCalls atomic.Int32
	mainCalls   atomic.Int32
	mainBodies  chan []byte
	mainURIs    chan string
}

func TestProxyPreservesEscapedPathQueryAndFiltersHopHeaders(t *testing.T) {
	var gotURI string
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		gotHeader = r.Header.Clone()
		w.Header().Set("Proxy-Authenticate", "secret")
		w.Header().Set("X-Response-Keep", "value")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	runtime := resolvedRuntime(t, 4, "coding", profile.ProtocolAnthropic, upstream.URL+"/base")
	handler := New(runtime, upstream.Client(), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/files/a%2Fb?download=1", nil)
	req.Header.Set("Connection", "keep-alive, X-Remove")
	req.Header.Set("X-Remove", "secret")
	req.Header.Set("X-Keep", "value")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if gotURI != "/base/v1/files/a%2Fb?download=1" {
		t.Fatalf("requestURI=%q", gotURI)
	}
	if gotHeader.Get("Connection") != "" || gotHeader.Get("X-Remove") != "" {
		t.Fatalf("hop headers=%v", gotHeader)
	}
	if gotHeader.Get("X-Keep") != "value" {
		t.Fatalf("end-to-end header=%q", gotHeader.Get("X-Keep"))
	}
	if res.Header().Get("Connection") != "" || res.Header().Get("Proxy-Authenticate") != "" {
		t.Fatalf("response hop headers=%v", res.Header())
	}
	if res.Header().Get("X-Response-Keep") != "value" {
		t.Fatalf("response end-to-end header=%q", res.Header().Get("X-Response-Keep"))
	}
}

// Break caught: following an upstream redirect can replay a caller's request to another Profile path.
func TestProxyReturnsRedirectWithoutCallingOtherProfile(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var redirectTargetCalls atomic.Int32
			var redirectTargetHeaders http.Header
			var redirectTargetBody []byte
			var upstream *httptest.Server
			upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/other-profile/v1/messages" {
					redirectTargetCalls.Add(1)
					redirectTargetHeaders = r.Header.Clone()
					redirectTargetBody, _ = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Location", upstream.URL+"/other-profile/v1/messages")
				w.WriteHeader(status)
			}))
			defer upstream.Close()

			handler := New(
				resolvedRuntime(t, 4, "coding", profile.ProtocolAnthropic, upstream.URL),
				upstream.Client(),
				nil,
			)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("caller body"))
			request.Header.Set("X-Api-Key", "caller-api-key")
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set("Cookie", "session=caller-cookie")
			request.Header.Set("X-Caller-Metadata", "caller-metadata")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != status {
				t.Fatalf("status=%d, want %d", response.Code, status)
			}
			if response.Header().Get("Location") != upstream.URL+"/other-profile/v1/messages" {
				t.Fatalf("Location=%q", response.Header().Get("Location"))
			}
			if redirectTargetCalls.Load() != 0 {
				t.Fatalf("other Profile redirect target calls=%d headers=%v body=%q",
					redirectTargetCalls.Load(), redirectTargetHeaders, redirectTargetBody)
			}
		})
	}
}

func TestProxyRecordsProfileMainUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"model":"claude-sonnet",
			"usage":{"input_tokens":11,"output_tokens":22}
		}`)
	}))
	defer upstream.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (4, 'coding', 'Coding', 1, '{}', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}

	handler := New(
		resolvedRuntime(t, 4, "coding", profile.ProtocolAnthropic, upstream.URL),
		upstream.Client(),
		stats.New(db),
	)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", res.Code, res.Body.String())
	}

	var profileID sql.NullInt64
	var slug, protocol, kind, path string
	deadline := time.Now().Add(time.Second)
	for {
		err = db.QueryRow(`
			SELECT profile_id, profile_slug, protocol, request_kind, path
			FROM usage ORDER BY id DESC LIMIT 1
		`).Scan(&profileID, &slug, &protocol, &kind, &path)
		if err == nil || !errors.Is(err, sql.ErrNoRows) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("main usage row not recorded: %v", err)
	}
	if !profileID.Valid || profileID.Int64 != 4 ||
		slug != "coding" || protocol != "anthropic" ||
		kind != "main" || path != "/v1/messages" {
		t.Fatalf("usage=%v %q %q %q %q", profileID, slug, protocol, kind, path)
	}
}

func resolvedRuntime(
	t *testing.T,
	id int64,
	slug string,
	protocol profile.Protocol,
	upstream string,
) profile.Runtime {
	t.Helper()
	record := profile.Record{
		ID:          id,
		Slug:        slug,
		DisplayName: strings.ToUpper(slug[:1]) + slug[1:],
		Enabled:     true,
		Config:      profile.NewConfig(protocol, upstream),
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newVisionUpstream(
	t *testing.T,
	respondToVision func(http.ResponseWriter, *http.Request),
) *visionUpstream {
	return newVisionUpstreamWithResponders(t, respondToVision, nil)
}

func newVisionUpstreamWithResponders(
	t *testing.T,
	respondToVision func(http.ResponseWriter, *http.Request),
	respondToMain func(http.ResponseWriter, *http.Request),
) *visionUpstream {
	t.Helper()

	upstream := &visionUpstream{
		mainBodies: make(chan []byte, 1),
		mainURIs:   make(chan string, 1),
	}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusInternalServerError)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		switch request.Model {
		case "sonnet":
			upstream.visionCalls.Add(1)
			if respondToVision != nil {
				respondToVision(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
			  "model":"sonnet",
			  "content":[{"type":"text","text":"screen description"}],
			  "usage":{"input_tokens":10,"output_tokens":20}
			}`)
		case "main-model", "native-model", "unknown-model":
			upstream.mainCalls.Add(1)
			upstream.mainBodies <- append([]byte(nil), body...)
			upstream.mainURIs <- r.RequestURI
			if respondToMain != nil {
				respondToMain(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
			  "model":"main-model",
			  "content":[{"type":"text","text":"done"}],
			  "usage":{"input_tokens":30,"output_tokens":40}
			}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *visionUpstream) proxy(enabled bool) http.Handler {
	return New(profile.Runtime{
		Slug:     "test",
		Protocol: profile.ProtocolAnthropic,
		Upstream: u.server.URL,
		Models: profile.ModelCatalog{
			"main-model": {ID: "main-model", SupportsVision: false},
		},
		OverloadRules: []provider.Rule{{
			Status:     http.StatusServiceUnavailable,
			MaxRetries: 0,
		}},
		Vision: profile.VisionRuntime{
			Enabled:             enabled,
			Model:               "sonnet",
			UnlistedModelPolicy: profile.UnlistedModelBypass,
			MaxTokens:           2048,
			Timeout:             2 * time.Second,
			MaxConcurrency:      4,
			CacheTTL:            30 * time.Minute,
			CacheMaxEntries:     512,
		},
	}, u.server.Client(), nil)
}

func TestVisionEnabledRewritesImageBeforeMainRequest(t *testing.T) {
	upstream := newVisionUpstream(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()

	upstream.proxy(true).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
	}
	if got := upstream.visionCalls.Load(); got != 1 {
		t.Fatalf("vision calls=%d, want 1", got)
	}
	if got := upstream.mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}

	mainBody := <-upstream.mainBodies
	var rewritten struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mainBody, &rewritten); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}
	if len(rewritten.Messages) != 1 || len(rewritten.Messages[0].Content) != 2 {
		t.Fatalf("unexpected rewritten messages: %s", mainBody)
	}
	if got := rewritten.Messages[0].Content[0].Type; got != "text" {
		t.Fatalf("rewritten image type=%q, want text; body=%s", got, mainBody)
	}
	if got := rewritten.Messages[0].Content[0].Text; !strings.Contains(got, "screen description") {
		t.Fatalf("rewritten description=%q", got)
	}
	if bytes.Contains(mainBody, []byte(`"type":"image"`)) {
		t.Fatalf("main body still contains image block: %s", mainBody)
	}
}

func TestExplicitVisionLogsOpaqueRequestAndOwnerTrace(t *testing.T) {
	visionHeaders := make(chan http.Header, 1)
	upstream := newVisionUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		visionHeaders <- r.Header.Clone()
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"screen description"}]}`)
	})
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	upstream.proxy(true).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}

	var cacheRecord map[string]any
	var attemptRecord map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		switch record["msg"] {
		case "vision.image.cache":
			cacheRecord = record
		case "vision.shadow.attempt":
			attemptRecord = record
		}
	}
	requestTrace, _ := cacheRecord["request_trace_id"].(string)
	ownerTrace, _ := cacheRecord["owner_trace_id"].(string)
	callID, _ := cacheRecord["call_id"].(string)
	if requestTrace == "" || ownerTrace != requestTrace || callID == "" {
		t.Fatalf("cache trace metadata=%v", cacheRecord)
	}
	if attemptRecord["request_trace_id"] != requestTrace ||
		attemptRecord["owner_trace_id"] != ownerTrace || attemptRecord["call_id"] != callID {
		t.Fatalf("attempt trace metadata=%v, cache=%v", attemptRecord, cacheRecord)
	}
	for name, values := range <-visionHeaders {
		for _, value := range values {
			if strings.Contains(name, "Trace") || strings.Contains(value, requestTrace) ||
				strings.Contains(value, callID) {
				t.Fatalf("vision request forwarded trace metadata: %s=%q", name, value)
			}
		}
	}
}

func TestVisionNativeAndUnlistedBypassReachMainUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		models profile.ModelCatalog
	}{
		{
			name:  "native vision",
			model: "native-model",
			models: profile.ModelCatalog{
				"native-model": {ID: "native-model", SupportsVision: true},
			},
		},
		{
			name:   "unlisted",
			model:  "unknown-model",
			models: profile.ModelCatalog{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newVisionUpstream(t, nil)
			body := []byte(strings.Replace(visionImageBody, `"main-model"`, `"`+test.model+`"`, 1))
			runtime := profile.Runtime{
				Slug:     "test",
				Protocol: profile.ProtocolAnthropic,
				Upstream: upstream.server.URL,
				Models:   test.models,
				Vision: profile.VisionRuntime{
					Enabled:             true,
					Model:               "sonnet",
					UnlistedModelPolicy: profile.UnlistedModelBypass,
					MaxTokens:           2048,
					Timeout:             2 * time.Second,
					MaxConcurrency:      4,
					CacheTTL:            30 * time.Minute,
					CacheMaxEntries:     512,
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			New(runtime, upstream.server.Client(), nil).ServeHTTP(response, request)

			if got := upstream.visionCalls.Load(); got != 0 {
				t.Fatalf("vision calls=%d, want 0", got)
			}
			if got := upstream.mainCalls.Load(); got != 1 {
				t.Fatalf("main calls=%d, want 1", got)
			}
			if got := <-upstream.mainBodies; !bytes.Equal(got, body) {
				t.Fatalf("main body changed:\n got: %s\nwant: %s", got, body)
			}
		})
	}
}

func TestNewSuppressesInvalidVisionPreprocessor(t *testing.T) {
	h := New(profile.Runtime{
		Protocol: profile.ProtocolAnthropic,
		Vision: profile.VisionRuntime{
			Enabled: true,
			Model:   " \t ",
		},
	}, http.DefaultClient, nil).(*handler)
	if h.vision != nil {
		t.Fatal("invalid vision configuration created a preprocessor")
	}
}

func TestVisionEnabledWithoutImageForwardsOriginalBody(t *testing.T) {
	upstream := newVisionUpstream(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionTextBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	upstream.proxy(true).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
	}
	if got := upstream.visionCalls.Load(); got != 0 {
		t.Fatalf("vision calls=%d, want 0", got)
	}
	if got := upstream.mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}
	if got := <-upstream.mainBodies; !bytes.Equal(got, []byte(visionTextBody)) {
		t.Fatalf("main body changed:\n got: %s\nwant: %s", got, visionTextBody)
	}
}

func TestVisionFailureReturnsBadGatewayWithoutMainRequest(t *testing.T) {
	upstream := newVisionUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "vision unavailable", http.StatusInternalServerError)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	upstream.proxy(true).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502; body=%q", response.Code, response.Body.String())
	}
	if got := upstream.visionCalls.Load(); got != 1 {
		t.Fatalf("vision calls=%d, want 1", got)
	}
	if got := upstream.mainCalls.Load(); got != 0 {
		t.Fatalf("main calls=%d, want 0", got)
	}
}

func TestVisionDisabledForwardsImageBodyUnchanged(t *testing.T) {
	upstream := newVisionUpstream(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	upstream.proxy(false).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
	}
	if got := upstream.visionCalls.Load(); got != 0 {
		t.Fatalf("vision calls=%d, want 0", got)
	}
	if got := upstream.mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}
	if got := <-upstream.mainBodies; !bytes.Equal(got, []byte(visionImageBody)) {
		t.Fatalf("main body changed:\n got: %s\nwant: %s", got, visionImageBody)
	}
}

func TestVisionSkipsIneligibleRequests(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
	}{
		{name: "non messages path", method: http.MethodPost, path: "/v1/complete", contentType: "application/json"},
		{name: "non POST method", method: http.MethodPut, path: "/v1/messages", contentType: "application/json"},
		{name: "non JSON content type", method: http.MethodPost, path: "/v1/messages", contentType: "text/plain"},
		{name: "invalid content type", method: http.MethodPost, path: "/v1/messages", contentType: `application/json; charset="`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newVisionUpstream(t, nil)
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(visionImageBody))
			request.Header.Set("Content-Type", tt.contentType)
			response := httptest.NewRecorder()

			upstream.proxy(true).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
			}
			if got := upstream.visionCalls.Load(); got != 0 {
				t.Fatalf("vision calls=%d, want 0", got)
			}
			if got := upstream.mainCalls.Load(); got != 1 {
				t.Fatalf("main calls=%d, want 1", got)
			}
			if got := <-upstream.mainBodies; !bytes.Equal(got, []byte(visionImageBody)) {
				t.Fatalf("main body changed:\n got: %s\nwant: %s", got, visionImageBody)
			}
		})
	}
}

func TestVisionSkipsEscapedMessagePaths(t *testing.T) {
	for _, path := range []string{"/v1%2Fmessages", "/v1/mess%61ges"} {
		t.Run(path, func(t *testing.T) {
			upstream := newVisionUpstream(t, nil)
			request := httptest.NewRequest(http.MethodPost, path+"?beta=1", strings.NewReader(visionImageBody))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			upstream.proxy(true).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
			}
			if got := upstream.visionCalls.Load(); got != 0 {
				t.Fatalf("vision calls=%d, want 0", got)
			}
			if got := upstream.mainCalls.Load(); got != 1 {
				t.Fatalf("main calls=%d, want 1", got)
			}
			if got := <-upstream.mainURIs; got != path+"?beta=1" {
				t.Fatalf("main RequestURI=%q, want %q", got, path+"?beta=1")
			}
			if got := <-upstream.mainBodies; !bytes.Equal(got, []byte(visionImageBody)) {
				t.Fatalf("main body changed:\n got: %s\nwant: %s", got, visionImageBody)
			}
		})
	}
}

func TestVisionSkipsOpenAIProfile(t *testing.T) {
	upstream := newVisionUpstream(t, nil)
	runtime := profile.Runtime{
		Slug:     "test",
		Protocol: profile.ProtocolOpenAI,
		Upstream: upstream.server.URL,
		Vision: profile.VisionRuntime{
			Enabled:         true,
			Model:           "sonnet",
			MaxTokens:       2048,
			Timeout:         2 * time.Second,
			MaxConcurrency:  4,
			CacheTTL:        30 * time.Minute,
			CacheMaxEntries: 512,
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, upstream.server.Client(), nil).ServeHTTP(response, request)

	if got := upstream.visionCalls.Load(); got != 0 {
		t.Fatalf("vision calls=%d, want 0", got)
	}
	if got := upstream.mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}
}

func TestOpenAIVisionRewritesResponsesImageBeforeMainRequest(t *testing.T) {
	var visionCalls atomic.Int32
	var mainCalls atomic.Int32
	mainBodies := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusInternalServerError)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch request.Model {
		case "vision-model":
			visionCalls.Add(1)
			_, _ = io.WriteString(w, `{
			  "model":"vision-model",
			  "output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"screen description","annotations":[]}]}],
			  "usage":{"input_tokens":10,"output_tokens":20}
			}`)
		case "main-model":
			mainCalls.Add(1)
			mainBodies <- append([]byte(nil), body...)
			_, _ = io.WriteString(w, `{
			  "model":"main-model",
			  "output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}]}],
			  "usage":{"input_tokens":30,"output_tokens":40}
			}`)
		default:
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	runtime := profile.Runtime{
		Slug:     "test",
		Protocol: profile.ProtocolOpenAI,
		Upstream: upstream.URL,
		Models: profile.ModelCatalog{
			"main-model": {ID: "main-model", SupportsVision: false},
		},
		Vision: profile.VisionRuntime{
			Enabled:             true,
			Transport:           profile.VisionTransportOpenAIResponses,
			Model:               "vision-model",
			UnlistedModelPolicy: profile.UnlistedModelBypass,
			MaxTokens:           512,
			Timeout:             2 * time.Second,
			MaxConcurrency:      4,
			CacheTTL:            30 * time.Minute,
			CacheMaxEntries:     512,
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesImageBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, upstream.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%q", response.Code, response.Body.String())
	}
	if got := visionCalls.Load(); got != 1 {
		t.Fatalf("vision calls=%d, want 1", got)
	}
	if got := mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}
	mainBody := <-mainBodies
	var rewritten struct {
		Stream bool `json:"stream"`
		Input  []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(mainBody, &rewritten); err != nil {
		t.Fatal(err)
	}
	if !rewritten.Stream {
		t.Fatal("stream setting was not preserved")
	}
	if rewritten.Input[0].Content[0].Type != "input_text" ||
		!strings.Contains(rewritten.Input[0].Content[0].Text, "screen description") {
		t.Fatalf("rewritten content=%+v", rewritten.Input[0].Content[0])
	}
}

func TestOpenAIVisionChatTransportKeepsResponsesMainRequest(t *testing.T) {
	var chatCalls atomic.Int32
	var responsesCalls atomic.Int32
	mainBodies := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
			if !bytes.Contains(body, []byte(`"type":"image_url"`)) {
				t.Errorf("chat body=%s", body)
			}
			_, _ = io.WriteString(w, `{
				"choices":[{"message":{"content":"chat description"}}],
				"usage":{"prompt_tokens":10,"completion_tokens":20}
			}`)
		case "/v1/responses":
			responsesCalls.Add(1)
			mainBodies <- body
			_, _ = io.WriteString(w, `{"output":[]}`)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	runtime := profile.Runtime{
		Slug:     "test",
		Protocol: profile.ProtocolOpenAI,
		Upstream: upstream.URL + "/v1",
		Models: profile.ModelCatalog{
			"main-model": {ID: "main-model", SupportsVision: false},
		},
		Vision: profile.VisionRuntime{
			Enabled:             true,
			Transport:           profile.VisionTransportOpenAIChatCompletions,
			Model:               "vision-model",
			UnlistedModelPolicy: profile.UnlistedModelBypass,
			MaxTokens:           512,
			Timeout:             2 * time.Second,
			MaxConcurrency:      4,
			CacheTTL:            30 * time.Minute,
			CacheMaxEntries:     512,
		},
	}
	body := `{
		"model":"main-model",
		"stream":true,
		"input":[
			{"role":"user","content":[
				{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="},
				{"type":"input_text","text":"What is shown?"}
			]},
			{"type":"function_call_output","call_id":"call-1","output":[
				{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2Uy"}
			]}
		]
	}`
	request := httptest.NewRequest(
		http.MethodPost,
		"/responses",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(runtime, upstream.Client(), nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if chatCalls.Load() != 2 || responsesCalls.Load() != 1 {
		t.Fatalf("chat=%d responses=%d", chatCalls.Load(), responsesCalls.Load())
	}
	mainBody := <-mainBodies
	if bytes.Contains(mainBody, []byte(`"type":"input_image"`)) ||
		!bytes.Contains(mainBody, []byte("chat description")) {
		t.Fatalf("main body=%s", mainBody)
	}
}

func TestOpenAIVisionNativeAndUnlistedBypassReachMainUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		models profile.ModelCatalog
	}{
		{
			name:  "native vision",
			model: "native-model",
			models: profile.ModelCatalog{
				"native-model": {ID: "native-model", SupportsVision: true},
			},
		},
		{
			name:   "unlisted",
			model:  "unknown-model",
			models: profile.ModelCatalog{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var visionCalls atomic.Int32
			var mainCalls atomic.Int32
			mainBodies := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read request", http.StatusInternalServerError)
					return
				}
				var request struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if request.Model == "vision-model" {
					visionCalls.Add(1)
				} else {
					mainCalls.Add(1)
					mainBodies <- append([]byte(nil), body...)
				}
				_, _ = io.WriteString(w, `{"output":[]}`)
			}))
			defer upstream.Close()

			body := []byte(strings.Replace(responsesImageBody, `"main-model"`, `"`+test.model+`"`, 1))
			runtime := profile.Runtime{
				Slug:     "test",
				Protocol: profile.ProtocolOpenAI,
				Upstream: upstream.URL,
				Models:   test.models,
				Vision: profile.VisionRuntime{
					Enabled:             true,
					Model:               "vision-model",
					UnlistedModelPolicy: profile.UnlistedModelBypass,
					MaxTokens:           512,
					Timeout:             2 * time.Second,
					MaxConcurrency:      4,
					CacheTTL:            30 * time.Minute,
					CacheMaxEntries:     512,
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			New(runtime, upstream.Client(), nil).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if got := visionCalls.Load(); got != 0 {
				t.Fatalf("vision calls=%d, want 0", got)
			}
			if got := mainCalls.Load(); got != 1 {
				t.Fatalf("main calls=%d, want 1", got)
			}
			if got := <-mainBodies; !bytes.Equal(got, body) {
				t.Fatalf("main body changed:\n got: %s\nwant: %s", got, body)
			}
		})
	}
}

func TestShouldPreprocessVisionMatchesProtocolEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		path     string
		want     bool
		wantOp   string
	}{
		{name: "anthropic messages", protocol: profile.ProtocolAnthropic, path: "/v1/messages", want: true, wantOp: "anthropic_messages"},
		{name: "anthropic responses", protocol: profile.ProtocolAnthropic, path: "/v1/responses", want: false},
		{name: "openai responses", protocol: profile.ProtocolOpenAI, path: "/v1/responses", want: true, wantOp: "openai_responses"},
		{name: "openai responses without v1", protocol: profile.ProtocolOpenAI, path: "/responses", want: true, wantOp: "openai_responses"},
		{name: "openai chat completions", protocol: profile.ProtocolOpenAI, path: "/v1/chat/completions", want: true, wantOp: "openai_chat_completions"},
		{name: "openai messages", protocol: profile.ProtocolOpenAI, path: "/v1/messages", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("Content-Type", "application/json")
			operation, got := requestVisionOperation(test.protocol, request)
			if got != test.want || string(operation) != test.wantOp {
				t.Fatalf("requestVisionOperation()=(%q,%v), want (%q,%v)", operation, got, test.wantOp, test.want)
			}
		})
	}
}

type trackingResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *trackingResponseWriter) Header() http.Header {
	return w.header
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *trackingResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func TestVisionRequestCancellationDoesNotWriteResponseOrCallMain(t *testing.T) {
	visionStarted := make(chan struct{})
	upstream := newVisionUpstream(t, func(_ http.ResponseWriter, r *http.Request) {
		close(visionStarted)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionImageBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := &trackingResponseWriter{header: make(http.Header)}
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		upstream.proxy(true).ServeHTTP(response, request)
	}()

	select {
	case <-visionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("vision request did not start")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not return after cancellation")
	}

	if got := upstream.mainCalls.Load(); got != 0 {
		t.Fatalf("main calls=%d, want 0", got)
	}
	if response.status != 0 || response.body.Len() != 0 {
		t.Fatalf("proxy wrote status=%d body=%q after cancellation", response.status, response.body.String())
	}
}

func TestVisionEnabledWithoutImagePreCanceledDoesNotWriteResponseOrCallMain(t *testing.T) {
	upstream := newVisionUpstream(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionTextBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := &trackingResponseWriter{header: make(http.Header)}

	upstream.proxy(true).ServeHTTP(response, request)

	if got := upstream.visionCalls.Load(); got != 0 {
		t.Fatalf("vision calls=%d, want 0", got)
	}
	if got := upstream.mainCalls.Load(); got != 0 {
		t.Fatalf("main calls=%d, want 0", got)
	}
	if response.status != 0 || response.body.Len() != 0 {
		t.Fatalf("proxy wrote status=%d body=%q after cancellation", response.status, response.body.String())
	}
}

func TestVisionMainRequestCancellationDoesNotWriteErrorResponse(t *testing.T) {
	mainStarted := make(chan struct{})
	upstream := newVisionUpstreamWithResponders(t, nil, func(_ http.ResponseWriter, r *http.Request) {
		close(mainStarted)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionTextBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := &trackingResponseWriter{header: make(http.Header)}
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		upstream.proxy(true).ServeHTTP(response, request)
	}()

	select {
	case <-mainStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("main request did not start")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not return after cancellation")
	}

	if got := upstream.mainCalls.Load(); got != 1 {
		t.Fatalf("main calls=%d, want 1", got)
	}
	if response.status != 0 || response.body.Len() != 0 {
		t.Fatalf("proxy wrote status=%d body=%q after cancellation", response.status, response.body.String())
	}
}

func TestMainOverloadBackoffCancellationDoesNotWriteResponse(t *testing.T) {
	consumed := make(chan struct{})
	transport := &overloadOnceTransport{consumed: consumed}
	handler := New(profile.Runtime{
		Upstream: "https://upstream.test",
		Slug:     "test",
		Protocol: profile.ProtocolAnthropic,
		OverloadRules: []provider.Rule{{
			Status:       http.StatusServiceUnavailable,
			BodyContains: "overloaded",
			MaxRetries:   2,
			RetryDelay:   time.Second,
		}},
	}, &http.Client{Transport: transport}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionTextBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := &trackingResponseWriter{header: make(http.Header)}
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		handler.ServeHTTP(response, request)
	}()

	select {
	case <-consumed:
	case <-time.After(time.Second):
		t.Fatal("proxy did not consume the overload response")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("proxy did not return after cancellation during backoff")
	}

	if transport.calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", transport.calls.Load())
	}
	if response.status != 0 || response.body.Len() != 0 {
		t.Fatalf("proxy wrote status=%d body=%q after cancellation", response.status, response.body.String())
	}
}

func TestEmptyOverloadRulesNetworkFailureReturnsBadGateway(t *testing.T) {
	transport := &networkFailureTransport{}
	handler := New(profile.Runtime{
		Upstream: "https://upstream.test",
		Slug:     "test",
		Protocol: profile.ProtocolAnthropic,
	}, &http.Client{Transport: transport}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(visionTextBody))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusBadGateway)
	}
	if transport.calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", transport.calls.Load())
	}
}

type networkFailureTransport struct {
	calls atomic.Int32
}

func (t *networkFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("network failure")
}

type overloadOnceTransport struct {
	calls    atomic.Int32
	consumed chan struct{}
}

func (t *overloadOnceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Header:     make(http.Header),
		Body: &signalCloseBody{
			Reader: strings.NewReader(`{"error":"overloaded"}`),
			closed: t.consumed,
		},
		Request: request,
	}, nil
}

type signalCloseBody struct {
	io.Reader
	closed chan struct{}
	once   sync.Once
}

func (b *signalCloseBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
