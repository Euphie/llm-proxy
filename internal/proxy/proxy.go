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
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
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
	cfg        profile.Runtime
	client     *http.Client
	stats      *stats.DB
	parser     stats.Parser
	vision     *vision.Preprocessor
	routing    *routing.Engine
	sessions   *routing.SessionStore
	evaluation evaluationSubmitter
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
	var currentTargets []routing.TargetPlan
	currentTargetIndex := 0
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
		currentTargets = currentAttempt.Targets()
		if len(currentTargets) == 0 {
			writeRoutingError(w, routing.ErrNoCapableModel)
			return
		}
		currentTargetID = currentTargets[0].ID()
		target = targetURL(currentTargets[0].Upstream(), requestURI)
		initialModel := currentAttempt.Model()
		initialTargetID := currentTargetID
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
				InitialTarget: initialTargetID, FinalTarget: currentTargetID,
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
			"target", currentTargetID,
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
	switchTarget := func() (bool, error) {
		if !isAuto || currentTargetIndex+1 >= len(currentTargets) {
			return false, nil
		}
		next := currentTargets[currentTargetIndex+1]
		if err := budget.CanReserveTargetSwitchCall(
			requestCtx,
			currentAttempt.AnswerCallCostMicroUSD(),
		); err != nil {
			return false, nil
		}
		if err := budget.ReserveTargetSwitchCall(
			requestCtx,
			currentAttempt.AnswerCallCostMicroUSD(),
		); err != nil {
			return false, err
		}
		previous := currentTargetID
		currentTargetIndex++
		answerAttempts++
		currentTargetID = next.ID()
		target = targetURL(next.Upstream(), requestURI)
		slog.Info("routing.target.switched",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"model", currentAttempt.Model(),
			"from_target", previous,
			"to_target", currentTargetID)
		return true, nil
	}
	switchModel := func() (bool, error) {
		if !isAuto || modelAttemptIndex+1 >= len(modelAttempts) {
			return false, nil
		}
		next := modelAttempts[modelAttemptIndex+1]
		nextTargets := next.Targets()
		if len(nextTargets) == 0 {
			return false, nil
		}
		nextTarget := nextTargets[0]
		nextTargetURL := targetURL(nextTarget.Upstream(), requestURI)
		if err := budget.CanReserveModelSwitchCall(
			requestCtx,
			next.AnswerCallCostMicroUSD(),
		); err != nil {
			return false, nil
		}
		nextBody, err := h.prepareAutoAttempt(
			requestCtx,
			r.Header,
			nextTargetURL,
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
		currentTargets = nextTargets
		currentTargetIndex = 0
		currentTargetID = nextTarget.ID()
		target = nextTargetURL
		body = nextBody
		slog.Info("routing.model.switched",
			"profile", label,
			"strategy", plan.Strategy(),
			"route", plan.Route(),
			"from_model", previous,
			"to_model", currentAttempt.Model(),
			"target", currentTargetID,
			"vision_mode", currentAttempt.VisionMode())
		return true, nil
	}
	switchAfterFailure := func() (bool, error) {
		if switched, err := switchTarget(); switched || err != nil {
			return switched, err
		}
		return switchModel()
	}

	var rule *provider.Rule
	retries := 0
	for {
		resp, err := h.do(requestCtx, r.Method, target, r.Header, body)
		if err != nil {
			if requestCtx.Err() != nil {
				return
			}
			failure := provider.ClassifyTransportFailure(err)
			failureClass, _ := provider.FailureClassOf(failure)
			if rule == nil {
				rule = provider.FirstRetryRule(h.cfg.OverloadRules)
			}
			if (failureClass == provider.FailureUnknownTransport ||
				failureClass == provider.FailureBudgetDeadline) &&
				rule != nil && retries < rule.MaxRetries &&
				h.reserveRetry(requestCtx, budget, currentTargetID, currentAttempt.AnswerCallCostMicroUSD()) {
				retries++
				answerAttempts++
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
			slog.Error("upstream request failed",
				"profile", label, "model", currentAttempt.Model(),
				"target", currentTargetID, "err", err)
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
					failure := provider.ClassifyTransportFailure(result.err)
					failureClass, _ := provider.FailureClassOf(failure)
					if rule == nil {
						rule = provider.FirstRetryRule(h.cfg.OverloadRules)
					}
					if (failureClass == provider.FailureUnknownTransport ||
						failureClass == provider.FailureBudgetDeadline) &&
						rule != nil && retries < rule.MaxRetries &&
						h.reserveRetry(requestCtx, budget, currentTargetID, currentAttempt.AnswerCallCostMicroUSD()) {
						retries++
						answerAttempts++
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
			if isAuto {
				h.submitEvaluation(
					r.Header, requestURI, routeRequest, classification, plan,
					currentAttempt, captured, time.Since(start),
				)
			}
			return
		}

		// Error response: buffer to check for overload
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

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
				h.reserveRetry(requestCtx, budget, currentTargetID, currentAttempt.AnswerCallCostMicroUSD()) {
				retries++
				answerAttempts++
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

const maxEvaluationContentBytes = 1 << 20

func (h *handler) submitEvaluation(
	headers http.Header,
	requestURI string,
	request routing.Request,
	classification routing.Classification,
	plan routing.ExecutionPlan,
	selected routing.ModelAttemptPlan,
	captured []byte,
	onlineLatency time.Duration,
) {
	config := h.cfg.AutoRouting.DynamicOptimization
	if h.evaluation == nil || !config.Enabled || len(captured) == 0 ||
		len(captured) > maxEvaluationContentBytes || request.Facts.HasTools {
		return
	}
	pair, ok := h.routing.EvaluationPair(request, classification, selected.Model())
	if !ok {
		return
	}
	online := evaluation.ParseModelOutput(
		request.Operation, captured, request.Facts,
		evaluationAttemptCost(selected, request.Facts.ImageCount),
		onlineLatency.Milliseconds(),
	)
	comparison := evaluation.ComparisonTask{
		ProfileID: h.cfg.ID, Strategy: plan.Strategy(), Route: plan.Route(),
		TaskType: classification.TaskType, CandidateModel: pair.Candidate.Model(),
		ReferenceModel: pair.Reference.Model(), ReviewerModel: config.ReviewerModel,
		Question: request.EvaluationText(),
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
	missingCost := evaluationAttemptCost(missing, request.Facts.ImageCount)
	estimatedCost := addEvaluationCost(missingCost, reviewerCost)
	if estimatedCost == 0 {
		estimatedCost = 1
	}

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
		plannedCost := evaluationAttemptCost(attempt, request.Facts.ImageCount)
		failedOutput := func() evaluation.ModelOutput {
			return evaluation.ModelOutput{
				CostMicroUSD: plannedCost,
				LatencyMS:    time.Since(started).Milliseconds(),
			}
		}
		body, err := h.prepareEvaluationAttempt(ctx, forwardedHeaders, attemptTarget, request, attempt)
		if err != nil {
			return failedOutput(), err
		}
		response, err := h.do(ctx, http.MethodPost, attemptTarget, forwardedHeaders, body)
		if err != nil {
			return failedOutput(), err
		}
		responseBody, err := readEvaluationResponse(response)
		output := evaluation.ParseModelOutput(
			request.Operation, responseBody, request.Facts,
			plannedCost,
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
		response, err := h.do(ctx, http.MethodPost, reviewerTarget, forwardedHeaders, body)
		if err != nil {
			return evaluation.ReviewVerdict{ReviewerCostMicroUSD: reviewerCost}, err
		}
		responseBody, err := readEvaluationResponse(response)
		if err != nil {
			return evaluation.ReviewVerdict{ReviewerCostMicroUSD: reviewerCost}, err
		}
		verdict, err := evaluation.ParseReviewVerdict(request.Operation, responseBody)
		verdict.ReviewerCostMicroUSD = reviewerCost
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
			return workflow.Evaluate(ctx, comparison)
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

func (h *handler) prepareEvaluationAttempt(
	ctx context.Context,
	headers http.Header,
	target string,
	request routing.Request,
	attempt routing.ModelAttemptPlan,
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
	return h.vision.ProcessTargetWithBudget(ctx, headers, body, target, nil)
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
