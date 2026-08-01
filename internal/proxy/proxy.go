// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/routing"
	"github.com/Euphie/llm-proxy/internal/stats"
	"github.com/Euphie/llm-proxy/internal/vision"
)

// New returns an http.Handler that forwards every request to cfg.Upstream,
// automatically retrying when the response matches an overload rule.
// Pass a non-nil *stats.DB to enable async token usage recording.
func New(cfg profile.Runtime, client *http.Client, sdb *stats.DB) http.Handler {
	var visionPreprocessor *vision.Preprocessor
	if cfg.Vision.Enabled && strings.TrimSpace(cfg.Vision.Model) != "" {
		visionPreprocessor = vision.New(cfg, client, sdb)
	}
	var routeEngine *routing.Engine
	if cfg.AutoRouting.Enabled {
		engine, err := routing.NewEngine(cfg, client)
		if err != nil {
			slog.Error("routing.engine.disabled", "profile", cfg.Slug, "error", err)
		} else {
			routeEngine = engine
		}
	}
	return &handler{
		cfg:     cfg,
		client:  client,
		stats:   sdb,
		parser:  stats.NewParser(string(cfg.Protocol)),
		vision:  visionPreprocessor,
		routing: routeEngine,
	}
}

type handler struct {
	cfg     profile.Runtime
	client  *http.Client
	stats   *stats.DB
	parser  stats.Parser
	vision  *vision.Preprocessor
	routing *routing.Engine
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

	requestCtx := r.Context()
	var budget *routing.AttemptBudget
	var plan routing.ExecutionPlan
	isAuto := false
	autoStream := false
	if model, ok := routing.RequestedModel(body); ok && model == routing.AutoModel {
		isAuto = true
		if h.routing == nil {
			http.Error(w, "intelligent routing is not enabled for this Profile", http.StatusBadRequest)
			return
		}
		routeRequest, routeErr := routing.ParseAutoRequest(
			h.cfg.Protocol,
			r.Method,
			r.URL.EscapedPath(),
			r.Header.Get("Content-Type"),
			body,
		)
		if routeErr != nil {
			writeRoutingError(w, routeErr)
			return
		}
		autoStream = routeRequest.Facts.Stream
		var cancel context.CancelFunc
		budget, requestCtx, cancel = h.routing.NewAttemptBudget(r.Context())
		defer cancel()
		var classification routing.Classification
		plan, classification, routeErr = h.routing.Route(requestCtx, r.Header, routeRequest, budget)
		if routeErr != nil {
			if requestCtx.Err() != nil {
				return
			}
			writeRoutingError(w, routeErr)
			return
		}
		body, routeErr = routeRequest.WithModel(plan.Model())
		if routeErr != nil {
			writeRoutingError(w, routeErr)
			return
		}
		slog.Info("routing.plan.created",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"model", plan.Model(),
			"vision_mode", plan.VisionMode(),
			"classification_source", classification.Source,
			"estimated_cost_micro_usd", plan.EstimatedCostMicroUSD(),
			"worst_case_cost_micro_usd", plan.WorstCaseCostMicroUSD())
	}

	preprocessVision := h.vision != nil && shouldPreprocessVision(h.cfg.Protocol, r)
	if isAuto {
		preprocessVision = h.vision != nil && plan.VisionMode() == routing.VisionComposite
	}
	if preprocessVision {
		var reserveVisionCall func(context.Context) error
		if budget != nil {
			reserveVisionCall = func(ctx context.Context) error {
				return budget.ReserveCall(ctx, routing.CallVision, plan.VisionCallCostMicroUSD())
			}
		}
		body, err = h.vision.ProcessTargetWithBudget(
			requestCtx,
			r.Header,
			body,
			target,
			reserveVisionCall,
		)
		if err != nil {
			if requestCtx.Err() != nil {
				return
			}
			if errors.Is(err, routing.ErrAttemptBudgetExceeded) {
				writeRoutingError(w, err)
				return
			}
			status := vision.HTTPStatus(err)
			http.Error(w, http.StatusText(status), status)
			return
		}
	}
	if requestCtx.Err() != nil {
		return
	}
	if budget != nil {
		if err := budget.ReserveCall(requestCtx, routing.CallAnswer, plan.AnswerCallCostMicroUSD()); err != nil {
			writeRoutingError(w, err)
			return
		}
	}

	var rule *provider.Rule
	retries := 0
	for {
		resp, err := h.do(requestCtx, r.Method, target, r.Header, body)
		if err != nil {
			if requestCtx.Err() != nil {
				return
			}
			if rule == nil {
				if len(h.cfg.OverloadRules) == 0 {
					slog.Error("upstream failed without retry rules",
						"profile", label, "attempts", retries+1, "err", err)
					http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
					return
				}
				rule = &h.cfg.OverloadRules[0]
			}
			if retries >= rule.MaxRetries || !h.reserveRetry(requestCtx, budget, target, plan) {
				slog.Error("upstream failed without retry rules",
					"profile", label, "attempts", retries+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			retries++
			if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
				return
			}
			continue
		}

		if resp.StatusCode < 400 {
			var captured []byte
			if isAuto {
				result := relayAutoSuccess(w, resp, autoStream)
				if result.err != nil {
					if result.committed {
						slog.Warn("routing.client_stream.failed_after_commit",
							"profile", label,
							"strategy", plan.Strategy(),
							"route", plan.Route(),
							"model", plan.Model(),
							"error", result.err)
						return
					}
					if requestCtx.Err() != nil {
						return
					}
					if rule == nil && len(h.cfg.OverloadRules) > 0 {
						rule = &h.cfg.OverloadRules[0]
					}
					if rule == nil || retries >= rule.MaxRetries ||
						!h.reserveRetry(requestCtx, budget, target, plan) {
						http.Error(w, "upstream stream failed before client commit", http.StatusBadGateway)
						return
					}
					retries++
					if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
						return
					}
					continue
				}
				captured = result.captured
			} else {
				captured = stream(w, resp)
			}
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", retries+1, "elapsed", time.Since(start).Round(time.Millisecond))
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
			if retries >= rule.MaxRetries || !h.reserveRetry(requestCtx, budget, target, plan) {
				forward(w, resp, errBody)
				return
			}
			retries++
			if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
				return
			}
			continue
		}

		// Non-overload error: forward as-is
		forward(w, resp, errBody)
		return
	}
}

const maxPrecommitStreamBytes = 1 << 20

type relayResult struct {
	captured  []byte
	committed bool
	err       error
}

func relayAutoSuccess(
	w http.ResponseWriter,
	resp *http.Response,
	streaming bool,
) relayResult {
	defer resp.Body.Close()
	if !streaming {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return relayResult{err: err}
		}
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, err = w.Write(body)
		return relayResult{captured: body, committed: true, err: err}
	}

	flusher, canFlush := w.(http.Flusher)
	var captured bytes.Buffer
	var pending bytes.Buffer
	committed := false
	buffer := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			captured.Write(chunk)
			if committed {
				if _, err := w.Write(chunk); err != nil {
					return relayResult{captured: captured.Bytes(), committed: true, err: err}
				}
				if canFlush {
					flusher.Flush()
				}
			} else {
				pending.Write(chunk)
				if pending.Len() > maxPrecommitStreamBytes {
					return relayResult{err: errors.New("stream exceeded pre-commit buffer limit")}
				}
				ready, err := hasCompleteSSEEvent(pending.Bytes())
				if err != nil {
					return relayResult{err: err}
				}
				if ready {
					copyHeaders(w.Header(), resp.Header)
					w.WriteHeader(resp.StatusCode)
					if _, err := w.Write(pending.Bytes()); err != nil {
						return relayResult{captured: captured.Bytes(), committed: true, err: err}
					}
					pending.Reset()
					committed = true
					if canFlush {
						flusher.Flush()
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if committed {
					return relayResult{captured: captured.Bytes(), committed: true}
				}
				return relayResult{err: errors.New("stream ended before a complete protocol event")}
			}
			return relayResult{captured: captured.Bytes(), committed: committed, err: readErr}
		}
	}
}

func hasCompleteSSEEvent(buffer []byte) (bool, error) {
	normalized := strings.ReplaceAll(string(buffer), "\r\n", "\n")
	for {
		end := strings.Index(normalized, "\n\n")
		if end < 0 {
			return false, nil
		}
		event := normalized[:end]
		normalized = normalized[end+2:]
		dataLines := make([]string, 0, 1)
		for line := range strings.SplitSeq(event, "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				dataLines = append(dataLines, strings.TrimSpace(after))
			}
		}
		if len(dataLines) == 0 {
			continue
		}
		data := strings.Join(dataLines, "\n")
		if data == "[DONE]" {
			return true, nil
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &payload); err != nil || payload == nil {
			return false, errors.New("stream produced an invalid protocol event before client commit")
		}
		return true, nil
	}
}

func (h *handler) reserveRetry(
	ctx context.Context,
	budget *routing.AttemptBudget,
	target string,
	plan routing.ExecutionPlan,
) bool {
	if budget == nil {
		return true
	}
	return budget.ReserveRetry(ctx, target, plan.AnswerCallCostMicroUSD()) == nil
}

func waitForRetry(
	ctx context.Context,
	profileSlug string,
	path string,
	rule *provider.Rule,
	retry int,
) bool {
	wait := rule.RetryDelay + time.Duration(retry)*rule.RetryJitter
	slog.Info("retry",
		"profile", profileSlug, "attempt", retry,
		"max", rule.MaxRetries, "wait", wait, "path", path)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeRoutingError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, routing.ErrUnsupportedOperation), errors.Is(err, routing.ErrNoCapableModel):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, routing.ErrInvalidRequest), errors.Is(err, routing.ErrRoutingDisabled):
		status = http.StatusBadRequest
	case errors.Is(err, routing.ErrAttemptBudgetExceeded):
		status = http.StatusTooManyRequests
	}
	http.Error(w, err.Error(), status)
}

func targetURL(upstream, requestURI string) string {
	return strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(requestURI, "/")
}

func shouldPreprocessVision(protocol profile.Protocol, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := r.URL.EscapedPath()
	switch protocol {
	case profile.ProtocolAnthropic:
		if path != "/v1/messages" {
			return false
		}
	case profile.ProtocolOpenAI:
		if path != "/responses" && path != "/v1/responses" && path != "/v1/chat/completions" {
			return false
		}
	default:
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
