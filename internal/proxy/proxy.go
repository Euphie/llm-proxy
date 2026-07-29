// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/stats"
	"github.com/Euphie/llm-proxy/internal/vision"
)

// New returns an http.Handler that forwards every request to cfg.Upstream,
// automatically retrying when the response matches an overload rule.
// Pass a non-nil *stats.DB to enable async token usage recording.
func New(cfg profile.Runtime, client *http.Client, sdb *stats.DB) http.Handler {
	var visionPreprocessor *vision.Preprocessor
	if cfg.Protocol == profile.ProtocolAnthropic && cfg.Vision.Enabled {
		visionPreprocessor = vision.New(cfg, client, sdb)
	}
	return &handler{
		cfg:    cfg,
		client: client,
		stats:  sdb,
		parser: stats.NewParser(string(cfg.Protocol)),
		vision: visionPreprocessor,
	}
}

type handler struct {
	cfg    profile.Runtime
	client *http.Client
	stats  *stats.DB
	parser stats.Parser
	vision *vision.Preprocessor
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	label := h.cfg.Slug
	target := targetURL(h.cfg.Upstream, r.RequestURI)
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	if h.vision != nil && shouldPreprocessVision(r) {
		body, err = h.vision.Process(r.Context(), r.Header, body)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			status := vision.HTTPStatus(err)
			http.Error(w, http.StatusText(status), status)
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}

	// rule is locked in on the first overload match and reused for subsequent retries.
	var rule *provider.Rule

	for attempt := 0; ; attempt++ {
		if rule != nil {
			if attempt > rule.MaxRetries {
				slog.Warn("max retries reached, giving up",
					"profile", label, "max", rule.MaxRetries)
				break
			}
			wait := rule.RetryDelay + time.Duration(attempt)*rule.RetryJitter
			slog.Info("retry",
				"profile", label, "attempt", attempt,
				"max", rule.MaxRetries, "wait", wait, "path", r.URL.Path)

			select {
			case <-r.Context().Done():
				return
			case <-time.After(wait):
			}
		}

		resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if rule != nil && attempt >= rule.MaxRetries {
				slog.Error("upstream failed", "profile", label, "attempts", attempt+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			if rule == nil && len(h.cfg.OverloadRules) == 0 {
				slog.Error("upstream failed without retry rules",
					"profile", label, "attempts", attempt+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			slog.Warn("upstream error, will retry", "profile", label, "attempt", attempt+1, "err", err)
			if rule == nil {
				rule = &h.cfg.OverloadRules[0]
			}
			continue
		}

		// 2xx: stream to client while capturing for stats
		if resp.StatusCode < 400 {
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", attempt+1, "elapsed", time.Since(start).Round(time.Millisecond))
			captured := stream(w, resp)
			if h.stats != nil {
				h.stats.RecordAsync(stats.RequestMeta{
					ProfileID:   h.cfg.ID,
					ProfileSlug: label,
					Protocol:    string(h.cfg.Protocol),
					Kind:        "main",
					Path:        r.URL.Path,
				}, captured, h.parser)
			}
			return
		}

		// Error response: buffer to check for overload
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody); matched != nil {
			if rule == nil {
				rule = matched
			}
			continue
		}

		// Non-overload error: forward as-is
		forward(w, resp, errBody)
		return
	}

	// Still overloaded after max retries — re-issue one final request to forward the error.
	resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	errBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	forward(w, resp, errBody)
}

func targetURL(upstream, requestURI string) string {
	return strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(requestURI, "/")
}

func shouldPreprocessVision(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/messages" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func (h *handler) do(ctx context.Context, method, url string, headers http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, headers)
	return h.client.Do(req)
}

// stream writes a successful response to w with SSE-friendly chunked flushing,
// while simultaneously capturing the data for usage parsing.
// It returns all bytes written (the captured body).
func stream(w http.ResponseWriter, resp *http.Response) []byte {
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	var capture bytes.Buffer
	buf := make([]byte, 4096)

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			capture.Write(chunk)
			_, _ = w.Write(chunk)
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return capture.Bytes()
}

// forward writes a buffered (error) response back to the client.
func forward(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func copyHeaders(dst, src http.Header) {
	connectionHeaders := make(map[string]struct{})
	for _, value := range src.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				connectionHeaders[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}
	for k, vs := range src {
		if isHopByHopHeader(k) {
			continue
		}
		if _, remove := connectionHeaders[http.CanonicalHeaderKey(k)]; remove {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade":
		return true
	default:
		return false
	}
}
