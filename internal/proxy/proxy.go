// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"anthropic-proxy/internal/config"
	"anthropic-proxy/internal/provider"
)

// New returns an http.Handler that forwards every request to cfg.Upstream,
// automatically retrying when the response matches an overload rule.
func New(cfg *config.Config, client *http.Client) http.Handler {
	return &handler{cfg: cfg, client: client}
}

type handler struct {
	cfg    *config.Config
	client *http.Client
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	label := h.cfg.ProviderName
	target := h.cfg.Upstream + r.RequestURI
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// rule is locked in on the first overload match and reused for subsequent retries.
	var rule *provider.Rule

	for attempt := 0; ; attempt++ {
		if rule != nil {
			if attempt > rule.MaxRetries {
				slog.Warn("max retries reached, giving up",
					"provider", label, "max", rule.MaxRetries)
				break
			}
			wait := rule.RetryDelay + time.Duration(attempt)*rule.RetryJitter
			slog.Info("retry",
				"provider", label, "attempt", attempt,
				"max", rule.MaxRetries, "wait", wait, "path", r.URL.Path)

			select {
			case <-r.Context().Done():
				http.Error(w, "client disconnected", http.StatusGatewayTimeout)
				return
			case <-time.After(wait):
			}
		}

		resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
		if err != nil {
			if rule != nil && attempt >= rule.MaxRetries {
				slog.Error("upstream failed", "provider", label, "attempts", attempt+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			slog.Warn("upstream error, will retry", "provider", label, "attempt", attempt+1, "err", err)
			if rule == nil {
				rule = &h.cfg.OverloadRules[0] // use first rule's params for connection errors
			}
			continue
		}

		// 2xx: stream directly without buffering
		if resp.StatusCode < 400 {
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", attempt+1, "elapsed", time.Since(start).Round(time.Millisecond))
			stream(w, resp)
			return
		}

		// Error response: buffer to check for overload
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody); matched != nil {
			if rule == nil {
				rule = matched // lock in retry params from the first matched rule
			}
			continue // wait and retry handled at the top of the loop
		}

		// Non-overload error: forward as-is
		forward(w, resp, errBody)
		return
	}

	// Fell through: still overloaded after max retries — return last error response
	// Re-issue one final request just to get the response to forward.
	// (We can't reuse the previous resp.Body as it was already read.)
	resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	errBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	forward(w, resp, errBody)
}

func (h *handler) do(ctx context.Context, method, url string, headers http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return h.client.Do(req)
}

// stream writes a successful response with SSE-friendly chunked flushing.
func stream(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if ok {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// forward writes a buffered (error) response back to the client.
func forward(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
