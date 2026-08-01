package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkflowGeneratesOnlyMissingReferenceAndBlindlyMapsReviewerWinner(t *testing.T) {
	workflow := NewWorkflow(func() bool { return true })
	generated := ""
	reviewed := false
	task := comparisonTask()
	task.Candidate = &ModelOutput{Text: "candidate answer", CostMicroUSD: 10, LatencyMS: 20}
	task.Generate = func(_ context.Context, model string) (ModelOutput, error) {
		generated = model
		return ModelOutput{Text: "reference answer", CostMicroUSD: 40, LatencyMS: 30}, nil
	}
	task.Review = func(_ context.Context, input ReviewInput) (ReviewVerdict, error) {
		reviewed = true
		if input.A != "reference answer" || input.B != "candidate answer" {
			t.Fatalf("blind input=%+v", input)
		}
		if strings.Contains(input.A+input.B, "fast") || strings.Contains(input.A+input.B, "strong") {
			t.Fatalf("review input leaked model identity: %+v", input)
		}
		return ReviewVerdict{Winner: WinnerB, ReviewerCostMicroUSD: 5}, nil
	}

	result, err := workflow.Evaluate(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if generated != "strong" || !reviewed {
		t.Fatalf("generated=%q reviewed=%v", generated, reviewed)
	}
	if result.SpentMicroUSD != 45 || result.Evidence == nil ||
		result.Evidence.Outcome != OutcomeCandidateWin ||
		result.Evidence.CandidateCostMicroUSD != 10 ||
		result.Evidence.ReferenceCostMicroUSD != 40 ||
		result.Evidence.ReviewerCostMicroUSD != 5 {
		t.Fatalf("result=%+v evidence=%+v", result, result.Evidence)
	}
}

func TestWorkflowGeneratesOnlyMissingCandidateWhenOnlineUsedBaseline(t *testing.T) {
	workflow := NewWorkflow(func() bool { return false })
	task := comparisonTask()
	task.Reference = &ModelOutput{Text: "reference answer", CostMicroUSD: 40, LatencyMS: 30}
	task.Generate = func(_ context.Context, model string) (ModelOutput, error) {
		if model != "fast" {
			t.Fatalf("generated model=%q", model)
		}
		return ModelOutput{Text: "candidate answer", CostMicroUSD: 10, LatencyMS: 20}, nil
	}
	task.Review = func(_ context.Context, input ReviewInput) (ReviewVerdict, error) {
		if input.A != "candidate answer" || input.B != "reference answer" {
			t.Fatalf("blind input=%+v", input)
		}
		return ReviewVerdict{Winner: WinnerTie, ReviewerCostMicroUSD: 5}, nil
	}

	result, err := workflow.Evaluate(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.SpentMicroUSD != 15 || result.Evidence.Outcome != OutcomeTie {
		t.Fatalf("result=%+v", result)
	}
}

func TestWorkflowUsesDeterministicFailureBeforeSubjectiveReview(t *testing.T) {
	workflow := NewWorkflow(func() bool { return false })
	task := comparisonTask()
	task.Candidate = &ModelOutput{
		Text: "malformed", CostMicroUSD: 10, LatencyMS: 20,
		DeterministicFailure: true, SevereError: true,
	}
	task.Generate = func(_ context.Context, model string) (ModelOutput, error) {
		return ModelOutput{Text: "valid reference", CostMicroUSD: 40, LatencyMS: 30}, nil
	}
	task.Review = func(context.Context, ReviewInput) (ReviewVerdict, error) {
		t.Fatal("subjective reviewer ran after deterministic failure")
		return ReviewVerdict{}, nil
	}

	result, err := workflow.Evaluate(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.SpentMicroUSD != 40 || result.Evidence.Outcome != OutcomeReferenceWin ||
		!result.Evidence.SevereError || !result.Evidence.DeterministicFailure ||
		result.Evidence.ReviewerCostMicroUSD != 0 {
		t.Fatalf("result=%+v evidence=%+v", result, result.Evidence)
	}
}

func TestWorkflowRejectsIncompleteOrAmbiguousComparisonTasks(t *testing.T) {
	workflow := NewWorkflow(nil)
	task := comparisonTask()
	if _, err := workflow.Evaluate(context.Background(), task); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("missing online output error=%v", err)
	}
	task.Candidate = &ModelOutput{Text: "candidate"}
	task.Reference = &ModelOutput{Text: "reference"}
	if _, err := workflow.Evaluate(context.Background(), task); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("two online outputs error=%v", err)
	}
}

func comparisonTask() ComparisonTask {
	return ComparisonTask{
		ProfileID: 7, Strategy: "20260802-001", Route: "balanced", TaskType: "simple",
		CandidateModel: "fast", ReferenceModel: "strong", ReviewerModel: "judge",
		Question: "answer the request",
		Generate: func(context.Context, string) (ModelOutput, error) {
			return ModelOutput{}, errors.New("unexpected generation")
		},
		Review: func(context.Context, ReviewInput) (ReviewVerdict, error) {
			return ReviewVerdict{}, errors.New("unexpected review")
		},
	}
}
