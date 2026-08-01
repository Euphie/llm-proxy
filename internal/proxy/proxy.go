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
	return NewWithSessionStore(cfg, client, sdb, nil)
}

func NewWithSessionStore(
	cfg profile.Runtime,
	client *http.Client,
	sdb *stats.DB,
	sessions *routing.SessionStore,
) http.Handler {
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
		cfg:      cfg,
		client:   client,
		stats:    sdb,
		parser:   stats.NewParser(string(cfg.Protocol)),
		vision:   visionPreprocessor,
		routing:  routeEngine,
		sessions: sessions,
	}
}

type handler struct {
	cfg      profile.Runtime
	client   *http.Client
	stats    *stats.DB
	parser   stats.Parser
	vision   *vision.Preprocessor
	routing  *routing.Engine
	sessions *routing.SessionStore
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	responseState := &responseStateWriter{ResponseWriter: w}
	w = responseState
	label := h.cfg.Slug
	target := targetURL(h.cfg.Upstream, r.RequestURI)
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)
	sessionID := singleSessionID(r.Header)
	r.Header.Del(routing.SessionIDHeader)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	requestCtx := r.Context()
	var budget *routing.AttemptBudget
	var plan routing.ExecutionPlan
	var classification routing.Classification
	var routeRequest routing.Request
	var modelAttempts []routing.ModelAttemptPlan
	var currentAttempt routing.ModelAttemptPlan
	modelAttemptIndex := 0
	isAuto := false
	autoStream := false
	var sessionKey routing.SessionKey
	sessionKeyValid := false
	sessionBindingUsed := false
	if model, ok := routing.RequestedModel(body); ok && model == routing.AutoModel {
		isAuto = true
		if h.routing == nil {
			http.Error(w, "intelligent routing is not enabled for this Profile", http.StatusBadRequest)
			return
		}
		var routeErr error
		routeRequest, routeErr = routing.ParseAutoRequest(
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
		plan, classification, routeErr = h.routing.RouteWithPreference(
			requestCtx,
			r.Header,
			routeRequest,
			budget,
			func(routeID string) routing.SessionPreference {
				if h.sessions == nil || sessionID == "" {
					return routing.SessionPreference{}
				}
				key, ok := h.sessions.Key(
					r.Header,
					sessionID,
					h.cfg.ID,
					routeID,
					routing.SessionPurposeLLM,
				)
				if !ok {
					return routing.SessionPreference{}
				}
				sessionKey = key
				sessionKeyValid = true
				binding, found, err := h.sessions.Get(requestCtx, key)
				if err != nil {
					slog.Warn("routing.session.read_failed", "profile", label, "route", routeID, "error", err)
					return routing.SessionPreference{}
				}
				if !found {
					return routing.SessionPreference{}
				}
				sessionBindingUsed = true
				return routing.SessionPreference{
					Model: binding.Model, MinQualityScoreBPS: binding.QualityScoreBPS,
				}
			},
		)
		if routeErr != nil {
			if requestCtx.Err() != nil {
				return
			}
			writeRoutingError(w, routeErr)
			return
		}
		modelAttempts = plan.ModelAttempts()
		if len(modelAttempts) == 0 {
			writeRoutingError(w, routing.ErrNoCapableModel)
			return
		}
		currentAttempt = modelAttempts[0]
		initialModel := currentAttempt.Model()
		defer func() {
			if h.stats == nil {
				return
			}
			snapshot := budget.Snapshot()
			h.stats.RecordRoutingTraceAsync(stats.RoutingTrace{
				ProfileID: h.cfg.ID, ProfileSlug: label,
				Protocol: string(h.cfg.Protocol), Path: r.URL.Path,
				Strategy: plan.Strategy(), Route: plan.Route(),
				TaskType: classification.TaskType, Risk: string(classification.Risk),
				ClassificationSource: string(classification.Source),
				InitialModel:         initialModel, FinalModel: currentAttempt.Model(),
				VisionMode:                   string(currentAttempt.VisionMode()),
				StatusCode:                   responseState.statusCode,
				ClientCommitted:              responseState.committed,
				AnswerAttempts:               snapshot.AnswerAttempts,
				AuxiliaryCalls:               snapshot.AuxiliaryCalls,
				TotalOutboundCalls:           snapshot.TotalOutboundCalls,
				ModelSwitches:                snapshot.ModelSwitches,
				TargetSwitches:               snapshot.TargetSwitches,
				PlannedWorstCaseCostMicroUSD: plan.WorstCaseCostMicroUSD(),
				ReservedCostMicroUSD:         snapshot.WorstCaseCostMicroUSD,
				ElapsedMilliseconds:          time.Since(start).Milliseconds(),
			})
		}()
		slog.Info("routing.plan.created",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"model", plan.Model(),
			"vision_mode", plan.VisionMode(),
			"planned_model_attempts", len(modelAttempts),
			"classification_source", classification.Source,
			"decision_reason", plan.Reason(),
			"session_binding_used", sessionBindingUsed,
			"estimated_cost_micro_usd", plan.EstimatedCostMicroUSD(),
			"worst_case_cost_micro_usd", plan.WorstCaseCostMicroUSD())
	}

	preprocessVision := h.vision != nil && shouldPreprocessVision(h.cfg.Protocol, r)
	if isAuto {
		body, err = h.prepareAutoAttempt(
			requestCtx,
			r.Header,
			target,
			routeRequest,
			currentAttempt,
			budget,
		)
		if err != nil {
			writeAutoPreparationError(w, requestCtx, err)
			return
		}
	} else if preprocessVision {
		var reserveVisionCall func(context.Context) error
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
		if err := budget.ReserveCall(requestCtx, routing.CallAnswer, currentAttempt.AnswerCallCostMicroUSD()); err != nil {
			writeRoutingError(w, err)
			return
		}
	}

	answerAttempts := 1
	switchModel := func() (bool, error) {
		if !isAuto || modelAttemptIndex+1 >= len(modelAttempts) {
			return false, nil
		}
		next := modelAttempts[modelAttemptIndex+1]
		if err := budget.CanReserveModelSwitchCall(
			requestCtx,
			next.AnswerCallCostMicroUSD(),
		); err != nil {
			return false, nil
		}
		nextBody, err := h.prepareAutoAttempt(
			requestCtx,
			r.Header,
			target,
			routeRequest,
			next,
			budget,
		)
		if err != nil {
			return false, err
		}
		if err := budget.ReserveModelSwitchCall(
			requestCtx,
			next.AnswerCallCostMicroUSD(),
		); err != nil {
			return false, err
		}
		previous := currentAttempt.Model()
		modelAttemptIndex++
		answerAttempts++
		currentAttempt = next
		body = nextBody
		slog.Info("routing.model.switched",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"from_model", previous,
			"to_model", currentAttempt.Model(),
			"vision_mode", currentAttempt.VisionMode())
		return true, nil
	}

	var rule *provider.Rule
	retries := 0
	for {
		resp, err := h.do(requestCtx, r.Method, target, r.Header, body)
		if err != nil {
			if requestCtx.Err() != nil {
				return
			}
			if rule == nil && len(h.cfg.OverloadRules) > 0 {
				rule = &h.cfg.OverloadRules[0]
			}
			if rule != nil && retries < rule.MaxRetries &&
				h.reserveRetry(requestCtx, budget, target, currentAttempt.AnswerCallCostMicroUSD()) {
				retries++
				answerAttempts++
				if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
					return
				}
				continue
			}
			switched, switchErr := switchModel()
			if switchErr != nil {
				writeAutoPreparationError(w, requestCtx, switchErr)
				return
			}
			if switched {
				rule = nil
				retries = 0
				continue
			}
			slog.Error("upstream request failed",
				"profile", label, "model", currentAttempt.Model(), "err", err)
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
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
							"model", currentAttempt.Model(),
							"error", result.err)
						return
					}
					if requestCtx.Err() != nil {
						return
					}
					if rule == nil && len(h.cfg.OverloadRules) > 0 {
						rule = &h.cfg.OverloadRules[0]
					}
					if rule != nil && retries < rule.MaxRetries &&
						h.reserveRetry(requestCtx, budget, target, currentAttempt.AnswerCallCostMicroUSD()) {
						retries++
						answerAttempts++
						if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
							return
						}
						continue
					}
					switched, switchErr := switchModel()
					if switchErr != nil {
						writeAutoPreparationError(w, requestCtx, switchErr)
						return
					}
					if switched {
						rule = nil
						retries = 0
						continue
					}
					http.Error(w, "upstream stream failed before client commit", http.StatusBadGateway)
					return
				}
				captured = result.captured
			} else {
				captured = stream(w, resp)
			}
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", answerAttempts, "elapsed", time.Since(start).Round(time.Millisecond))
			if h.stats != nil {
				h.stats.RecordAsync(stats.RequestMeta{
					ProfileID:   h.cfg.ID,
					ProfileSlug: label,
					Protocol:    string(h.cfg.Protocol),
					Kind:        "main",
					Path:        r.URL.Path,
				}, captured, h.parser)
			}
			if isAuto && sessionKeyValid && modelAttemptIndex == 0 {
				h.bindRoutingSession(sessionKey, plan, currentAttempt)
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
			if retries < rule.MaxRetries &&
				h.reserveRetry(requestCtx, budget, target, currentAttempt.AnswerCallCostMicroUSD()) {
				retries++
				answerAttempts++
				if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
					return
				}
				continue
			}
			switched, switchErr := switchModel()
			if switchErr != nil {
				writeAutoPreparationError(w, requestCtx, switchErr)
				return
			}
			if switched {
				rule = nil
				retries = 0
				continue
			}
			forward(w, resp, errBody)
			return
		}

		// Non-overload error: forward as-is
		forward(w, resp, errBody)
		return
	}
}

func singleSessionID(headers http.Header) string {
	values := headers.Values(routing.SessionIDHeader)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func (h *handler) bindRoutingSession(
	key routing.SessionKey,
	plan routing.ExecutionPlan,
	attempt routing.ModelAttemptPlan,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.sessions.Bind(ctx, key, routing.SessionBinding{
		ProfileID: h.cfg.ID,
		Route:     plan.Route(), Purpose: routing.SessionPurposeLLM,
		Model: attempt.Model(), QualityScoreBPS: attempt.QualityScoreBPS(),
		Strategy: plan.Strategy(),
	}, h.cfg.AutoRouting.SessionTTL); err != nil {
		slog.Warn(
			"routing.session.write_failed",
			"profile", h.cfg.Slug,
			"route", plan.Route(),
			"model", attempt.Model(),
			"error", err,
		)
	}
}

const maxPrecommitStreamBytes = 1 << 20

type responseStateWriter struct {
	http.ResponseWriter
	statusCode int
	committed  bool
}

func (w *responseStateWriter) WriteHeader(statusCode int) {
	if w.committed {
		return
	}
	w.statusCode = statusCode
	w.committed = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseStateWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseStateWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseStateWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

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
	answerCallCostMicroUSD int64,
) bool {
	if budget == nil {
		return true
	}
	return budget.ReserveRetry(ctx, target, answerCallCostMicroUSD) == nil
}

func (h *handler) prepareAutoAttempt(
	ctx context.Context,
	headers http.Header,
	target string,
	request routing.Request,
	attempt routing.ModelAttemptPlan,
	budget *routing.AttemptBudget,
) ([]byte, error) {
	body, err := request.WithModel(attempt.Model())
	if err != nil {
		return nil, err
	}
	if attempt.VisionMode() != routing.VisionComposite {
		return body, nil
	}
	if h.vision == nil {
		return nil, routing.ErrNoCapableModel
	}
	return h.vision.ProcessTargetWithBudget(
		ctx,
		headers,
		body,
		target,
		func(callCtx context.Context) error {
			return budget.ReserveCall(
				callCtx,
				routing.CallVision,
				attempt.VisionCallCostMicroUSD(),
			)
		},
	)
}

func writeAutoPreparationError(
	w http.ResponseWriter,
	ctx context.Context,
	err error,
) {
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, routing.ErrAttemptBudgetExceeded) ||
		errors.Is(err, routing.ErrInvalidRequest) ||
		errors.Is(err, routing.ErrNoCapableModel) {
		writeRoutingError(w, err)
		return
	}
	status := vision.HTTPStatus(err)
	http.Error(w, http.StatusText(status), status)
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
