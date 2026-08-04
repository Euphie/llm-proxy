// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/llmrequest"
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
	return NewWithEvaluation(cfg, client, sdb, sessions, nil)
}

type evaluationSubmitter interface {
	Submit(evaluation.Job) evaluation.SubmitResult
}

func NewWithEvaluation(
	cfg profile.Runtime,
	client *http.Client,
	sdb *stats.DB,
	sessions *routing.SessionStore,
	evaluations evaluationSubmitter,
) http.Handler {
	client = proxyHTTPClient(client)
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
		cfg:        cfg,
		client:     client,
		stats:      sdb,
		parser:     stats.NewParser(string(cfg.Protocol)),
		vision:     visionPreprocessor,
		routing:    routeEngine,
		sessions:   sessions,
		evaluation: evaluations,
	}
}

type handler struct {
	cfg                profile.Runtime
	client             *http.Client
	stats              *stats.DB
	parser             stats.Parser
	vision             *vision.Preprocessor
	routing            *routing.Engine
	sessions           *routing.SessionStore
	evaluation         evaluationSubmitter
	callLedgerSink     func([]routing.CallLedgerEntry)
	budgetSnapshotSink func(routing.AttemptBudgetSnapshot)
	beforeAutoAnswer   func(context.Context)
	cacheMetricsMu     sync.Mutex
	cacheMetricsLoaded time.Time
	cacheMetrics       map[string]routing.CacheMetrics
}

func (h *handler) loadRoutingCacheMetrics(
	ctx context.Context,
) (map[string]routing.CacheMetrics, error) {
	if h.stats == nil || h.cfg.ID <= 0 {
		return nil, nil
	}
	h.cacheMetricsMu.Lock()
	defer h.cacheMetricsMu.Unlock()
	if h.cacheMetrics != nil && time.Since(h.cacheMetricsLoaded) < time.Minute {
		return cloneRoutingCacheMetrics(h.cacheMetrics), nil
	}
	stored, err := h.stats.QueryModelCacheMetrics(ctx, h.cfg.ID)
	if err != nil {
		return nil, err
	}
	loaded := make(map[string]routing.CacheMetrics, len(stored))
	for model, item := range stored {
		loaded[model] = routing.CacheMetrics{
			Samples: item.Samples, UncachedInputTokens: item.UncachedInputTokens,
			CacheReadTokens: item.CacheReadTokens, CacheWriteTokens: item.CacheWriteTokens,
		}
	}
	h.cacheMetrics = loaded
	h.cacheMetricsLoaded = time.Now()
	return cloneRoutingCacheMetrics(loaded), nil
}

func cloneRoutingCacheMetrics(
	metrics map[string]routing.CacheMetrics,
) map[string]routing.CacheMetrics {
	if len(metrics) == 0 {
		return nil
	}
	cloned := make(map[string]routing.CacheMetrics, len(metrics))
	for model, item := range metrics {
		cloned[model] = item
	}
	return cloned
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	responseState := &responseStateWriter{ResponseWriter: w}
	w = responseState
	label := h.cfg.Slug
	requestURI := r.RequestURI
	target := targetURL(h.cfg.Upstream, requestURI)
	currentTargetID := profile.PrimaryTargetID
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)
	requestCtx, requestTraceID, err := vision.NewRequestTrace(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	sessionID := singleSessionID(r.Header)
	r.Header.Del(routing.SessionIDHeader)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	var budget *routing.AttemptBudget
	var plan routing.ExecutionPlan
	var classification routing.Classification
	var routeRequest routing.Request
	var modelAttempts []routing.ModelAttemptPlan
	var currentAttempt routing.ModelAttemptPlan
	var currentTargets []routing.TargetPlan
	var autoExecutor *autoExecution
	var currentNodeLease *autoNodeLease
	var callLedger *routing.CallLedger
	initialModel := ""
	initialTargetID := currentTargetID
	visionCached := false
	currentTargetIndex := 0
	modelAttemptIndex := 0
	sessionBindingModelIndex := 0
	isAuto := false
	autoStream := false
	selfEscalationEnabled := false
	selfEscalationCount := 0
	selfEscalationReason := ""
	selfEscalationModel := ""
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
		budget, requestCtx, cancel = h.routing.NewAttemptBudget(requestCtx)
		defer cancel()
		callLedger = routing.NewCallLedger(requestTraceID)
		requestCtx = routing.WithCallLedger(requestCtx, callLedger)
		defer func() {
			calls := callLedger.Snapshot()
			if h.callLedgerSink != nil {
				h.callLedgerSink(calls)
			}
			snapshot := budget.Snapshot()
			if h.budgetSnapshotSink != nil {
				h.budgetSnapshotSink(snapshot)
			}
			aggregate := callLedger.Aggregate()
			for _, call := range calls {
				logRoutingCallDebug(call)
			}
			slog.Debug(
				"routing.debug.completed",
				"request_trace_id", requestTraceID,
				"profile", label,
				"strategy", plan.Strategy(),
				"route", plan.Route(),
				"status_code", responseState.statusCode,
				"final_model", currentAttempt.Model(),
				"final_upstream_node", currentTargetID,
				"answer_attempts", snapshot.AnswerAttempts,
				"auxiliary_calls", snapshot.AuxiliaryCalls,
				"total_outbound_calls", snapshot.TotalOutboundCalls,
				"model_switches", snapshot.ModelSwitches,
				"upstream_node_switches", snapshot.TargetSwitches,
				"estimated_consumed_usd", debugUSD(aggregate.EstimatedConsumedMicroUSD),
				"known_actual_usd", debugUSD(aggregate.KnownActualMicroUSD),
				"all_actual_costs_known", aggregate.AllActualCostsKnown,
				"elapsed_ms", time.Since(start).Milliseconds(),
			)
			if h.stats == nil {
				return
			}
			h.stats.RecordRoutingTraceWithCallsAndCandidatesAsync(stats.RoutingTrace{
				CorrelationID: aggregate.CorrelationID,
				ProfileID:     h.cfg.ID, ProfileSlug: label,
				Protocol: string(h.cfg.Protocol), Path: r.URL.Path,
				Strategy: plan.Strategy(), Route: plan.Route(),
				TaskType: classification.TaskType, Difficulty: string(classification.Difficulty),
				Risk: string(classification.Risk), ClassificationSource: string(classification.Source),
				ClassificationConfidenceBPS: classification.ConfidenceBPS,
				ClassificationReasonCodes:   append([]string(nil), classification.ReasonCodes...),
				EstimatedInputTokens:        routeRequest.Facts.EstimatedInputTokens,
				RequestedOutputTokens:       routeRequest.Facts.RequestedOutputTokens,
				DecisionReason:              plan.Reason(),
				InitialModel:                initialModel, FinalModel: currentAttempt.Model(),
				InitialTarget: initialTargetID, FinalTarget: currentTargetID,
				VisionMode:                    string(currentAttempt.VisionMode()),
				StatusCode:                    responseState.statusCode,
				ClientCommitted:               responseState.committed,
				AnswerAttempts:                snapshot.AnswerAttempts,
				AuxiliaryCalls:                snapshot.AuxiliaryCalls,
				TotalOutboundCalls:            snapshot.TotalOutboundCalls,
				ModelSwitches:                 snapshot.ModelSwitches,
				SelfEscalations:               selfEscalationCount,
				SelfEscalationReason:          selfEscalationReason,
				TargetSwitches:                snapshot.TargetSwitches,
				PlannedWorstCaseCostMicroUSD:  plan.WorstCaseCostMicroUSD(),
				ConsumedEstimatedCostMicroUSD: aggregate.EstimatedConsumedMicroUSD,
				HeldCostMicroUSD:              snapshot.HeldCostMicroUSD,
				KnownActualCostMicroUSD:       aggregate.KnownActualMicroUSD,
				AllActualCostsKnown:           aggregate.AllActualCostsKnown,
				ElapsedMilliseconds:           time.Since(start).Milliseconds(),
			}, physicalCallsForStats(calls), candidateDecisionsForStats(plan.CandidateDecisions()))
		}()
		preference := routing.SessionPreference{}
		if h.stats != nil {
			metrics, err := h.loadRoutingCacheMetrics(requestCtx)
			if err != nil {
				slog.Warn("routing.cache_metrics.read_failed", "profile", label, "error", err)
			} else {
				preference.CacheMetrics = metrics
			}
		}
		if h.sessions != nil && sessionID != "" {
			if key, ok := h.sessions.Key(
				r.Header, sessionID, h.cfg.ID, routing.SessionPurposeLLM,
			); ok {
				sessionKey = key
				sessionKeyValid = true
				binding, found, err := h.sessions.Get(requestCtx, key)
				if err != nil {
					slog.Warn("routing.session.read_failed", "profile", label, "error", err)
				} else if found {
					preference.TaskType = binding.TaskType
					preference.Difficulty = binding.Difficulty
					preference.RouteID = binding.Route
					preference.Model = binding.Model
					preference.MinQualityScoreBPS = binding.QualityScoreBPS
					preference.Strategy = binding.Strategy
				}
			}
		}
		plan, classification, routeErr = h.routing.RouteWithPreference(
			requestCtx, r.Header, routeRequest, budget, preference,
		)
		if routeErr != nil {
			if requestCtx.Err() != nil {
				return
			}
			writeRoutingError(w, routeErr)
			return
		}
		sessionBindingUsed = classification.Source == routing.ClassificationSourceSession
		modelAttempts = plan.ModelAttempts()
		visionUsageParser := stats.NewParser("anthropic")
		if h.cfg.Vision.Transport != profile.VisionTransportAnthropicMessages {
			visionUsageParser = stats.NewParser("openai")
		}
		autoExecutor = newAutoExecution(
			plan, budget, callLedger, h.cfg.Models, h.parser, visionUsageParser,
		)
		if len(modelAttempts) == 0 {
			writeRoutingError(w, routing.ErrNoCapableModel)
			return
		}
		currentAttempt = modelAttempts[0]
		currentTargets = currentAttempt.Targets()
		if len(currentTargets) == 0 {
			writeRoutingError(w, routing.ErrNoCapableModel)
			return
		}
		currentTargetID = currentTargets[0].ID()
		target = targetURL(currentTargets[0].Upstream(), requestURI)
		initialModel = currentAttempt.Model()
		initialTargetID = currentTargetID
		h.logRoutingPlanDebug(
			requestTraceID,
			label,
			routeRequest,
			classification,
			plan,
		)
		slog.Info("routing.plan.created",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"model", plan.Model(),
			"target", currentTargetID,
			"vision_mode", plan.VisionMode(),
			"planned_model_attempts", len(modelAttempts),
			"classification_source", classification.Source,
			"decision_reason", plan.Reason(),
			"session_binding_used", sessionBindingUsed,
			"estimated_cost_micro_usd", plan.EstimatedCostMicroUSD(),
			"worst_case_cost_micro_usd", plan.WorstCaseCostMicroUSD())
	}

	entranceOperation, preprocessVision := requestVisionOperation(h.cfg.Protocol, r)
	preprocessVision = h.vision != nil && preprocessVision
	if isAuto {
		prepared, prepareErr := h.prepareAutoAttemptSequence(
			requestCtx,
			r.Header,
			requestURI,
			routeRequest,
			modelAttempts,
			modelAttemptIndex,
			currentTargetIndex,
			autoExecutor,
			visionCached,
		)
		if prepareErr != nil {
			writeAutoPreparationError(w, requestCtx, prepareErr)
			return
		}
		modelAttemptIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
		currentTargets = prepared.targets
		currentTargetIndex = prepared.targetIndex
		currentTargetID = prepared.target.ID()
		target = prepared.targetURL
		body = prepared.body
		currentNodeLease = prepared.nodeLease
		selfEscalationEnabled = prepared.selfEscalationEnabled
		visionCached = currentAttempt.VisionMode() == routing.VisionComposite
		defer func() { currentNodeLease.release() }()
	} else if preprocessVision {
		var reserveVisionCall func(context.Context) error
		body, err = h.vision.ProcessOperationTargetWithBudget(
			requestCtx,
			r.Header,
			entranceOperation,
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
	answerAttempts := 0
	nodeAnswerRetryIndex := 0
	switchAfterSelfEscalation := func(reason string) (bool, error) {
		nextModelIndex, ok := routing.NextStrongerModelAttempt(modelAttempts, modelAttemptIndex)
		if !isAuto || !ok {
			return false, nil
		}
		previousModel := currentAttempt.Model()
		currentNodeLease.release()
		prepared, err := h.prepareAutoAttemptSequence(
			requestCtx,
			r.Header,
			requestURI,
			routeRequest,
			modelAttempts,
			nextModelIndex,
			0,
			autoExecutor,
			visionCached,
		)
		if err != nil {
			return false, err
		}
		modelAttemptIndex = prepared.modelIndex
		sessionBindingModelIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
		currentTargets = prepared.targets
		currentTargetIndex = prepared.targetIndex
		currentTargetID = prepared.target.ID()
		target = prepared.targetURL
		body = prepared.body
		currentNodeLease = prepared.nodeLease
		selfEscalationEnabled = prepared.selfEscalationEnabled
		visionCached = visionCached || currentAttempt.VisionMode() == routing.VisionComposite
		nodeAnswerRetryIndex = 0
		selfEscalationCount++
		selfEscalationReason = reason
		selfEscalationModel = previousModel
		slog.Info(
			"routing.model.self_escalated",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"from_model", previousModel,
			"to_model", currentAttempt.Model(),
			"reason_code", reason,
		)
		return true, nil
	}
	switchAfterFailure := func() (bool, error) {
		if !isAuto {
			return false, nil
		}
		previousModel := currentAttempt.Model()
		previousTarget := currentTargetID
		nextModelIndex := modelAttemptIndex
		nextTargetIndex := currentTargetIndex + 1
		if nextTargetIndex >= len(currentTargets) {
			nextModelIndex++
			nextTargetIndex = 0
		}
		if nextModelIndex >= len(modelAttempts) {
			return false, nil
		}
		currentNodeLease.release()
		prepared, err := h.prepareAutoAttemptSequence(
			requestCtx,
			r.Header,
			requestURI,
			routeRequest,
			modelAttempts,
			nextModelIndex,
			nextTargetIndex,
			autoExecutor,
			visionCached,
		)
		if err != nil {
			return false, err
		}
		modelAttemptIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
		currentTargets = prepared.targets
		currentTargetIndex = prepared.targetIndex
		currentTargetID = prepared.target.ID()
		target = prepared.targetURL
		body = prepared.body
		currentNodeLease = prepared.nodeLease
		selfEscalationEnabled = prepared.selfEscalationEnabled
		visionCached = visionCached || currentAttempt.VisionMode() == routing.VisionComposite
		nodeAnswerRetryIndex = 0
		if previousModel == currentAttempt.Model() {
			slog.Info("routing.target.switched",
				"profile", label,
				"strategy", plan.Strategy(),
				"route", plan.Route(),
				"model", currentAttempt.Model(),
				"from_target", previousTarget,
				"to_target", currentTargetID)
		} else {
			slog.Info("routing.model.switched",
				"profile", label,
				"strategy", plan.Strategy(),
				"route", plan.Route(),
				"from_model", previousModel,
				"to_model", currentAttempt.Model(),
				"target", currentTargetID,
				"vision_mode", currentAttempt.VisionMode())
		}
		return true, nil
	}

	var rule *provider.Rule
	retries := 0
	answerRetryAllowed := func(retryIndex int) bool {
		return !isAuto || currentNodeLease.allowsAnswerRetry(retryIndex)
	}
	for {
		completeAnswer := func(int, string, []byte) {}
		if isAuto {
			if h.beforeAutoAnswer != nil {
				h.beforeAutoAnswer(requestCtx)
			}
			_, completion, ticketErr := currentNodeLease.beginAnswer(
				requestCtx, nodeAnswerRetryIndex,
			)
			if ticketErr != nil {
				writeRoutingError(w, ticketErr)
				return
			}
			completeAnswer = completion
		}
		answerAttempts++
		resp, err := h.do(requestCtx, r.Method, target, r.Header, body)
		if err != nil {
			completeAnswer(0, "network", nil)
			if requestCtx.Err() != nil {
				return
			}
			failure := provider.ClassifyTransportFailure(err)
			failureClass, _ := provider.FailureClassOf(failure)
			recoverable := recoverableTransportFailure(failureClass)
			if rule == nil {
				rule = provider.FirstRetryRule(h.cfg.OverloadRules)
			}
			if recoverable &&
				rule != nil && retries < rule.MaxRetries &&
				answerRetryAllowed(nodeAnswerRetryIndex+1) {
				retries++
				nodeAnswerRetryIndex++
				if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
					return
				}
				continue
			}
			if recoverable {
				switched, switchErr := switchAfterFailure()
				if switchErr != nil {
					writeAutoPreparationError(w, requestCtx, switchErr)
					return
				}
				if switched {
					rule = nil
					retries = 0
					continue
				}
			}
			slog.Error("upstream request failed",
				"profile", label, "model", currentAttempt.Model(),
				"target", currentTargetID, "err", err)
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode < 400 {
			var captured []byte
			if isAuto {
				result := relayAutoSuccess(
					w,
					resp,
					autoStream,
					routeRequest.Operation,
					selfEscalationEnabled,
				)
				if result.selfEscalation.Requested {
					completeAnswer(resp.StatusCode, "self_escalated", result.captured)
					switched, switchErr := switchAfterSelfEscalation(
						result.selfEscalation.ReasonCode,
					)
					if switchErr != nil {
						writeAutoPreparationError(w, requestCtx, switchErr)
						return
					}
					if !switched {
						http.Error(w, "no stronger model is available", http.StatusBadGateway)
						return
					}
					rule = nil
					retries = 0
					continue
				}
				if result.err != nil {
					completeAnswer(resp.StatusCode, "response_io", result.captured)
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
					failure := provider.ClassifyTransportFailure(result.err)
					failureClass, _ := provider.FailureClassOf(failure)
					recoverable := recoverableTransportFailure(failureClass)
					if rule == nil {
						rule = provider.FirstRetryRule(h.cfg.OverloadRules)
					}
					if recoverable &&
						rule != nil && retries < rule.MaxRetries &&
						answerRetryAllowed(nodeAnswerRetryIndex+1) {
						retries++
						nodeAnswerRetryIndex++
						if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
							return
						}
						continue
					}
					if recoverable {
						switched, switchErr := switchAfterFailure()
						if switchErr != nil {
							writeAutoPreparationError(w, requestCtx, switchErr)
							return
						}
						if switched {
							rule = nil
							retries = 0
							continue
						}
					}
					http.Error(w, "upstream stream failed before client commit", http.StatusBadGateway)
					return
				}
				completeAnswer(resp.StatusCode, "success", result.captured)
				captured = result.captured
			} else {
				captured = stream(w, resp)
			}
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", answerAttempts,
				"self_escalations", selfEscalationCount,
				"self_escalation_reason", selfEscalationReason,
				"elapsed", time.Since(start).Round(time.Millisecond))
			if h.stats != nil {
				h.stats.RecordAsync(stats.RequestMeta{
					ProfileID:   h.cfg.ID,
					ProfileSlug: label,
					Protocol:    string(h.cfg.Protocol),
					Kind:        "main",
					Path:        r.URL.Path,
				}, captured, h.parser)
			}
			if isAuto && sessionKeyValid && modelAttemptIndex == sessionBindingModelIndex {
				h.bindRoutingSession(sessionKey, plan, classification, currentAttempt)
			}
			if isAuto {
				h.submitEvaluation(
					r.Header, requestURI, routeRequest, classification, plan,
					currentAttempt, captured, time.Since(start),
					selfEscalationEnabled, selfEscalationCount, selfEscalationModel,
				)
			}
			return
		}

		// Error response: buffer to check for overload
		errBody, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			completeAnswer(resp.StatusCode, "response_io", errBody)
		} else {
			completeAnswer(resp.StatusCode, "upstream", errBody)
		}

		failure := provider.ClassifyHTTPFailure(
			h.cfg.OverloadRules,
			resp.StatusCode,
			errBody,
			nil,
		)
		failureClass, _ := provider.FailureClassOf(failure)
		if matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody); matched != nil &&
			failureClass == provider.FailureOverloadTransient {
			if rule == nil {
				rule = matched
			}
			if retries < rule.MaxRetries &&
				answerRetryAllowed(nodeAnswerRetryIndex+1) {
				retries++
				nodeAnswerRetryIndex++
				if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
					return
				}
				continue
			}
			switched, switchErr := switchAfterFailure()
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

func (h *handler) logRoutingPlanDebug(
	requestTraceID string,
	profileSlug string,
	request routing.Request,
	classification routing.Classification,
	plan routing.ExecutionPlan,
) {
	route := h.cfg.AutoRouting.Strategy.Routes[plan.Route()]
	slog.Debug(
		"routing.debug.classification",
		"request_trace_id", requestTraceID,
		"profile", profileSlug,
		"strategy", plan.Strategy(),
		"task_type", classification.TaskType,
		"difficulty", classification.Difficulty,
		"risk", classification.Risk,
		"confidence_percent", float64(classification.ConfidenceBPS)/100,
		"source", classification.Source,
		"reason_codes", classification.ReasonCodes,
		"route", plan.Route(),
		"uses_default_route", h.routing.UsesDefaultRouteFor(classification.TaskType, classification.Difficulty),
		"estimated_input_tokens", request.Facts.EstimatedInputTokens,
		"requested_output_tokens", request.Facts.RequestedOutputTokens,
		"image_count", request.Facts.ImageCount,
		"has_tools", request.Facts.HasTools,
		"requires_structured_output", request.Facts.RequiresStructuredOutput,
		"stream", request.Facts.Stream,
	)
	for _, candidate := range plan.CandidateDecisions() {
		slog.Debug(
			"routing.debug.candidate",
			"request_trace_id", requestTraceID,
			"profile", profileSlug,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"model", candidate.Model,
			"decision", candidate.Decision,
			"reason_code", candidate.Reason,
			"quality_percent", float64(candidate.QualityScoreBPS)/100,
			"route_min_quality_percent", float64(route.MinQualityBPS)/100,
			"severe_error_percent", float64(candidate.SevereErrorRateBPS)/100,
			"route_max_severe_error_percent", float64(route.MaxSevereErrorRateBPS)/100,
			"expected_cost_usd", debugUSD(candidate.ExpectedCostMicroUSD),
			"answer_worst_cost_usd", debugUSD(candidate.AnswerCallCostMicroUSD),
			"vision_call_cost_usd", debugUSD(candidate.VisionCallCostMicroUSD),
			"vision_mode", candidate.VisionMode,
			"upstream_nodes", candidate.UpstreamNodeIDs,
		)
	}
	for index, attempt := range plan.ModelAttempts() {
		nodes := attempt.Targets()
		nodeIDs := make([]string, 0, len(nodes))
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID())
		}
		slog.Debug(
			"routing.debug.attempt_plan",
			"request_trace_id", requestTraceID,
			"profile", profileSlug,
			"attempt_index", index,
			"model", attempt.Model(),
			"quality_percent", float64(attempt.QualityScoreBPS())/100,
			"expected_cost_usd", debugUSD(attempt.ExpectedCostMicroUSD()),
			"vision_mode", attempt.VisionMode(),
			"upstream_nodes", nodeIDs,
		)
	}
	graph := plan.CallGraph()
	slog.Debug(
		"routing.debug.budget_plan",
		"request_trace_id", requestTraceID,
		"profile", profileSlug,
		"answer_calls", graph.AnswerCalls,
		"auxiliary_calls", graph.AuxiliaryCalls,
		"total_outbound_calls", graph.TotalOutboundCalls,
		"model_switches", graph.ModelSwitches,
		"upstream_node_switches", graph.TargetSwitches,
		"reachable_nodes", len(graph.Attempts),
		"graph_worst_case_cost_usd", debugUSD(graph.WorstCaseCostMicroUSD),
		"request_worst_case_cost_usd", debugUSD(plan.WorstCaseCostMicroUSD()),
		"request_deadline", graph.Deadline,
	)
}

func debugUSD(microUSD int64) float64 {
	return float64(microUSD) / 1_000_000
}

func logRoutingCallDebug(call routing.CallLedgerEntry) {
	slog.Debug(
		"routing.debug.call",
		"request_trace_id", call.CorrelationID,
		"sequence", call.Sequence,
		"kind", call.Kind,
		"model", call.Model,
		"upstream_node", call.Target,
		"image_index", call.ImageIndex,
		"retry_index", call.RetryIndex,
		"model_switch_index", call.ModelSwitchIndex,
		"upstream_node_switch_index", call.TargetSwitchIndex,
		"estimated_cost_usd", debugUSD(call.EstimatedMicroUSD),
		"actual_cost_known", call.ActualCostKnown,
		"actual_cost_usd", debugUSD(call.ActualMicroUSD),
		"input_tokens", call.Usage.InputTokens,
		"output_tokens", call.Usage.OutputTokens,
		"cache_read_tokens", call.Usage.CacheReadTokens,
		"cache_write_tokens", call.Usage.CacheCreationTokens,
		"status_code", call.StatusCode,
		"outcome", call.Outcome,
	)
}

func recoverableTransportFailure(class provider.FailureClass) bool {
	switch class {
	case provider.FailureOperationTimeout, provider.FailureUnknownTransport:
		return true
	default:
		return false
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
	classification routing.Classification,
	attempt routing.ModelAttemptPlan,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.sessions.Bind(ctx, key, routing.SessionBinding{
		ProfileID: h.cfg.ID,
		Route:     plan.Route(), Purpose: routing.SessionPurposeLLM,
		TaskType:   classification.TaskType,
		Difficulty: classification.Difficulty,
		Model:      attempt.Model(), QualityScoreBPS: attempt.QualityScoreBPS(),
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

const maxEvaluationContentBytes = 1 << 20

var errEvaluationCostInvariant = errors.New("routing evaluation cost accounting invariant violated")

type evaluationCostTracker struct {
	mu       sync.Mutex
	reserved int64
	spent    int64
}

func newEvaluationCostTracker(reserved int64) *evaluationCostTracker {
	return &evaluationCostTracker{reserved: reserved}
}

func (t *evaluationCostTracker) charge(cost int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cost < 0 || t.reserved < 0 || t.spent < 0 || t.spent > t.reserved ||
		cost > t.reserved-t.spent {
		return errEvaluationCostInvariant
	}
	t.spent += cost
	return nil
}

func (t *evaluationCostTracker) total() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spent
}

func (h *handler) submitEvaluation(
	headers http.Header,
	requestURI string,
	request routing.Request,
	classification routing.Classification,
	plan routing.ExecutionPlan,
	selected routing.ModelAttemptPlan,
	captured []byte,
	onlineLatency time.Duration,
	selfEscalationAvailable bool,
	selfEscalationCount int,
	selfEscalationModel string,
) {
	config := h.cfg.AutoRouting.DynamicOptimization
	if h.evaluation == nil || !config.Enabled || len(captured) == 0 ||
		len(captured) > maxEvaluationContentBytes || request.Facts.HasTools {
		return
	}
	evaluationModel := selected.Model()
	if selfEscalationCount > 0 && selfEscalationModel != "" {
		evaluationModel = selfEscalationModel
	}
	pair, ok := h.routing.EvaluationPair(request, classification, evaluationModel)
	if !ok {
		return
	}
	onlineCost := evaluationAttemptCost(selected, request.Facts.ImageCount)
	if model, found := h.cfg.Models[evaluationModel]; found {
		if actual, known := responseActualCost(captured, h.parser, model); known &&
			actual <= selected.AnswerCallCostMicroUSD() {
			onlineCost = addEvaluationCost(
				actual,
				multiplyEvaluationCost(selected.VisionCallCostMicroUSD(), request.Facts.ImageCount),
			)
		}
	}
	online := evaluation.ParseModelOutput(
		request.Operation, captured, request.Facts,
		onlineCost,
		onlineLatency.Milliseconds(),
	)
	comparison := evaluation.ComparisonTask{
		ProfileID: h.cfg.ID, Strategy: plan.Strategy(), Route: plan.Route(),
		TaskType: classification.TaskType, Difficulty: string(classification.Difficulty),
		Risk: string(classification.Risk), VisionMode: string(plan.VisionMode()),
		CandidateModel: pair.Candidate.Model(),
		ReferenceModel: pair.Reference.Model(), ReviewerModel: config.ReviewerModel,
		Question: request.EvaluationText(),
	}
	switch {
	case selfEscalationCount > 0 && pair.Candidate.Model() == selfEscalationModel:
		comparison.EscalationObservation = evaluation.EscalationRequested
	case selfEscalationAvailable && selected.Model() == pair.Candidate.Model():
		comparison.EscalationObservation = evaluation.EscalationNotRequested
	}
	switch selected.Model() {
	case pair.Candidate.Model():
		comparison.Candidate = &online
	case pair.Reference.Model():
		comparison.Reference = &online
	default:
		return
	}

	missing := pair.Candidate
	if comparison.Candidate != nil {
		missing = pair.Reference
	}
	reviewer, ok := h.cfg.Models[config.ReviewerModel]
	if !ok || !reviewer.HasContextWindow || !reviewer.HasMaxOutputTokens ||
		reviewer.MaxOutputTokens < 256 {
		return
	}
	requestedOutput := request.Facts.RequestedOutputTokens
	if requestedOutput <= 0 {
		requestedOutput = 4096
	}
	reviewerInputTokens := request.Facts.EstimatedInputTokens + requestedOutput*2 + 512
	if reviewerInputTokens > reviewer.ContextWindow-256 {
		return
	}
	reviewerCost := estimateEvaluationCallCost(reviewerInputTokens, 256, reviewer)
	missingCost := evaluationWorstCaseAttemptCost(
		missing,
		request.Facts.ImageCount,
		h.cfg.OverloadRules,
	)
	estimatedCost := addEvaluationCost(missingCost, reviewerCost)
	if estimatedCost == 0 {
		estimatedCost = 1
	}
	costTracker := newEvaluationCostTracker(estimatedCost)

	forwardedHeaders := evaluationHeaders(headers)
	comparison.Generate = func(ctx context.Context, model string) (evaluation.ModelOutput, error) {
		attempt, found := evaluationPairAttempt(pair, model)
		if !found {
			return evaluation.ModelOutput{}, evaluation.ErrInvalidComparison
		}
		targets := attempt.Targets()
		if len(targets) == 0 {
			return evaluation.ModelOutput{}, evaluation.ErrInvalidComparison
		}
		attemptTarget := targetURL(targets[0].Upstream(), requestURI)
		started := time.Now()
		hardCostBefore := costTracker.total()
		failedOutput := func() evaluation.ModelOutput {
			return evaluation.ModelOutput{
				CostMicroUSD: costTracker.total() - hardCostBefore,
				LatencyMS:    time.Since(started).Milliseconds(),
			}
		}
		reserveVisionCall := func(
			context.Context,
			int,
			int,
		) (vision.CallCompletion, error) {
			if err := costTracker.charge(attempt.VisionCallCostMicroUSD()); err != nil {
				return nil, err
			}
			return func(int, string, []byte) {}, nil
		}
		body, err := h.prepareEvaluationAttempt(
			ctx,
			forwardedHeaders,
			attemptTarget,
			request,
			attempt,
			reserveVisionCall,
		)
		if err != nil {
			return failedOutput(), err
		}
		response, err := h.doEvaluation(
			ctx,
			http.MethodPost,
			attemptTarget,
			forwardedHeaders,
			body,
			func() error { return costTracker.charge(attempt.AnswerCallCostMicroUSD()) },
		)
		if err != nil {
			return failedOutput(), err
		}
		responseBody, err := readEvaluationResponse(response)
		generatedCost := costTracker.total() - hardCostBefore
		if modelConfig, found := h.cfg.Models[attempt.Model()]; found {
			if actual, known := responseActualCost(responseBody, h.parser, modelConfig); known &&
				actual <= attempt.AnswerCallCostMicroUSD() {
				generatedCost -= attempt.AnswerCallCostMicroUSD()
				generatedCost = addEvaluationCost(generatedCost, actual)
			}
		}
		output := evaluation.ParseModelOutput(
			request.Operation, responseBody, request.Facts,
			generatedCost,
			time.Since(started).Milliseconds(),
		)
		if err != nil {
			return output, err
		}
		return output, nil
	}
	reviewerTargets := h.cfg.RoutingTargets(config.ReviewerModel)
	if len(reviewerTargets) == 0 {
		return
	}
	reviewerTarget := targetURL(reviewerTargets[0].Upstream, requestURI)
	comparison.Review = func(ctx context.Context, input evaluation.ReviewInput) (evaluation.ReviewVerdict, error) {
		body, err := evaluation.BuildReviewRequest(request.Operation, config.ReviewerModel, input)
		if err != nil {
			return evaluation.ReviewVerdict{}, err
		}
		reviewerSpent := int64(0)
		response, err := h.doEvaluation(
			ctx,
			http.MethodPost,
			reviewerTarget,
			forwardedHeaders,
			body,
			func() error {
				if err := costTracker.charge(reviewerCost); err != nil {
					return err
				}
				reviewerSpent = reviewerCost
				return nil
			},
		)
		if err != nil {
			return evaluation.ReviewVerdict{ReviewerCostMicroUSD: reviewerSpent}, err
		}
		responseBody, err := readEvaluationResponse(response)
		if err != nil {
			return evaluation.ReviewVerdict{ReviewerCostMicroUSD: reviewerCost}, err
		}
		verdict, err := evaluation.ParseReviewVerdict(request.Operation, responseBody)
		verdict.ReviewerCostMicroUSD = reviewerCost
		if actual, known := responseActualCost(responseBody, h.parser, reviewer); known &&
			actual <= reviewerCost {
			verdict.ReviewerCostMicroUSD = actual
		}
		return verdict, err
	}
	workflow := evaluation.NewWorkflow(nil)
	job := evaluation.Job{
		ProfileID: h.cfg.ID, SampleRateBPS: config.SampleRateBPS,
		DailyBudgetMicroUSD:   config.DailyBudgetMicroUSD,
		EstimatedCostMicroUSD: estimatedCost, MaxConcurrency: config.MaxConcurrency,
		QueueCapacity: config.QueueCapacity, Timeout: config.TaskTimeout,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		Run: func(ctx context.Context) (evaluation.Result, error) {
			result, err := workflow.Evaluate(ctx, comparison)
			spent := costTracker.total()
			if result.SpentMicroUSD < 0 || result.SpentMicroUSD > spent || spent > estimatedCost {
				return evaluation.Result{SpentMicroUSD: spent}, errors.Join(err, errEvaluationCostInvariant)
			}
			return result, err
		},
	}
	result := h.evaluation.Submit(job)
	slog.Info(
		"routing.evaluation.submitted",
		"profile", h.cfg.Slug,
		"strategy", plan.Strategy(),
		"route", plan.Route(),
		"candidate_model", pair.Candidate.Model(),
		"reference_model", pair.Reference.Model(),
		"result", result,
	)
}

func responseActualCost(
	body []byte,
	parser stats.Parser,
	model profile.ModelCapability,
) (int64, bool) {
	if parser == nil {
		return 0, false
	}
	usage, ok := parser.Parse(body)
	if !ok {
		return 0, false
	}
	return routing.ActualCallCost(routing.CallUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		InputPresent: usage.InputPresent, OutputPresent: usage.OutputPresent,
		CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
		InputIncludesCache: usage.InputIncludesCache, Present: true,
	}, model)
}

func (h *handler) prepareEvaluationAttempt(
	ctx context.Context,
	headers http.Header,
	target string,
	request routing.Request,
	attempt routing.ModelAttemptPlan,
	reserveVisionCall vision.CallReservation,
) ([]byte, error) {
	body, err := request.WithModelNonStreaming(attempt.Model())
	if err != nil {
		return nil, err
	}
	if attempt.VisionMode() != routing.VisionComposite {
		return body, nil
	}
	if h.vision == nil {
		return nil, routing.ErrNoCapableModel
	}
	return h.vision.ProcessOperationTargetWithTickets(
		ctx,
		headers,
		request.Operation,
		body,
		target,
		reserveVisionCall,
	)
}

func evaluationPairAttempt(
	pair routing.EvaluationPair,
	model string,
) (routing.ModelAttemptPlan, bool) {
	if pair.Candidate.Model() == model {
		return pair.Candidate, true
	}
	if pair.Reference.Model() == model {
		return pair.Reference, true
	}
	return routing.ModelAttemptPlan{}, false
}

func evaluationHeaders(headers http.Header) http.Header {
	cloned := headers.Clone()
	cloned.Del(routing.SessionIDHeader)
	cloned.Del("Content-Length")
	cloned.Del("Accept-Encoding")
	return cloned
}

func readEvaluationResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxEvaluationContentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxEvaluationContentBytes {
		return nil, errors.New("routing evaluation response exceeded the memory limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("routing evaluation upstream status %d", response.StatusCode)
	}
	return body, nil
}

func evaluationAttemptCost(attempt routing.ModelAttemptPlan, imageCount int) int64 {
	return addEvaluationCost(
		attempt.AnswerCallCostMicroUSD(),
		multiplyEvaluationCost(attempt.VisionCallCostMicroUSD(), imageCount),
	)
}

func evaluationWorstCaseAttemptCost(
	attempt routing.ModelAttemptPlan,
	imageCount int,
	rules []provider.Rule,
) int64 {
	visionCost := multiplyEvaluationCost(attempt.VisionCallCostMicroUSD(), imageCount)
	visionCost = multiplyEvaluationCost64(visionCost, maxEvaluationVisionAttempts(rules))
	return addEvaluationCost(attempt.AnswerCallCostMicroUSD(), visionCost)
}

func maxEvaluationVisionAttempts(rules []provider.Rule) int64 {
	maxRetries := int64(0)
	for _, rule := range rules {
		if !provider.IsRetryableStatus(rule.Status) || rule.MaxRetries <= 0 {
			continue
		}
		retries := int64(rule.MaxRetries)
		if retries > maxRetries {
			maxRetries = retries
		}
	}
	if maxRetries == math.MaxInt64 {
		return math.MaxInt64
	}
	return maxRetries + 1
}

func estimateEvaluationCallCost(
	inputTokens int,
	outputTokens int,
	model profile.ModelCapability,
) int64 {
	return addEvaluationCost(
		estimateEvaluationTokenCost(inputTokens, model.InputPriceMicroUSDPerMillion),
		estimateEvaluationTokenCost(outputTokens, model.OutputPriceMicroUSDPerMillion),
	)
}

func estimateEvaluationTokenCost(tokens int, price int64) int64 {
	if tokens <= 0 || price <= 0 {
		return 0
	}
	if int64(tokens) > math.MaxInt64/price {
		return math.MaxInt64
	}
	product := int64(tokens) * price
	return product/1_000_000 + boolEvaluationCost(product%1_000_000 != 0)
}

func addEvaluationCost(left int64, right int64) int64 {
	if left == math.MaxInt64 || right == math.MaxInt64 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func multiplyEvaluationCost(cost int64, count int) int64 {
	if cost <= 0 || count <= 0 {
		return 0
	}
	if int64(count) > math.MaxInt64/cost {
		return math.MaxInt64
	}
	return cost * int64(count)
}

func multiplyEvaluationCost64(cost int64, count int64) int64 {
	if cost <= 0 || count <= 0 {
		return 0
	}
	if count > math.MaxInt64/cost {
		return math.MaxInt64
	}
	return cost * count
}

func boolEvaluationCost(value bool) int64 {
	if value {
		return 1
	}
	return 0
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
	captured       []byte
	committed      bool
	selfEscalation selfEscalationDecision
	err            error
}

func relayAutoSuccess(
	w http.ResponseWriter,
	resp *http.Response,
	streaming bool,
	operation llmrequest.Operation,
	detectSelfEscalation bool,
) relayResult {
	defer resp.Body.Close()
	if !streaming {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return relayResult{err: err}
		}
		if detectSelfEscalation {
			decision, detectErr := detectSelfEscalationResponse(operation, body)
			if detectErr != nil || decision.Requested {
				return relayResult{
					captured: body, selfEscalation: decision, err: detectErr,
				}
			}
		}
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, err = w.Write(body)
		return relayResult{captured: body, committed: true, err: err}
	}

	flusher, canFlush := w.(http.Flusher)
	var captured bytes.Buffer
	var pending bytes.Buffer
	var streamGuard selfEscalationStreamGuard
	committed := false
	buffer := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			captured.Write(chunk)
			if committed {
				emit := chunk
				if detectSelfEscalation {
					var guardErr error
					emit, guardErr = streamGuard.Filter(chunk)
					if guardErr != nil {
						return relayResult{captured: captured.Bytes(), committed: true, err: guardErr}
					}
				}
				if len(emit) > 0 {
					if _, err := w.Write(emit); err != nil {
						return relayResult{captured: captured.Bytes(), committed: true, err: err}
					}
				}
				if canFlush {
					flusher.Flush()
				}
			} else {
				pending.Write(chunk)
				if pending.Len() > maxPrecommitStreamBytes {
					return relayResult{err: provider.NewFailure(
						provider.FailureRequestProtocolCapability,
						0,
						errors.New("stream exceeded pre-commit buffer limit"),
					)}
				}
				var ready bool
				var decision selfEscalationDecision
				var err error
				if detectSelfEscalation {
					decision, ready, err = detectSelfEscalationStream(
						operation, pending.Bytes(),
					)
				} else {
					ready, err = hasCompleteSSEEvent(pending.Bytes(), operation)
				}
				if err != nil {
					return relayResult{err: err}
				}
				if decision.Requested {
					return relayResult{
						captured: captured.Bytes(), selfEscalation: decision,
					}
				}
				if ready {
					emit := pending.Bytes()
					if detectSelfEscalation {
						emit, err = streamGuard.Filter(emit)
						if err != nil {
							return relayResult{captured: captured.Bytes(), err: err}
						}
					}
					copyHeaders(w.Header(), resp.Header)
					w.WriteHeader(resp.StatusCode)
					if len(emit) > 0 {
						if _, err := w.Write(emit); err != nil {
							return relayResult{captured: captured.Bytes(), committed: true, err: err}
						}
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
					if detectSelfEscalation {
						if remaining := streamGuard.Flush(); len(remaining) > 0 {
							if _, err := w.Write(remaining); err != nil {
								return relayResult{captured: captured.Bytes(), committed: true, err: err}
							}
							if canFlush {
								flusher.Flush()
							}
						}
					}
					return relayResult{captured: captured.Bytes(), committed: true}
				}
				return relayResult{err: errors.New("stream ended before a complete protocol event")}
			}
			return relayResult{captured: captured.Bytes(), committed: committed, err: readErr}
		}
	}
}

func (h *handler) prepareAutoAttempt(
	ctx context.Context,
	headers http.Header,
	target string,
	request routing.Request,
	attempt routing.ModelAttemptPlan,
	nodeLease *autoNodeLease,
	selfEscalation bool,
) ([]byte, bool, error) {
	body, err := request.WithModel(attempt.Model())
	injected := false
	if selfEscalation {
		body, injected, err = request.WithModelAndSelfEscalation(attempt.Model())
	}
	if err != nil {
		return nil, false, err
	}
	if attempt.VisionMode() != routing.VisionComposite {
		return body, injected, nil
	}
	if h.vision == nil {
		return nil, false, routing.ErrNoCapableModel
	}
	body, err = h.vision.ProcessOperationTargetWithTickets(
		ctx,
		headers,
		request.Operation,
		body,
		target,
		nodeLease.visionReservation(h.cfg.Vision.Model),
	)
	return body, injected, err
}

type preparedAutoAttempt struct {
	modelIndex            int
	targetIndex           int
	attempt               routing.ModelAttemptPlan
	targets               []routing.TargetPlan
	target                routing.TargetPlan
	targetURL             string
	body                  []byte
	nodeLease             *autoNodeLease
	selfEscalationEnabled bool
}

func (h *handler) prepareAutoAttemptSequence(
	ctx context.Context,
	headers http.Header,
	requestURI string,
	request routing.Request,
	attempts []routing.ModelAttemptPlan,
	modelIndex int,
	targetIndex int,
	execution *autoExecution,
	visionCached bool,
) (preparedAutoAttempt, error) {
	for modelIndex < len(attempts) {
		attempt := attempts[modelIndex]
		targets := attempt.Targets()
		if targetIndex >= len(targets) {
			if modelIndex+1 >= len(attempts) {
				return preparedAutoAttempt{}, routing.ErrNoCapableModel
			}
			modelIndex++
			targetIndex = 0
			continue
		}
		target := targets[targetIndex]
		attemptTarget := targetURL(target.Upstream(), requestURI)
		nodeLease, reserveErr := execution.reserveNode(
			ctx, modelIndex, targetIndex, visionCached,
		)
		if reserveErr != nil {
			if modelIndex == 0 && targetIndex == 0 {
				return preparedAutoAttempt{}, reserveErr
			}
			if modelIndex+1 < len(attempts) {
				modelIndex++
				targetIndex = 0
				continue
			}
			return preparedAutoAttempt{}, reserveErr
		}
		_, canEscalate := routing.NextStrongerModelAttempt(attempts, modelIndex)
		selfEscalation := h.cfg.AutoRouting.SelfEscalation.Enabled && canEscalate
		body, injected, err := h.prepareAutoAttempt(
			ctx,
			headers,
			attemptTarget,
			request,
			attempt,
			nodeLease,
			selfEscalation,
		)
		if err == nil {
			nodeLease.releaseUnusedVision()
			return preparedAutoAttempt{
				modelIndex: modelIndex, targetIndex: targetIndex,
				attempt: attempt, targets: targets, target: target,
				targetURL: attemptTarget, body: body, nodeLease: nodeLease,
				selfEscalationEnabled: injected,
			}, nil
		}
		nodeLease.release()
		if ctx.Err() != nil || !recoverableVisionFailure(err) {
			return preparedAutoAttempt{}, err
		}
		if targetIndex+1 < len(targets) {
			slog.Info(
				"routing.vision.target_fallback",
				"profile", h.cfg.Slug,
				"model", attempt.Model(),
				"from_target", target.ID(),
				"to_target", targets[targetIndex+1].ID(),
			)
			targetIndex++
			continue
		}
		if modelIndex+1 >= len(attempts) {
			return preparedAutoAttempt{}, err
		}
		slog.Info(
			"routing.vision.model_fallback",
			"profile", h.cfg.Slug,
			"from_model", attempt.Model(),
			"to_model", attempts[modelIndex+1].Model(),
		)
		modelIndex++
		targetIndex = 0
	}
	return preparedAutoAttempt{}, routing.ErrNoCapableModel
}

func recoverableVisionFailure(err error) bool {
	if errors.Is(err, routing.ErrAttemptBudgetExceeded) {
		return false
	}
	class, ok := provider.FailureClassOf(err)
	if !ok {
		return false
	}
	switch class {
	case provider.FailureOverloadTransient,
		provider.FailureOperationTimeout,
		provider.FailureUnknownTransport,
		provider.FailureMalformedResponse:
		return true
	default:
		return false
	}
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
	if class, ok := provider.FailureClassOf(err); ok &&
		(class == provider.FailureAuthentication ||
			class == provider.FailureRequestProtocolCapability) {
		upstreamStatus := provider.FailureStatus(err)
		if upstreamStatus >= 400 && upstreamStatus <= 499 {
			status = upstreamStatus
		}
	}
	http.Error(w, err.Error(), status)
}

func targetURL(upstream, requestURI string) string {
	return strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(requestURI, "/")
}

func requestVisionOperation(
	protocol profile.Protocol,
	r *http.Request,
) (llmrequest.Operation, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	path := r.URL.EscapedPath()
	operation := llmrequest.Operation("")
	switch protocol {
	case profile.ProtocolAnthropic:
		if path != "/v1/messages" {
			return "", false
		}
		operation = llmrequest.OperationAnthropicMessages
	case profile.ProtocolOpenAI:
		switch path {
		case "/responses", "/v1/responses":
			operation = llmrequest.OperationOpenAIResponses
		case "/v1/chat/completions":
			operation = llmrequest.OperationOpenAIChatCompletions
		default:
			return "", false
		}
	default:
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return operation, err == nil && mediaType == "application/json"
}

func (h *handler) do(ctx context.Context, method, url string, headers http.Header, body []byte) (*http.Response, error) {
	req, err := newProxyRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	return h.client.Do(req)
}

func (h *handler) doEvaluation(
	ctx context.Context,
	method string,
	url string,
	headers http.Header,
	body []byte,
	charge func() error,
) (*http.Response, error) {
	req, err := newProxyRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	transport := h.client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{
		Transport:     chargingRoundTripper{base: transport, charge: charge},
		CheckRedirect: h.client.CheckRedirect,
		Jar:           h.client.Jar,
		Timeout:       h.client.Timeout,
	}
	return client.Do(req)
}

type chargingRoundTripper struct {
	base   http.RoundTripper
	charge func() error
}

func (t chargingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	if t.charge != nil {
		if err := t.charge(); err != nil {
			return nil, err
		}
	}
	return t.base.RoundTrip(request)
}

func newProxyRequest(
	ctx context.Context,
	method string,
	url string,
	headers http.Header,
	body []byte,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, headers)
	return req, nil
}

func proxyHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.Jar = nil
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &cloned
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
