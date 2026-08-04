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
	baseline profile.ModelCapability
}

func NewEngine(runtime profile.Runtime, client *http.Client) (*Engine, error) {
	planner, err := NewPlanner(runtime)
	if err != nil {
		return nil, err
	}
	return &Engine{
		planner: planner, analyzer: newAnalyzer(runtime, client),
		auto:     runtime.AutoRouting,
		baseline: runtime.Models[runtime.AutoRouting.StrongBaselineModel],
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
	fallbackReason := ""
	if classification, matched := ClassifyHardRisk(request, e.baseline, e.auto.RiskPolicy); matched {
		if session, ok := e.sessionClassification(preference); ok {
			classification.TaskType = session.TaskType
			classification.Difficulty = session.Difficulty
			classification.ReasonCodes = append(classification.ReasonCodes, "session_context_preserved")
		}
		plan, err := e.plan(request, classification, preference, budget)
		return plan, classification, err
	}
	if classification, ok := e.sessionClassification(preference); ok {
		plan, err := e.plan(request, classification, preference, budget)
		return plan, classification, err
	}
	if classification, matched := classifySimple(request); matched {
		plan, err := e.plan(request, classification, cachePreference, budget)
		return plan, classification, err
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
		default:
			return ExecutionPlan{}, Classification{}, err
		}
	}
	if err != nil || classification.ConfidenceBPS < e.auto.AnalyzerMinConfidenceBPS {
		confidence := 0
		if err == nil {
			fallbackReason = "task_analyzer_low_confidence"
			confidence = classification.ConfidenceBPS
		}
		fallback := Classification{
			TaskType: "unknown", Difficulty: DifficultyUnknown, Risk: RiskUnknown,
			ConfidenceBPS: confidence, Source: ClassificationSourceFallback,
			ReasonCodes: []string{fallbackReason},
		}
		plan, planErr := e.plan(request, fallback, cachePreference, budget)
		return plan, fallback, planErr
	}
	plan, err := e.plan(request, classification, cachePreference, budget)
	return plan, classification, err
}

func (e *Engine) sessionClassification(preference SessionPreference) (Classification, bool) {
	if preference.TaskType == "" || preference.RouteID == "" || preference.Model == "" ||
		preference.Strategy != e.auto.Strategy.Name || preference.MinQualityScoreBPS < 0 ||
		preference.MinQualityScoreBPS > 10_000 {
		return Classification{}, false
	}
	classification := Classification{
		TaskType: preference.TaskType, Difficulty: preference.Difficulty, Risk: RiskNormal,
		ConfidenceBPS: 10_000, Source: ClassificationSourceSession,
		ReasonCodes: []string{"session_reuse"},
	}
	if classification.Difficulty == "" {
		classification.Difficulty = DifficultyUnknown
	}
	if e.planner.RouteID(classification) != preference.RouteID {
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
