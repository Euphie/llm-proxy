// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/Euphie/llm-proxy/internal/agenttrajectory"
	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/routing"
	"github.com/Euphie/llm-proxy/internal/stats"
	"github.com/Euphie/llm-proxy/internal/vision"
)

var errBodyTooLarge = errors.New("body exceeds memory limit")

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
	return NewWithAgentTrajectories(cfg, client, sdb, sessions, evaluations, nil)
}

type agentTrajectoryRuntime interface {
	Observe(context.Context, agenttrajectory.Observation) (agenttrajectory.CompletedTrajectory, bool, error)
	Queue(context.Context, int64) error
	BeginEvaluation(context.Context, int64) error
	Skip(context.Context, int64, string) error
	ApplySubmitResult(context.Context, int64, evaluation.SubmitResult) error
	ApplyTerminal(context.Context, int64, evaluation.TerminalResult) error
}

func NewWithAgentTrajectories(
	cfg profile.Runtime,
	client *http.Client,
	sdb *stats.DB,
	sessions *routing.SessionStore,
	evaluations evaluationSubmitter,
	agentTrajectories agentTrajectoryRuntime,
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
		cfg:               cfg,
		client:            client,
		stats:             sdb,
		parser:            stats.NewParser(string(cfg.Protocol)),
		vision:            visionPreprocessor,
		routing:           routeEngine,
		sessions:          sessions,
		evaluation:        evaluations,
		agentTrajectories: agentTrajectories,
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
	agentTrajectories  agentTrajectoryRuntime
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
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)
	requestCtx, requestTraceID, err := vision.NewRequestTrace(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	sessionID := singleSessionID(r.Header)
	r.Header.Del(routing.SessionIDHeader)

	body, err := readBodyWithLimit(r.Body, r.ContentLength, maxClientRequestBodyBytes)
	_ = r.Body.Close()
	if errors.Is(err, errBodyTooLarge) {
		http.Error(w, "request body exceeds 64 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	originalBody := body
	if requestedModel, ok := routing.RequestedModel(body); ok {
		status := h.cfg.ModelStatuses[requestedModel]
		if status == modeldirectory.StatusOffline || status == modeldirectory.StatusRetired {
			writeModelOffline(w, requestedModel, status)
			return
		}
	}

	var budget *routing.AttemptBudget
	var plan routing.ExecutionPlan
	var classification routing.Classification
	var routeRequest routing.Request
	var modelAttempts []routing.ModelAttemptPlan
	var currentAttempt routing.ModelAttemptPlan
	var autoExecutor *autoExecution
	var currentNodeLease *autoNodeLease
	var callLedger *routing.CallLedger
	initialModel := ""
	visionCached := false
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
	var routeTaskFingerprint []byte
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
				"answer_attempts", snapshot.AnswerAttempts,
				"auxiliary_calls", snapshot.AuxiliaryCalls,
				"total_outbound_calls", snapshot.TotalOutboundCalls,
				"model_switches", snapshot.ModelSwitches,
				"estimated_consumed_usd", debugUSD(aggregate.EstimatedConsumedMicroUSD),
				"known_actual_usd", debugUSD(aggregate.KnownActualMicroUSD),
				"all_actual_costs_known", aggregate.AllActualCostsKnown,
				"elapsed_ms", time.Since(start).Milliseconds(),
			)
			if h.stats == nil {
				return
			}
			var traceSessionKey []byte
			if sessionKeyValid {
				traceSessionKey = append([]byte(nil), sessionKey[:]...)
			}
			h.stats.RecordRoutingTraceWithCallsAndCandidatesAsync(stats.RoutingTrace{
				CorrelationID: aggregate.CorrelationID,
				SessionKey:    traceSessionKey,
				ProfileID:     h.cfg.ID, ProfileSlug: label,
				Protocol: string(h.cfg.Protocol), Path: r.URL.Path,
				Strategy: plan.Strategy(), Route: plan.Route(),
				TaskType: classification.TaskType, Difficulty: string(classification.Difficulty),
				Risk: string(classification.Risk), ClassificationSource: string(classification.Source),
				ClassificationConfidenceBPS:  classification.ConfidenceBPS,
				TaskTypeConfidenceBPS:        classification.TaskTypeConfidenceBPS,
				DifficultyConfidenceBPS:      classification.DifficultyConfidenceBPS,
				RiskConfidenceBPS:            classification.RiskConfidenceBPS,
				ClassificationUnderspecified: classification.Underspecified,
				ComplexitySignals:            classification.ComplexitySignals.Map(),
				ClassificationReasonCodes:    append([]string(nil), classification.ReasonCodes...),
				EstimatedInputTokens:         routeRequest.Facts.EstimatedInputTokens,
				RequestedOutputTokens:        routeRequest.Facts.RequestedOutputTokens,
				DecisionReason:               plan.Reason(),
				InitialModel:                 initialModel, FinalModel: currentAttempt.Model(),
				VisionMode:                    string(currentAttempt.VisionMode()),
				StatusCode:                    responseState.statusCode,
				ClientCommitted:               responseState.committed,
				AnswerAttempts:                snapshot.AnswerAttempts,
				AuxiliaryCalls:                snapshot.AuxiliaryCalls,
				TotalOutboundCalls:            snapshot.TotalOutboundCalls,
				ModelSwitches:                 snapshot.ModelSwitches,
				SelfEscalations:               selfEscalationCount,
				SelfEscalationReason:          selfEscalationReason,
				PlannedWorstCaseCostMicroUSD:  plan.WorstCaseCostMicroUSD(),
				ConsumedEstimatedCostMicroUSD: aggregate.EstimatedConsumedMicroUSD,
				HeldCostMicroUSD:              snapshot.HeldCostMicroUSD,
				KnownActualCostMicroUSD:       aggregate.KnownActualMicroUSD,
				AllActualCostsKnown:           aggregate.AllActualCostsKnown,
				ElapsedMilliseconds:           time.Since(start).Milliseconds(),
				RuntimeRevision:               h.cfg.RuntimeRevision,
				PolicyVersionID:               h.cfg.ActivePolicyVersionID,
				ModelCatalogRevision:          h.cfg.ModelCatalogRevision,
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
				binding, found, err := h.sessions.GetForRuntime(
					requestCtx, key, h.cfg.RuntimeRevision,
					h.cfg.ActivePolicyVersionID, h.cfg.ModelCatalogRevision,
				)
				if err != nil {
					slog.Warn("routing.session.read_failed", "profile", label, "error", err)
				} else if found {
					if fingerprint, ok := routeRequest.TaskFingerprint(key); ok {
						routeTaskFingerprint = append([]byte(nil), fingerprint[:]...)
						preference.TaskContinuation = bytes.Equal(binding.TaskFingerprint, fingerprint[:])
					} else if len(binding.TaskFingerprint) == len(key) {
						routeTaskFingerprint = append([]byte(nil), binding.TaskFingerprint...)
						preference.TaskContinuation = true
					}
					preference.TaskType = binding.TaskType
					preference.Difficulty = binding.Difficulty
					preference.RouteID = binding.Route
					preference.Model = binding.Model
					preference.MinQualityScoreBPS = binding.QualityScoreBPS
					preference.Strategy = binding.Strategy
					preference.ModelLocked = binding.ModelLocked
				}
				if len(routeTaskFingerprint) == 0 {
					if fingerprint, ok := routeRequest.TaskFingerprint(key); ok {
						routeTaskFingerprint = append([]byte(nil), fingerprint[:]...)
					}
				}
			}
		}
		plan, classification, routeErr = h.routing.RouteWithPreference(
			requestCtx, r.Header, routeRequest, budget, preference,
		)
		if routeErr != nil {
			if writeRequestContextError(w, r.Context(), requestCtx) {
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
		initialModel = currentAttempt.Model()
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
			autoExecutor,
			visionCached,
		)
		if prepareErr != nil {
			writeAutoPreparationError(w, r.Context(), requestCtx, prepareErr)
			return
		}
		modelAttemptIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
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
			if writeRequestContextError(w, r.Context(), requestCtx) {
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
	if writeRequestContextError(w, r.Context(), requestCtx) {
		return
	}
	answerAttempts := 0
	observedModelPath := make([]string, 0, len(modelAttempts))
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
			autoExecutor,
			visionCached,
		)
		if err != nil {
			return false, err
		}
		modelAttemptIndex = prepared.modelIndex
		sessionBindingModelIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
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
		nextModelIndex := modelAttemptIndex + 1
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
			autoExecutor,
			visionCached,
		)
		if err != nil {
			return false, err
		}
		modelAttemptIndex = prepared.modelIndex
		currentAttempt = prepared.attempt
		body = prepared.body
		currentNodeLease = prepared.nodeLease
		selfEscalationEnabled = prepared.selfEscalationEnabled
		visionCached = visionCached || currentAttempt.VisionMode() == routing.VisionComposite
		nodeAnswerRetryIndex = 0
		slog.Info("routing.model.switched",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"from_model", previousModel,
			"to_model", currentAttempt.Model(),
			"vision_mode", currentAttempt.VisionMode())
		return true, nil
	}

	var rule *provider.Rule
	retries := 0
	answerRetryAllowed := func(retryIndex int) bool {
		return !isAuto || currentNodeLease.allowsAnswerRetry(retryIndex)
	}
	for {
		observedModelPath = appendObservedModel(observedModelPath, currentAttempt.Model())
		completeAnswer := func(int, string, []byte) {}
		if isAuto {
			if h.beforeAutoAnswer != nil {
				h.beforeAutoAnswer(requestCtx)
			}
			_, completion, ticketErr := currentNodeLease.beginAnswer(
				requestCtx, nodeAnswerRetryIndex,
			)
			if ticketErr != nil {
				if writeRequestContextError(w, r.Context(), requestCtx) {
					return
				}
				writeRoutingError(w, ticketErr)
				return
			}
			completeAnswer = completion
		}
		answerAttempts++
		resp, err := h.do(requestCtx, r.Method, target, r.Header, body)
		if err != nil {
			completeAnswer(0, "network", nil)
			if writeRequestContextError(w, r.Context(), requestCtx) {
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
					writeRequestContextError(w, r.Context(), requestCtx)
					return
				}
				continue
			}
			if recoverable {
				switched, switchErr := switchAfterFailure()
				if switchErr != nil {
					writeAutoPreparationError(w, r.Context(), requestCtx, switchErr)
					return
				}
				if switched {
					rule = nil
					retries = 0
					continue
				}
			}
			slog.Error("upstream request failed",
				"profile", label, "model", currentAttempt.Model(), "err", err)
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
						writeAutoPreparationError(w, r.Context(), requestCtx, switchErr)
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
					if writeRequestContextError(w, r.Context(), requestCtx) {
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
							writeRequestContextError(w, r.Context(), requestCtx)
							return
						}
						continue
					}
					if recoverable {
						switched, switchErr := switchAfterFailure()
						if switchErr != nil {
							writeAutoPreparationError(w, r.Context(), requestCtx, switchErr)
							return
						}
						if switched {
							rule = nil
							retries = 0
							continue
						}
					}
					http.Error(w, "upstream response failed before client commit", http.StatusBadGateway)
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
			if isAuto && sessionKeyValid && modelAttemptIndex == sessionBindingModelIndex &&
				classification.Source != routing.ClassificationSourceFallback {
				cacheReadTokens := 0
				if usage, ok := h.parser.Parse(captured); ok {
					cacheReadTokens = usage.CacheReadTokens
				}
				h.bindRoutingSession(
					sessionKey, routeTaskFingerprint, routeRequest.Facts.ConversationTokens,
					cacheReadTokens,
					plan, classification, currentAttempt,
				)
			}
			if isAuto {
				if routeRequest.Facts.HasTools && h.agentTrajectories != nil {
					var trajectorySessionKey *routing.SessionKey
					if sessionKeyValid {
						trajectorySessionKey = &sessionKey
					}
					h.submitAgentTrajectory(
						requestCtx, r.Header, requestURI, routeRequest, classification, plan,
						currentAttempt, observedModelPath, originalBody, captured, time.Since(start),
						trajectorySessionKey, selfEscalationCount, selfEscalationModel,
						selfEscalationReason,
					)
				} else {
					h.submitEvaluation(
						r.Header, requestURI, routeRequest, classification, plan,
						currentAttempt, captured, time.Since(start),
						selfEscalationEnabled, selfEscalationCount, selfEscalationModel,
					)
				}
			}
			return
		}

		// Error response: buffer to check for overload
		errBody, readErr := readBodyWithLimit(
			resp.Body, resp.ContentLength, maxBufferedUpstreamResponseBytes,
		)
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			completeAnswer(resp.StatusCode, "response_io", errBody)
			if writeRequestContextError(w, r.Context(), requestCtx) {
				return
			}
			message := "failed to read upstream response"
			if errors.Is(readErr, errBodyTooLarge) {
				message = "upstream response exceeded the safe buffer limit"
			}
			http.Error(w, message, http.StatusBadGateway)
			return
		}
		completeAnswer(resp.StatusCode, "upstream", errBody)

		failure := provider.ClassifyHTTPFailure(
			h.cfg.OverloadRules,
			resp.StatusCode,
			errBody,
			nil,
		)
		failureClass, _ := provider.FailureClassOf(failure)
		if failureClass == provider.FailureOverloadTransient {
			matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody)
			if rule == nil && matched != nil {
				rule = matched
			}
			if rule != nil && retries < rule.MaxRetries &&
				answerRetryAllowed(nodeAnswerRetryIndex+1) {
				retries++
				nodeAnswerRetryIndex++
				if !waitForRetry(requestCtx, label, r.URL.Path, rule, retries) {
					writeRequestContextError(w, r.Context(), requestCtx)
					return
				}
				continue
			}
			switched, switchErr := switchAfterFailure()
			if switchErr != nil {
				writeAutoPreparationError(w, r.Context(), requestCtx, switchErr)
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
		"task_type_confidence_percent", float64(classification.TaskTypeConfidenceBPS)/100,
		"difficulty_confidence_percent", float64(classification.DifficultyConfidenceBPS)/100,
		"risk_confidence_percent", float64(classification.RiskConfidenceBPS)/100,
		"classification_underspecified", classification.Underspecified,
		"complexity_signals", classification.ComplexitySignals.Map(),
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
			"stability_percent", float64(candidate.StabilityScoreBPS)/100,
			"route_min_stability_percent", float64(route.MinStabilityBPS)/100,
			"severe_error_percent", float64(candidate.SevereErrorRateBPS)/100,
			"route_max_severe_error_percent", float64(route.MaxSevereErrorRateBPS)/100,
			"cost_efficiency_percent", float64(candidate.CostEfficiencyScoreBPS)/100,
			"performance_percent", float64(candidate.PerformanceScoreBPS)/100,
			"routing_score_percent", float64(candidate.RoutingScoreBPS)/100,
			"expected_latency_ms", candidate.ExpectedLatencyMS,
			"expected_cost_usd", debugUSD(candidate.ExpectedCostMicroUSD),
			"answer_worst_cost_usd", debugUSD(candidate.AnswerCallCostMicroUSD),
			"vision_call_cost_usd", debugUSD(candidate.VisionCallCostMicroUSD),
			"vision_mode", candidate.VisionMode,
		)
	}
	for index, attempt := range plan.ModelAttempts() {
		slog.Debug(
			"routing.debug.attempt_plan",
			"request_trace_id", requestTraceID,
			"profile", profileSlug,
			"attempt_index", index,
			"model", attempt.Model(),
			"quality_percent", float64(attempt.QualityScoreBPS())/100,
			"expected_cost_usd", debugUSD(attempt.ExpectedCostMicroUSD()),
			"vision_mode", attempt.VisionMode(),
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
		"image_index", call.ImageIndex,
		"retry_index", call.RetryIndex,
		"model_switch_index", call.ModelSwitchIndex,
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
	for _, name := range []string{
		routing.SessionIDHeader,
		routing.ClaudeCodeSessionIDHeader,
		routing.CodexSessionIDHeader,
		routing.CodexThreadIDHeader,
	} {
		values := headers.Values(name)
		if len(values) > 1 {
			return ""
		}
		if len(values) == 1 && values[0] != "" {
			return values[0]
		}
	}
	return ""
}

func (h *handler) bindRoutingSession(
	key routing.SessionKey,
	taskFingerprint []byte,
	conversationTokens int,
	cacheReadTokens int,
	plan routing.ExecutionPlan,
	classification routing.Classification,
	attempt routing.ModelAttemptPlan,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.sessions.BindWithLockPolicy(ctx, key, routing.SessionBinding{
		ProfileID: h.cfg.ID,
		Route:     plan.Route(), Purpose: routing.SessionPurposeLLM,
		TaskType:   classification.TaskType,
		Difficulty: classification.Difficulty,
		Model:      attempt.Model(), QualityScoreBPS: attempt.QualityScoreBPS(),
		Strategy:                    plan.Strategy(),
		TaskFingerprint:             append([]byte(nil), taskFingerprint...),
		ClassificationConfidenceBPS: classification.ConfidenceBPS,
		ConversationTokens:          conversationTokens,
		CacheReadTokens:             cacheReadTokens,
		ClassificationReliable: classification.Source == routing.ClassificationSourceAnalyzer &&
			!classification.Underspecified && classification.TaskType != "unknown" &&
			classification.Difficulty != routing.DifficultyUnknown && classification.Risk != routing.RiskUnknown,
		HighestModel:         attempt.Model() == h.cfg.AutoRouting.StrongBaselineModel,
		RuntimeRevision:      h.cfg.RuntimeRevision,
		PolicyVersionID:      h.cfg.ActivePolicyVersionID,
		ModelCatalogRevision: h.cfg.ModelCatalogRevision,
	}, h.cfg.AutoRouting.SessionTTL, routing.SessionLockPolicy{
		TokenThreshold: h.cfg.AutoRouting.SessionLockTokenThreshold,
	}); err != nil {
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

func (h *handler) reviewEvaluationWithRepair(
	ctx context.Context,
	operation routing.Operation,
	target string,
	headers http.Header,
	model string,
	input evaluation.ReviewInput,
	reviewerCost int64,
	reviewerRepairCost int64,
	reviewer profile.ModelCapability,
	costTracker *evaluationCostTracker,
) (evaluation.ReviewVerdict, error) {
	call := func(repair bool) (evaluation.ReviewVerdict, int64, error) {
		var body []byte
		var err error
		if repair {
			body, err = evaluation.BuildReviewRepairRequest(operation, model, input)
		} else {
			body, err = evaluation.BuildReviewRequest(operation, model, input)
		}
		if err != nil {
			return evaluation.ReviewVerdict{}, 0, err
		}
		callCost := reviewerCost
		if repair {
			callCost = reviewerRepairCost
		}
		charged := false
		response, err := h.doEvaluation(
			ctx, http.MethodPost, target, headers, body,
			func() error {
				if err := costTracker.charge(callCost); err != nil {
					return err
				}
				charged = true
				return nil
			},
		)
		spent := int64(0)
		if charged {
			spent = callCost
		}
		if err != nil {
			return evaluation.ReviewVerdict{}, spent, err
		}
		responseBody, err := readEvaluationResponse(response)
		if err != nil {
			return evaluation.ReviewVerdict{}, spent, err
		}
		if actual, known := responseActualCost(responseBody, h.parser, reviewer); known && actual <= callCost {
			spent = actual
		}
		verdict, err := evaluation.ParseReviewVerdict(operation, responseBody)
		verdict.ReviewerCostMicroUSD = spent
		return verdict, spent, err
	}

	verdict, spent, err := call(false)
	if err == nil {
		return verdict, nil
	}
	if evaluation.ReviewFailureCodeOf(err) == "" {
		verdict.ReviewerCostMicroUSD = spent
		return verdict, err
	}
	slog.Info(
		"routing.evaluation.review_repair",
		"profile", h.cfg.Slug,
		"reviewer_model", model,
		"reason", evaluation.ReviewFailureCodeOf(err),
	)
	repaired, repairSpent, repairErr := call(true)
	repaired.ReviewerCostMicroUSD = addEvaluationCost(spent, repairSpent)
	if repairErr != nil {
		return repaired, evaluation.RepairReviewFailure(repairErr)
	}
	return repaired, nil
}

func (h *handler) submitAgentTrajectory(
	ctx context.Context,
	headers http.Header,
	requestURI string,
	request routing.Request,
	classification routing.Classification,
	plan routing.ExecutionPlan,
	selected routing.ModelAttemptPlan,
	modelPath []string,
	requestBody []byte,
	captured []byte,
	onlineLatency time.Duration,
	sessionKey *routing.SessionKey,
	selfEscalationCount int,
	selfEscalationModel string,
	selfEscalationReason string,
) {
	entries := routing.CallLedgerFromContext(ctx).Snapshot()
	onlineCost, finalOutputCost, finalOutputLatency, ledgerBacked := trajectoryLedgerCosts(entries, selected.Model())
	if !ledgerBacked {
		onlineCost = trajectoryPathCost(plan, modelPath, request.Facts.ImageCount)
		if onlineCost == 0 {
			onlineCost = evaluationAttemptCost(selected, request.Facts.ImageCount)
		}
		finalOutputCost = evaluationAttemptCost(selected, request.Facts.ImageCount)
		finalOutputLatency = onlineLatency.Milliseconds()
	}
	if !ledgerBacked {
		model, found := h.cfg.Models[selected.Model()]
		if found {
			if actual, known := responseActualCost(captured, h.parser, model); known &&
				actual <= selected.AnswerCallCostMicroUSD() {
				selectedEstimate := evaluationAttemptCost(selected, request.Facts.ImageCount)
				actualSelected := addEvaluationCost(
					actual,
					multiplyEvaluationCost(selected.VisionCallCostMicroUSD(), request.Facts.ImageCount),
				)
				if selectedEstimate <= onlineCost {
					onlineCost = addEvaluationCost(onlineCost-selectedEstimate, actualSelected)
				}
				finalOutputCost = actualSelected
			}
		}
	}
	selfEscalations := []agenttrajectory.SelfEscalation(nil)
	if selfEscalationCount > 0 && selfEscalationModel != "" && selfEscalationModel != selected.Model() {
		selfEscalations = append(selfEscalations, agenttrajectory.SelfEscalation{
			FromModel: selfEscalationModel, ToModel: selected.Model(), Reason: selfEscalationReason,
		})
	}
	completed, terminal, err := h.agentTrajectories.Observe(ctx, agenttrajectory.Observation{
		ProfileID: h.cfg.ID, ProfileSlug: h.cfg.Slug, Protocol: request.Operation,
		SessionKey: sessionKey, Strategy: plan.Strategy(), Route: plan.Route(),
		TaskType: classification.TaskType, Difficulty: string(classification.Difficulty),
		Risk: string(classification.Risk), VisionMode: string(plan.VisionMode()),
		Model: selected.Model(), ModelPath: modelPath, SelfEscalations: selfEscalations,
		RequestBody: requestBody, ResponseBody: captured,
		StartedAt: time.Now().Add(-onlineLatency), LatencyMS: onlineLatency.Milliseconds(),
		CandidateCostMicroUSD:   onlineCost,
		FinalOutputCostMicroUSD: finalOutputCost, FinalOutputLatencyMS: finalOutputLatency,
		FinalOutputMetricsKnown: true,
	})
	if err != nil {
		slog.Warn("routing.agent_trajectory.observe_failed", "profile", h.cfg.Slug, "error", err)
		return
	}
	if !terminal || !completed.Eligible {
		return
	}
	recordID := completed.Record.ID
	config := h.cfg.AutoRouting.DynamicOptimization
	if !config.Enabled {
		h.skipAgentTrajectory(ctx, recordID, "evaluation_disabled")
		return
	}
	if h.evaluation == nil {
		h.skipAgentTrajectory(ctx, recordID, "evaluation_service_unavailable")
		return
	}
	evaluationModel, onlineModel, ok := trajectoryEvaluationModels(
		completed.Record.ModelPath, h.cfg.AutoRouting.StrongBaselineModel,
	)
	if !ok {
		h.skipAgentTrajectory(ctx, recordID, "ambiguous_model_path")
		return
	}
	pair, ok := h.routing.EvaluationPair(request, classification, evaluationModel)
	if !ok {
		h.skipAgentTrajectory(ctx, recordID, "no_evaluation_pair")
		return
	}
	reviewer, ok := h.cfg.Models[config.ReviewerModel]
	if !ok || !reviewer.HasContextWindow || !reviewer.HasMaxOutputTokens ||
		reviewer.MaxOutputTokens < evaluation.ReviewRepairMaxOutputTokens {
		h.skipAgentTrajectory(ctx, recordID, "reviewer_unavailable")
		return
	}

	comparison := evaluation.ComparisonTask{
		ProfileID: h.cfg.ID, Strategy: completed.Record.Strategy, Route: completed.Record.Route,
		TaskType: completed.Record.TaskType, Difficulty: completed.Record.Difficulty,
		Risk: completed.Record.Risk, VisionMode: completed.Record.VisionMode,
		CandidateModel: pair.Candidate.Model(), ReferenceModel: pair.Reference.Model(),
		ReviewerModel: config.ReviewerModel,
		Context:       agenttrajectory.ReviewContext(completed.Plaintext, 2<<20),
		Question:      trajectoryQuestion(completed.Plaintext),
	}
	if len(completed.Plaintext.SelfEscalations) > 0 {
		comparison.EscalationObservation = evaluation.EscalationRequested
	}
	onlineCost = completed.Record.CandidateCostMicroUSD
	onlineLatencyMS := completed.Record.ElapsedMS
	if completed.Plaintext.FinalOutputMetricsKnown {
		onlineCost = completed.Plaintext.FinalOutputCostMicroUSD
		onlineLatencyMS = completed.Plaintext.FinalOutputLatencyMS
	}
	online := evaluation.ModelOutput{
		Text: completed.Plaintext.FinalText, CostMicroUSD: onlineCost,
		LatencyMS: onlineLatencyMS,
	}
	switch onlineModel {
	case pair.Candidate.Model():
		comparison.Candidate = &online
	case pair.Reference.Model():
		comparison.Reference = &online
	default:
		h.skipAgentTrajectory(ctx, recordID, "selected_model_not_in_pair")
		return
	}
	missing := pair.Candidate
	if comparison.Candidate != nil {
		missing = pair.Reference
	}
	missingModel, ok := h.cfg.Models[missing.Model()]
	if !ok || !missingModel.HasContextWindow || !missingModel.HasMaxOutputTokens || missingModel.MaxOutputTokens < 2048 {
		h.skipAgentTrajectory(ctx, recordID, "reference_model_unavailable")
		return
	}
	referenceInputTokens := estimateEvaluationTextTokens(comparison.Context) + 1024
	if referenceInputTokens+2048 > missingModel.ContextWindow {
		h.skipAgentTrajectory(ctx, recordID, "reference_context_too_large")
		return
	}
	reviewerInputTokens := referenceInputTokens + estimateEvaluationTextTokens(online.Text) + 2048 + 512
	if reviewerInputTokens+evaluation.ReviewRepairMaxOutputTokens > reviewer.ContextWindow {
		h.skipAgentTrajectory(ctx, recordID, "reviewer_context_too_large")
		return
	}
	reviewerCost := estimateEvaluationCallCost(reviewerInputTokens, evaluation.ReviewMaxOutputTokens, reviewer)
	reviewerRepairCost := estimateEvaluationCallCost(
		reviewerInputTokens, evaluation.ReviewRepairMaxOutputTokens, reviewer,
	)
	missingCost := estimateEvaluationCallCost(referenceInputTokens, 2048, missingModel)
	estimatedCost := addEvaluationCost(missingCost, addEvaluationCost(reviewerCost, reviewerRepairCost))
	if estimatedCost <= 0 || estimatedCost == math.MaxInt64 {
		h.skipAgentTrajectory(ctx, recordID, "evaluation_cost_unavailable")
		return
	}
	if config.DailyBudgetMicroUSD <= 0 || estimatedCost > config.DailyBudgetMicroUSD {
		h.skipAgentTrajectory(ctx, recordID, "evaluation_budget_too_small")
		return
	}
	costTracker := newEvaluationCostTracker(estimatedCost)
	forwardedHeaders := evaluationHeaders(headers)
	target := targetURL(h.cfg.Upstream, requestURI)
	comparison.Generate = func(ctx context.Context, model string) (evaluation.ModelOutput, error) {
		_, found := evaluationPairAttempt(pair, model)
		if !found || model != missing.Model() {
			return evaluation.ModelOutput{}, evaluation.ErrInvalidComparison
		}
		body, err := agenttrajectory.BuildReferenceRequest(request.Operation, model, completed.Plaintext)
		if err != nil {
			return evaluation.ModelOutput{}, err
		}
		started := time.Now()
		spentBefore := costTracker.total()
		failedOutput := func() evaluation.ModelOutput {
			return evaluation.ModelOutput{
				CostMicroUSD: costTracker.total() - spentBefore,
				LatencyMS:    time.Since(started).Milliseconds(),
			}
		}
		response, err := h.doEvaluation(
			ctx, http.MethodPost, target, forwardedHeaders, body,
			func() error { return costTracker.charge(missingCost) },
		)
		if err != nil {
			return failedOutput(), err
		}
		responseBody, err := readEvaluationResponse(response)
		generatedCost := costTracker.total() - spentBefore
		if config, found := h.cfg.Models[model]; found {
			if actual, known := responseActualCost(responseBody, h.parser, config); known &&
				actual <= missingCost {
				generatedCost = actual
			}
		}
		output, parseErr := agenttrajectory.ParseTextOnlyEvaluationOutput(
			request.Operation, responseBody, routing.RequestFacts{}, generatedCost,
			time.Since(started).Milliseconds(),
		)
		if err != nil {
			return output, err
		}
		return output, parseErr
	}
	comparison.Review = func(ctx context.Context, input evaluation.ReviewInput) (evaluation.ReviewVerdict, error) {
		return h.reviewEvaluationWithRepair(
			ctx, request.Operation, target, forwardedHeaders, config.ReviewerModel,
			input, reviewerCost, reviewerRepairCost, reviewer, costTracker,
		)
	}
	stateCtx, cancelState := trajectoryStateContext(ctx)
	err = h.agentTrajectories.Queue(stateCtx, recordID)
	cancelState()
	if err != nil {
		slog.Warn("routing.agent_trajectory.queue_state_failed", "profile", h.cfg.Slug, "trajectory_id", recordID, "error", err)
		h.skipAgentTrajectory(ctx, recordID, "queue_state_failed")
		return
	}
	workflow := evaluation.NewWorkflow(nil)
	job := evaluation.Job{
		ProfileID: h.cfg.ID, SampleRateBPS: config.SampleRateBPS,
		Sampling: evaluation.SamplingKey{
			ProfileID: h.cfg.ID, Strategy: completed.Record.Strategy, Route: completed.Record.Route,
			TaskType: completed.Record.TaskType, Difficulty: completed.Record.Difficulty,
			Risk: completed.Record.Risk, VisionMode: completed.Record.VisionMode,
			CandidateModel: pair.Candidate.Model(), ReferenceModel: pair.Reference.Model(),
		},
		DailyBudgetMicroUSD: config.DailyBudgetMicroUSD, EstimatedCostMicroUSD: estimatedCost,
		MaxConcurrency: config.MaxConcurrency, QueueCapacity: config.QueueCapacity,
		Timeout: config.TaskTimeout, ExpiresAt: time.Now().Add(10 * time.Minute),
		Run: func(ctx context.Context) (evaluation.Result, error) {
			if err := h.agentTrajectories.BeginEvaluation(ctx, recordID); err != nil {
				return evaluation.Result{}, err
			}
			result, err := workflow.Evaluate(ctx, comparison)
			spent := costTracker.total()
			if result.SpentMicroUSD < 0 || result.SpentMicroUSD > spent || spent > estimatedCost {
				return evaluation.Result{SpentMicroUSD: spent}, errors.Join(err, errEvaluationCostInvariant)
			}
			return result, err
		},
		OnTerminal: func(result evaluation.TerminalResult) {
			callbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := h.agentTrajectories.ApplyTerminal(callbackCtx, recordID, result); err != nil {
				slog.Warn("routing.agent_trajectory.terminal_state_failed", "profile", h.cfg.Slug, "trajectory_id", recordID, "error", err)
			}
		},
	}
	submitResult := h.evaluation.Submit(job)
	stateCtx, cancelState = trajectoryStateContext(ctx)
	err = h.agentTrajectories.ApplySubmitResult(stateCtx, recordID, submitResult)
	cancelState()
	if err != nil {
		slog.Warn("routing.agent_trajectory.submit_state_failed", "profile", h.cfg.Slug, "trajectory_id", recordID, "error", err)
	}
	slog.Info(
		"routing.agent_trajectory.submitted", "profile", h.cfg.Slug,
		"trajectory_id", recordID, "candidate_model", pair.Candidate.Model(),
		"reference_model", pair.Reference.Model(), "result", submitResult,
	)
}

func appendObservedModel(path []string, model string) []string {
	if model == "" || len(path) > 0 && path[len(path)-1] == model {
		return path
	}
	return append(path, model)
}

func trajectoryPathCost(plan routing.ExecutionPlan, path []string, imageCount int) int64 {
	attempts := plan.ModelAttempts()
	total := int64(0)
	for _, model := range path {
		for _, attempt := range attempts {
			if attempt.Model() == model {
				total = addEvaluationCost(total, evaluationAttemptCost(attempt, imageCount))
				break
			}
		}
	}
	return total
}

func trajectoryLedgerCosts(
	entries []routing.CallLedgerEntry,
	finalModel string,
) (int64, int64, int64, bool) {
	finalSwitchIndex := -1
	for _, entry := range entries {
		if entry.Kind == routing.CallAnswer && entry.Model == finalModel {
			finalSwitchIndex = entry.ModelSwitchIndex
		}
	}
	if finalSwitchIndex < 0 {
		return 0, 0, 0, false
	}
	total := int64(0)
	finalCost := int64(0)
	finalLatency := int64(0)
	for _, entry := range entries {
		cost := entry.EstimatedMicroUSD
		if entry.ActualCostKnown {
			cost = entry.ActualMicroUSD
		}
		total = addEvaluationCost(total, cost)
		if entry.ModelSwitchIndex != finalSwitchIndex ||
			(entry.Kind != routing.CallAnswer && entry.Kind != routing.CallVision) {
			continue
		}
		finalCost = addEvaluationCost(finalCost, cost)
		finalLatency = addEvaluationCost(finalLatency, entry.ElapsedMS)
	}
	return total, finalCost, finalLatency, true
}

func trajectoryEvaluationModels(path []string, strongBaseline string) (string, string, bool) {
	if len(path) == 1 && path[0] != "" {
		return path[0], path[0], true
	}
	if len(path) == 2 && path[0] != "" && path[0] != path[1] && path[1] == strongBaseline {
		return path[0], path[1], true
	}
	return "", "", false
}

func (h *handler) skipAgentTrajectory(ctx context.Context, id int64, reason string) {
	stateCtx, cancel := trajectoryStateContext(ctx)
	defer cancel()
	if err := h.agentTrajectories.Skip(stateCtx, id, reason); err != nil {
		slog.Warn("routing.agent_trajectory.skip_failed", "profile", h.cfg.Slug, "trajectory_id", id, "reason", reason, "error", err)
	}
}

func trajectoryStateContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
}

func trajectoryQuestion(plaintext agenttrajectory.Plaintext) string {
	for index := len(plaintext.Events) - 1; index >= 0; index-- {
		event := plaintext.Events[index]
		if event.Kind == agenttrajectory.EventUserMessage && strings.TrimSpace(event.Text) != "" {
			return event.Text
		}
	}
	return "Evaluate completion of the observed task."
}

func estimateEvaluationTextTokens(value string) int {
	if value == "" {
		return 0
	}
	tokens := len(value) / 4
	if len(value)%4 != 0 {
		tokens++
	}
	return tokens
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
		reviewer.MaxOutputTokens < evaluation.ReviewRepairMaxOutputTokens {
		return
	}
	requestedOutput := request.Facts.RequestedOutputTokens
	if requestedOutput <= 0 {
		requestedOutput = 4096
	}
	reviewerInputTokens := request.Facts.EstimatedInputTokens + requestedOutput*2 + 512
	if reviewerInputTokens > reviewer.ContextWindow-evaluation.ReviewRepairMaxOutputTokens {
		return
	}
	reviewerCost := estimateEvaluationCallCost(reviewerInputTokens, evaluation.ReviewMaxOutputTokens, reviewer)
	reviewerRepairCost := estimateEvaluationCallCost(
		reviewerInputTokens, evaluation.ReviewRepairMaxOutputTokens, reviewer,
	)
	missingCost := evaluationWorstCaseAttemptCost(
		missing,
		request.Facts.ImageCount,
		h.cfg.OverloadRules,
	)
	estimatedCost := addEvaluationCost(missingCost, addEvaluationCost(reviewerCost, reviewerRepairCost))
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
		attemptTarget := targetURL(h.cfg.Upstream, requestURI)
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
	reviewerTarget := targetURL(h.cfg.Upstream, requestURI)
	comparison.Review = func(ctx context.Context, input evaluation.ReviewInput) (evaluation.ReviewVerdict, error) {
		return h.reviewEvaluationWithRepair(
			ctx, request.Operation, reviewerTarget, forwardedHeaders, config.ReviewerModel,
			input, reviewerCost, reviewerRepairCost, reviewer, costTracker,
		)
	}
	workflow := evaluation.NewWorkflow(nil)
	job := evaluation.Job{
		ProfileID: h.cfg.ID, SampleRateBPS: config.SampleRateBPS,
		Sampling: evaluation.SamplingKey{
			ProfileID: h.cfg.ID, Strategy: plan.Strategy(), Route: plan.Route(),
			TaskType: classification.TaskType, Difficulty: string(classification.Difficulty),
			Risk: string(classification.Risk), VisionMode: string(plan.VisionMode()),
			CandidateModel: pair.Candidate.Model(), ReferenceModel: pair.Reference.Model(),
		},
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

const (
	maxPrecommitStreamBytes                = 1 << 20
	maxCapturedResponseBytes               = 16 << 20
	maxClientRequestBodyBytes        int64 = 64 << 20
	maxBufferedUpstreamResponseBytes int64 = 64 << 20
)

func readBodyWithLimit(reader io.Reader, contentLength, limit int64) ([]byte, error) {
	if contentLength > limit {
		return nil, errBodyTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errBodyTooLarge
	}
	return body, nil
}

type boundedCapture struct {
	limit int
	body  bytes.Buffer
}

func (c *boundedCapture) Write(chunk []byte) {
	remaining := c.limit - c.body.Len()
	if remaining <= 0 {
		return
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	_, _ = c.body.Write(chunk)
}

func (c *boundedCapture) Bytes() []byte {
	return c.body.Bytes()
}

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
	promptLeakGuards ...*promptLeakDetector,
) relayResult {
	defer resp.Body.Close()
	var promptLeakGuard *promptLeakDetector
	if len(promptLeakGuards) > 0 {
		promptLeakGuard = promptLeakGuards[0]
	}
	if !streaming {
		body, err := readBodyWithLimit(
			resp.Body, resp.ContentLength, maxBufferedUpstreamResponseBytes,
		)
		if err != nil {
			return relayResult{err: provider.NewFailure(
				provider.FailureMalformedResponse, resp.StatusCode, err,
			)}
		}
		if detectSelfEscalation {
			decision, detectErr := detectSelfEscalationResponse(operation, body)
			if detectErr != nil || decision.Requested {
				return relayResult{
					captured: body, selfEscalation: decision, err: detectErr,
				}
			}
		}
		if promptLeakGuard.responseLeaks(body) {
			return relayResult{captured: body, err: errPromptDisclosure}
		}
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, err = w.Write(body)
		return relayResult{captured: body, committed: true, err: err}
	}

	flusher, canFlush := w.(http.Flusher)
	captured := boundedCapture{limit: maxCapturedResponseBytes}
	var pending bytes.Buffer
	streamGuard := newSelfEscalationStreamGuard(operation)
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
				if promptLeakGuard != nil {
					leaked, promptReady := promptLeakGuard.inspectStream(pending.Bytes())
					if leaked {
						return relayResult{captured: captured.Bytes(), err: errPromptDisclosure}
					}
					ready = ready && promptReady
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
						remaining, guardErr := streamGuard.Flush()
						if guardErr != nil {
							return relayResult{captured: captured.Bytes(), committed: true, err: guardErr}
						}
						if len(remaining) > 0 {
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
	attempt               routing.ModelAttemptPlan
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
	execution *autoExecution,
	visionCached bool,
) (preparedAutoAttempt, error) {
	for modelIndex < len(attempts) {
		attempt := attempts[modelIndex]
		attemptTarget := targetURL(h.cfg.Upstream, requestURI)
		nodeLease, reserveErr := execution.reserveNode(
			ctx, modelIndex, visionCached,
		)
		if reserveErr != nil {
			if modelIndex == 0 {
				return preparedAutoAttempt{}, reserveErr
			}
			if modelIndex+1 < len(attempts) {
				modelIndex++
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
				modelIndex: modelIndex, attempt: attempt,
				body: body, nodeLease: nodeLease,
				selfEscalationEnabled: injected,
			}, nil
		}
		nodeLease.release()
		if ctx.Err() != nil || !recoverableVisionFailure(err) {
			return preparedAutoAttempt{}, err
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
	parent context.Context,
	ctx context.Context,
	err error,
) {
	if writeRequestContextError(w, parent, ctx) {
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

func writeRequestContextError(
	w http.ResponseWriter,
	parent context.Context,
	requestCtx context.Context,
) bool {
	err := requestCtx.Err()
	if err == nil {
		return false
	}
	if parent.Err() != nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "routing request deadline exceeded", http.StatusGatewayTimeout)
		return true
	}
	http.Error(w, "routing request canceled", http.StatusBadGateway)
	return true
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

func writeModelOffline(w http.ResponseWriter, model string, status modeldirectory.Status) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "model_offline",
			"message": fmt.Sprintf("Model %q is %s for this Profile.", model, status),
		},
	})
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

// stream writes a successful response with SSE-friendly flushing and returns a
// bounded prefix for usage parsing and evaluation.
func stream(w http.ResponseWriter, resp *http.Response) []byte {
	return streamWithCaptureLimit(w, resp, maxCapturedResponseBytes)
}

func streamWithCaptureLimit(w http.ResponseWriter, resp *http.Response, captureLimit int) []byte {
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	capture := boundedCapture{limit: captureLimit}
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
