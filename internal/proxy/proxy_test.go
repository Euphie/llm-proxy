package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"anthropic-proxy/internal/config"
	"anthropic-proxy/internal/provider"
)

const (
	visionImageBody = `{"model":"main-model","max_tokens":256,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"What is shown?"}]}]}`
	visionTextBody  = `{"model":"main-model","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`
)

type visionUpstream struct {
	server      *httptest.Server
	visionCalls atomic.Int32
	mainCalls   atomic.Int32
	mainBodies  chan []byte
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

	upstream := &visionUpstream{mainBodies: make(chan []byte, 1)}
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
		case "main-model":
			upstream.mainCalls.Add(1)
			upstream.mainBodies <- append([]byte(nil), body...)
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
	return New(&config.Config{
		Upstream:     u.server.URL,
		ProviderName: "test",
		Protocol:     "anthropic",
		OverloadRules: []provider.Rule{{
			Status:     http.StatusServiceUnavailable,
			MaxRetries: 0,
		}},
		Vision: config.VisionConfig{
			Enabled:         enabled,
			Model:           "sonnet",
			MaxTokens:       2048,
			Timeout:         2 * time.Second,
			MaxConcurrency:  4,
			CacheTTL:        30 * time.Minute,
			CacheMaxEntries: 512,
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
	handler := New(&config.Config{
		Upstream:     "https://upstream.test",
		ProviderName: "test",
		Protocol:     "anthropic",
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
