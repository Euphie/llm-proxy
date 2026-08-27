package routing

import (
	"context"
	"errors"
	"net/http"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
)

type Engine struct {
	planner  *Planner
	analyzer *Analyzer
	auto     profile.AutoRoutingRuntime
}

func NewEngine(runtime profile.Runtime, client *http.Client) (*Engine, error) {
	planner, err := NewPlanner(runtime)
	if err != nil {
		return nil, err
	}
	return &Engine{
		planner: planner, analyzer: newAnalyzer(runtime, client),
		auto: runtime.AutoRouting,
	}, nil
}

func (e *Engine) NewAttemptBudget(
	parent context.Context,
) (*AttemptBudget, context.Context, context.CancelFunc) {
	return NewAttemptBudget(parent, e.auto.Strategy.Budget)
}

func (e *Engine) EvaluationPair(
	request Request,
	classification Classification,
	selectedModel string,
) (EvaluationPair, bool) {
	return e.planner.EvaluationPair(request, classification, selectedModel)
}

func (e *Engine) UsesDefaultRoute(taskType string) bool {
	return e.UsesDefaultRouteFor(taskType, DifficultyUnknown)
}

func (e *Engine) UsesDefaultRouteFor(taskType string, difficulty Difficulty) bool {
	_, mapped := e.auto.Strategy.RouteFor(taskType, string(difficulty))
	return !mapped
}

func (e *Engine) Route(
	ctx context.Context,
	headers http.Header,
	request Request,
	budget *AttemptBudget,
) (ExecutionPlan, Classification, error) {
	return e.RouteWithPreference(ctx, headers, request, budget, SessionPreference{})
}

func (e *Engine) RouteWithPreference(
	ctx context.Context,
	headers http.Header,
	request Request,
	budget *AttemptBudget,
	preference SessionPreference,
) (ExecutionPlan, Classification, error) {
	cachePreference := SessionPreference{CacheMetrics: preference.CacheMetrics}
	planningPreference := cachePreference
	if preference.ModelLocked {
		if _, ok := e.sessionPreferenceClassification(preference); ok {
			planningPreference = preference
		}
	}
	fallbackReason := ""
	if preference.TaskContinuation {
		if classification, ok := e.sessionClassification(preference); ok {
			plan, err := e.plan(request, classification, preference, budget)
			return plan, classification, err
		}
	}

	classification, err := e.analyzer.Analyze(ctx, headers, request, budget)
	if err != nil {
		if ctx.Err() != nil {
			return ExecutionPlan{}, Classification{}, ctx.Err()
		}
		if errors.Is(err, ErrAttemptBudgetExceeded) {
			return ExecutionPlan{}, Classification{}, err
		}
		class, ok := provider.FailureClassOf(err)
		if !ok {
			return ExecutionPlan{}, Classification{}, err
		}
		switch class {
		case provider.FailureOverloadTransient,
			provider.FailureOperationTimeout,
			provider.FailureUnknownTransport,
			provider.FailureMalformedResponse:
			fallbackReason = "task_analyzer_" + string(class)
			if code := analyzerResultErrorCode(err); class == provider.FailureMalformedResponse && code != "" {
				fallbackReason += "_" + code
			}
		case provider.FailureRequestProtocolCapability:
			status := provider.FailureStatus(err)
			if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
				return ExecutionPlan{}, Classification{}, err
			}
			fallbackReason = "task_analyzer_" + string(class)
		default:
			return ExecutionPlan{}, Classification{}, err
		}
	}
	if err != nil {
		fallback := Classification{
			TaskType: "unknown", Difficulty: DifficultyUnknown, Risk: RiskUnknown,
			Source:      ClassificationSourceFallback,
			ReasonCodes: []string{fallbackReason},
		}
		if retained, ok := e.sessionPreferenceClassification(preference); ok {
			plan, retainErr := e.plan(request, retained, preference, budget)
			if retainErr == nil && plan.Model() == preference.Model {
				fallback.ReasonCodes = append(
					fallback.ReasonCodes,
					ClassificationReasonSessionModelRetained,
				)
				plan.reason = "analyzer failure; retained Session model"
				plan.candidateDecisions = markSelectedCandidate(
					plan.candidateDecisions,
					plan.Model(),
					plan.reason,
				)
				return plan, fallback, nil
			}
			if errors.Is(retainErr, ErrAttemptBudgetExceeded) {
				return plan, fallback, retainErr
			}
		}
		plan, planErr := e.plan(request, fallback, cachePreference, budget)
		return plan, fallback, planErr
	}
	classification = applyClassificationConfidencePolicy(
		classification,
		e.auto.AnalyzerMinConfidenceBPS,
	)
	plan, err := e.plan(request, classification, planningPreference, budget)
	return plan, classification, err
}

func (e *Engine) sessionClassification(preference SessionPreference) (Classification, bool) {
	classification, ok := e.sessionPreferenceClassification(preference)
	if !ok {
		return Classification{}, false
	}
	reason := "session_reuse"
	switch {
	case preference.Model == e.auto.StrongBaselineModel && preference.ModelLocked:
		reason = "session_highest_model_locked"
	case preference.TaskContinuation:
		reason = "session_task_continuation"
	case preference.ModelLocked:
		reason = "session_model_locked"
	default:
		return Classification{}, false
	}
	classification.ReasonCodes = []string{reason}
	return classification, true
}

func (e *Engine) sessionPreferenceClassification(preference SessionPreference) (Classification, bool) {
	if preference.TaskType == "" || preference.RouteID == "" || preference.Model == "" ||
		preference.TaskType == "unknown" || preference.Difficulty == DifficultyUnknown ||
		preference.Strategy != e.auto.Strategy.Name || preference.MinQualityScoreBPS < 0 ||
		preference.MinQualityScoreBPS > 10_000 {
		return Classification{}, false
	}
	classification := Classification{
		TaskType: preference.TaskType, Difficulty: preference.Difficulty, Risk: RiskNormal,
		ConfidenceBPS: 10_000, TaskTypeConfidenceBPS: 10_000,
		DifficultyConfidenceBPS: 10_000, RiskConfidenceBPS: 10_000,
		Source: ClassificationSourceSession,
	}
	if classification.Difficulty == "" {
		classification.Difficulty = DifficultyUnknown
	}
	if e.planner.RouteID(classification) != preference.RouteID && !preference.ModelLocked {
		return Classification{}, false
	}
	return classification, true
}

func (e *Engine) plan(
	request Request,
	classification Classification,
	preference SessionPreference,
	budget *AttemptBudget,
) (ExecutionPlan, error) {
	if budget == nil {
		return e.planner.PlanWithPreference(request, classification, preference)
	}
	return e.planner.PlanWithBudget(request, classification, preference, budget.Snapshot())
}
