package vision

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/config"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/stats"
)

func TestVisionClientRequestHeadersResponseAndUsage(t *testing.T) {
	responseBody := []byte(`{
		"model":"sonnet",
		"content":[
			{"type":"text","text":"first"},
			{"type":"tool_use","name":"ignored"},
			{"type":"text","text":"second"}
		],
		"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":3}
	}`)
	var handlerErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			handlerErr = errors.New("wrong request path: " + r.URL.Path)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
			Messages  []struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			handlerErr = err
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Model != "sonnet" || request.MaxTokens != 2048 || request.Stream {
			handlerErr = errors.New("wrong shadow request")
		}
		if len(request.Messages) != 1 ||
			request.Messages[0].Role != "user" ||
			len(request.Messages[0].Content) != 2 {
			handlerErr = errors.New("wrong messages")
		}
		var textBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if len(request.Messages) == 1 && len(request.Messages[0].Content) == 2 {
			if !bytes.Equal(request.Messages[0].Content[0], testImage().block) {
				handlerErr = errors.New("image block was not preserved")
			}
			if err := json.Unmarshal(request.Messages[0].Content[1], &textBlock); err != nil ||
				textBlock.Type != "text" || textBlock.Text != defaultPrompt {
				handlerErr = errors.New("wrong prompt block")
			}
		}
		for name, want := range map[string]string{
			"Authorization":     "Bearer secret",
			"X-Api-Key":         "key",
			"Anthropic-Version": "2023-06-01",
		} {
			if got := r.Header.Get(name); got != want {
				handlerErr = errors.New("wrong allowed header: " + name)
			}
		}
		for _, name := range []string{"Anthropic-Beta", "Cookie", "Accept-Encoding"} {
			if got := r.Header.Get(name); got != "" {
				handlerErr = errors.New("copied disallowed header: " + name)
			}
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			handlerErr = errors.New("wrong content type: " + got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	client := newVisionClient(testVisionConfig(server.URL), server.Client(), nil)
	usage := make(chan []byte, 1)
	client.recordUsage = func(body []byte) { usage <- append([]byte(nil), body...) }
	headers := http.Header{
		"Authorization":     {"Bearer secret"},
		"X-Api-Key":         {"key"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"files-api-2025-04-14"},
		"Cookie":            {"must-not-copy"},
		"Accept-Encoding":   {"gzip"},
	}

	got, err := client.Describe(context.Background(), headers, testImage())
	if err != nil {
		t.Fatal(err)
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if got != "first\nsecond" {
		t.Fatalf("description=%q", got)
	}
	select {
	case recorded := <-usage:
		if !bytes.Equal(recorded, responseBody) {
			t.Fatalf("recorded usage body=%q", recorded)
		}
	default:
		t.Fatal("usage was not recorded")
	}
}

func TestVisionClientRecordsPrettyJSONUsageThroughStatsDB(t *testing.T) {
	responseBody := []byte(`{
	  "model": "sonnet",
	  "content": [{"type": "text", "text": "description"}],
	  "usage": {
	    "input_tokens": 10,
	    "output_tokens": 20,
	    "cache_read_input_tokens": 3,
	    "cache_creation_input_tokens": 4
	  }
	}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	dbPath := filepath.Join(t.TempDir(), "usage.db")
	sdb, err := stats.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	client := newVisionClient(testVisionConfig(server.URL), server.Client(), sdb)
	if _, err := client.Describe(context.Background(), nil, testImage()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	readDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()

	var providerName, model, path string
	var input, output, cacheRead, cacheCreation int
	err = readDB.QueryRow(`
		SELECT provider, model, path, input_tokens, output_tokens,
		       cache_read_tokens, cache_creation_tokens
		FROM usage ORDER BY id DESC LIMIT 1
	`).Scan(&providerName, &model, &path, &input, &output, &cacheRead, &cacheCreation)
	if err != nil {
		t.Fatalf("vision usage row not recorded: %v", err)
	}
	if providerName != "test-provider" || model != "sonnet" ||
		path != "/v1/messages#vision" || input != 10 || output != 20 ||
		cacheRead != 3 || cacheCreation != 4 {
		t.Fatalf("usage row=%q %q %q %d %d %d %d",
			providerName, model, path, input, output, cacheRead, cacheCreation)
	}
}

func TestVisionClientEffectivePrompt(t *testing.T) {
	if got := effectivePrompt(" \n\t "); got != defaultPrompt {
		t.Fatalf("blank prompt=%q", got)
	}
	const configured = "  preserve surrounding space  "
	if got := effectivePrompt(configured); got != configured {
		t.Fatalf("configured prompt=%q", got)
	}
}

func TestTruncateDebugContent(t *testing.T) {
	const short = "short content"
	if got, truncated := truncateDebugContent(short); got != short || truncated {
		t.Fatalf("short content=%q truncated=%v", got, truncated)
	}

	long := strings.Repeat("界", debugLogContentLimit+1)
	got, truncated := truncateDebugContent(long)
	if !truncated {
		t.Fatal("long content was not truncated")
	}
	if len([]rune(got)) != debugLogContentLimit {
		t.Fatalf("truncated runes=%d", len([]rune(got)))
	}
}

func TestVisionClientRetriesMatchedOverload(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"description"}]}`)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.OverloadRules = []provider.Rule{{
		Status:       http.StatusServiceUnavailable,
		BodyContains: "overloaded",
		MaxRetries:   2,
		RetryDelay:   10 * time.Millisecond,
		RetryJitter:  5 * time.Millisecond,
	}}
	client := newVisionClient(cfg, server.Client(), nil)
	var waits []time.Duration
	client.sleep = func(_ context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		return nil
	}

	got, err := client.Describe(context.Background(), nil, testImage())
	if err != nil {
		t.Fatal(err)
	}
	if got != "description" {
		t.Fatalf("description=%q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if len(waits) != 1 || waits[0] != 15*time.Millisecond {
		t.Fatalf("waits=%v", waits)
	}
}

func TestVisionClientHTTPRuleReplacesNetworkFallback(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"second-rule-overload"}`)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.OverloadRules = []provider.Rule{
		{
			Status:      529,
			MaxRetries:  5,
			RetryDelay:  10 * time.Millisecond,
			RetryJitter: time.Millisecond,
		},
		{
			Status:       http.StatusServiceUnavailable,
			BodyContains: "second-rule-overload",
			MaxRetries:   2,
			RetryDelay:   100 * time.Millisecond,
			RetryJitter:  10 * time.Millisecond,
		},
	}
	transport := &failOnceTransport{next: server.Client().Transport}
	client := newVisionClient(cfg, &http.Client{Transport: transport}, nil)
	var waits []time.Duration
	client.sleep = func(_ context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		return nil
	}

	_, err := client.Describe(context.Background(), nil, testImage())
	if err == nil {
		t.Fatal("expected overload error")
	}
	if transport.calls.Load() != 3 {
		t.Fatalf("transport calls=%d, want 3", transport.calls.Load())
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", upstreamCalls.Load())
	}
	wantWaits := []time.Duration{11 * time.Millisecond, 120 * time.Millisecond}
	if len(waits) != len(wantWaits) {
		t.Fatalf("waits=%v, want %v", waits, wantWaits)
	}
	for i := range wantWaits {
		if waits[i] != wantWaits[i] {
			t.Fatalf("wait %d=%v, want %v", i, waits[i], wantWaits[i])
		}
	}
}

func TestVisionClientDoesNotFollowRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetCalls atomic.Int32
			var targetMu sync.Mutex
			var targetHeaders http.Header
			var targetBody []byte
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetCalls.Add(1)
				body, _ := io.ReadAll(r.Body)
				targetMu.Lock()
				targetHeaders = r.Header.Clone()
				targetBody = append([]byte(nil), body...)
				targetMu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()

			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL+"/stolen")
				w.WriteHeader(status)
			}))
			defer origin.Close()

			cfg := testVisionConfig(origin.URL)
			client := newVisionClient(cfg, origin.Client(), nil)
			headers := http.Header{
				"Authorization":     {"Bearer redirect-secret"},
				"X-Api-Key":         {"redirect-key"},
				"Anthropic-Version": {"2023-06-01"},
				"Anthropic-Beta":    {"files-api-2025-04-14"},
			}

			_, err := client.Describe(context.Background(), headers, testImage())
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Fatalf("error=%v", err)
			}
			if targetCalls.Load() != 0 {
				t.Fatalf("redirect target calls=%d", targetCalls.Load())
			}
			targetMu.Lock()
			defer targetMu.Unlock()
			if len(targetHeaders) != 0 || len(targetBody) != 0 {
				t.Fatalf("redirect target received headers=%v body=%q", targetHeaders, targetBody)
			}
		})
	}
}

func TestVisionClientDoesNotInjectCookiesOrMutateCallerClient(t *testing.T) {
	receivedCookie := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCookie <- r.Header.Get("Cookie")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"description"}]}`)
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRequest, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(seedRequest.URL, []*http.Cookie{{Name: "session", Value: "cookie-secret"}})
	callerClient := server.Client()
	originalTransport := callerClient.Transport
	callerClient.Jar = jar
	callerClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("caller's redirect policy")
	}

	client := newVisionClient(testVisionConfig(server.URL), callerClient, nil)
	if client.httpClient == callerClient {
		t.Fatal("shadow client must be an independent copy")
	}
	if client.httpClient.Jar != nil {
		t.Fatal("shadow client retained caller CookieJar")
	}
	if callerClient.Jar != jar || callerClient.Transport != originalTransport || callerClient.CheckRedirect == nil {
		t.Fatal("caller client was mutated")
	}

	if _, err := client.Describe(context.Background(), nil, testImage()); err != nil {
		t.Fatal(err)
	}
	if cookie := <-receivedCookie; cookie != "" {
		t.Fatalf("upstream received Cookie=%q", cookie)
	}
	if cookies := callerClient.Jar.Cookies(seedRequest.URL); len(cookies) != 1 ||
		cookies[0].Name != "session" || cookies[0].Value != "cookie-secret" {
		t.Fatalf("caller CookieJar changed: %v", cookies)
	}
}

func TestVisionClientDoesNotRetryUnmatchedErrorOrLeakSecrets(t *testing.T) {
	const responseSecret = "private-upstream-response"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, responseSecret)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.OverloadRules = []provider.Rule{{
		Status:       http.StatusServiceUnavailable,
		BodyContains: "overloaded",
		MaxRetries:   3,
	}}
	client := newVisionClient(cfg, server.Client(), nil)
	var usageCalls atomic.Int32
	client.recordUsage = func([]byte) { usageCalls.Add(1) }
	image := testImage()
	headers := http.Header{"Authorization": {"Bearer secret-auth"}}

	_, err := client.Describe(context.Background(), headers, image)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error lacks status: %v", err)
	}
	for _, secret := range []string{responseSecret, "https://private.example/image.png", "secret-auth"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
	if usageCalls.Load() != 0 {
		t.Fatalf("usage calls=%d", usageCalls.Load())
	}
}

func TestVisionClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Vision.Timeout = 20 * time.Millisecond
	client := newVisionClient(cfg, server.Client(), nil)

	_, err := client.Describe(context.Background(), nil, testImage())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestVisionClientNetworkErrorBoundAndFreshBodies(t *testing.T) {
	cfg := testVisionConfig("https://upstream.invalid")
	cfg.OverloadRules = []provider.Rule{{
		MaxRetries:  2,
		RetryDelay:  time.Millisecond,
		RetryJitter: time.Millisecond,
	}}
	transport := &failingTransport{}
	client := newVisionClient(cfg, &http.Client{Transport: transport}, nil)
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Describe(context.Background(), nil, testImage())
	if err == nil {
		t.Fatal("expected error")
	}
	if transport.calls != 3 {
		t.Fatalf("calls=%d, want 3", transport.calls)
	}
	if len(transport.bodies) != 3 {
		t.Fatalf("bodies=%d", len(transport.bodies))
	}
	for i := 1; i < len(transport.bodies); i++ {
		if !bytes.Equal(transport.bodies[0], transport.bodies[i]) {
			t.Fatalf("body %d differs across retries", i)
		}
	}
	if strings.Contains(err.Error(), "transport-secret") {
		t.Fatalf("transport error leaked details: %v", err)
	}
}

type failingTransport struct {
	mu     sync.Mutex
	calls  int
	bodies [][]byte
}

type failOnceTransport struct {
	next  http.RoundTripper
	calls atomic.Int32
}

func (t *failOnceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.calls.Add(1) == 1 {
		return nil, errors.New("initial network failure")
	}
	return t.next.RoundTrip(r)
}

func (t *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.bodies = append(t.bodies, body)
	return nil, errors.New("transport-secret")
}

func testVisionConfig(upstream string) *config.Config {
	return &config.Config{
		Upstream:     upstream,
		ProviderName: "test-provider",
		Vision: config.VisionConfig{
			Model:     "sonnet",
			MaxTokens: 2048,
			Timeout:   time.Second,
		},
		OverloadRules: []provider.Rule{{
			Status:       http.StatusServiceUnavailable,
			BodyContains: "overloaded",
			MaxRetries:   2,
		}},
	}
}

func testImage() imageRef {
	return imageRef{
		block:        json.RawMessage(`{"type":"image","source":{"type":"url","url":"https://private.example/image.png"}}`),
		sourceType:   "url",
		cachePayload: "https://private.example/image.png",
	}
}
