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

func (e *Engine) Route(
	ctx context.Context,
	headers http.Header,
	request Request,
	budget *AttemptBudget,
) (ExecutionPlan, Classification, error) {
	return e.RouteWithPreference(ctx, headers, request, budget, nil)
}

func (e *Engine) RouteWithPreference(
	ctx context.Context,
	headers http.Header,
	request Request,
	budget *AttemptBudget,
	resolvePreference func(routeID string) SessionPreference,
) (ExecutionPlan, Classification, error) {
	if classification, matched := ClassifyLocal(request, e.baseline, e.auto.RiskPolicy); matched {
		plan, err := e.plan(request, classification, resolvePreference, budget)
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
		default:
			return ExecutionPlan{}, Classification{}, err
		}
	}
	if err != nil || classification.ConfidenceBPS < e.auto.AnalyzerMinConfidenceBPS {
		fallback := Classification{
			TaskType: "high_risk", Risk: RiskHigh,
			Source: ClassificationSourceFallback,
		}
		plan, planErr := e.plan(request, fallback, resolvePreference, budget)
		return plan, fallback, planErr
	}
	plan, err := e.plan(request, classification, resolvePreference, budget)
	return plan, classification, err
}

func (e *Engine) plan(
	request Request,
	classification Classification,
	resolvePreference func(routeID string) SessionPreference,
	budget *AttemptBudget,
) (ExecutionPlan, error) {
	preference := SessionPreference{}
	if resolvePreference != nil {
		preference = resolvePreference(e.planner.RouteID(classification))
	}
	if budget == nil {
		return e.planner.PlanWithPreference(request, classification, preference)
	}
	return e.planner.PlanWithBudget(request, classification, preference, budget.Snapshot())
}
