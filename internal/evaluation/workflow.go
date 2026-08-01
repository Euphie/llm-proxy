package evaluation

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
)

var ErrInvalidComparison = errors.New("invalid routing evaluation comparison")

type ModelOutput struct {
	Text                 string
	CostMicroUSD         int64
	LatencyMS            int64
	DeterministicFailure bool
	SevereError          bool
}

type ComparisonTask struct {
	ProfileID      int64
	Strategy       string
	Route          string
	TaskType       string
	CandidateModel string
	ReferenceModel string
	ReviewerModel  string
	Question       string
	Candidate      *ModelOutput
	Reference      *ModelOutput
	Generate       func(context.Context, string) (ModelOutput, error)
	Review         func(context.Context, ReviewInput) (ReviewVerdict, error)
}

type ReviewInput struct {
	Question string
	A        string
	B        string
}

type Winner string

const (
	WinnerA   Winner = "a"
	WinnerB   Winner = "b"
	WinnerTie Winner = "tie"
)

type ReviewVerdict struct {
	Winner               Winner
	SevereA              bool
	SevereB              bool
	ReviewerCostMicroUSD int64
}

type Workflow struct {
	swap func() bool
}

func NewWorkflow(swap func() bool) *Workflow {
	if swap == nil {
		swap = randomSwap
	}
	return &Workflow{swap: swap}
}

func (w *Workflow) Evaluate(ctx context.Context, task ComparisonTask) (Result, error) {
	if err := validateComparison(task); err != nil {
		return Result{}, err
	}
	candidate := cloneOutput(task.Candidate)
	reference := cloneOutput(task.Reference)
	spent := int64(0)
	if candidate == nil {
		generated, err := task.Generate(ctx, task.CandidateModel)
		spent = generated.CostMicroUSD
		if err != nil {
			return Result{SpentMicroUSD: max(spent, int64(0))}, err
		}
		candidate = &generated
	} else {
		generated, err := task.Generate(ctx, task.ReferenceModel)
		spent = generated.CostMicroUSD
		if err != nil {
			return Result{SpentMicroUSD: max(spent, int64(0))}, err
		}
		reference = &generated
	}
	normalizeOutput(candidate)
	normalizeOutput(reference)
	if candidate.CostMicroUSD < 0 || reference.CostMicroUSD < 0 ||
		candidate.LatencyMS < 0 || reference.LatencyMS < 0 {
		return Result{SpentMicroUSD: max(spent, int64(0))}, ErrInvalidComparison
	}

	if candidate.DeterministicFailure || reference.DeterministicFailure {
		outcome := OutcomeTie
		switch {
		case candidate.DeterministicFailure && !reference.DeterministicFailure:
			outcome = OutcomeReferenceWin
		case !candidate.DeterministicFailure && reference.DeterministicFailure:
			outcome = OutcomeCandidateWin
		}
		return Result{
			SpentMicroUSD: spent,
			Evidence: evidenceFor(
				task, *candidate, *reference, outcome,
				candidate.SevereError, candidate.DeterministicFailure, 0,
			),
		}, nil
	}

	swapped := w.swap()
	input := ReviewInput{Question: task.Question, A: candidate.Text, B: reference.Text}
	if swapped {
		input.A, input.B = input.B, input.A
	}
	verdict, err := task.Review(ctx, input)
	spent += max(verdict.ReviewerCostMicroUSD, int64(0))
	if err != nil {
		return Result{SpentMicroUSD: spent}, err
	}
	if verdict.ReviewerCostMicroUSD < 0 ||
		(verdict.Winner != WinnerA && verdict.Winner != WinnerB && verdict.Winner != WinnerTie) {
		return Result{SpentMicroUSD: spent}, ErrInvalidComparison
	}
	outcome := mapWinner(verdict.Winner, swapped)
	severeCandidate := verdict.SevereA
	if swapped {
		severeCandidate = verdict.SevereB
	}
	return Result{
		SpentMicroUSD: spent,
		Evidence: evidenceFor(
			task, *candidate, *reference, outcome, severeCandidate, false,
			verdict.ReviewerCostMicroUSD,
		),
	}, nil
}

func validateComparison(task ComparisonTask) error {
	if task.ProfileID <= 0 || strings.TrimSpace(task.Strategy) == "" ||
		strings.TrimSpace(task.Route) == "" || strings.TrimSpace(task.TaskType) == "" ||
		strings.TrimSpace(task.CandidateModel) == "" || strings.TrimSpace(task.ReferenceModel) == "" ||
		strings.TrimSpace(task.ReviewerModel) == "" || task.CandidateModel == task.ReferenceModel ||
		task.Generate == nil || task.Review == nil || (task.Candidate == nil) == (task.Reference == nil) {
		return ErrInvalidComparison
	}
	return nil
}

func normalizeOutput(output *ModelOutput) {
	if strings.TrimSpace(output.Text) == "" {
		output.DeterministicFailure = true
		output.SevereError = true
	}
}

func cloneOutput(output *ModelOutput) *ModelOutput {
	if output == nil {
		return nil
	}
	cloned := *output
	return &cloned
}

func mapWinner(winner Winner, swapped bool) Outcome {
	if winner == WinnerTie {
		return OutcomeTie
	}
	if (winner == WinnerA) != swapped {
		return OutcomeCandidateWin
	}
	return OutcomeReferenceWin
}

func evidenceFor(
	task ComparisonTask,
	candidate ModelOutput,
	reference ModelOutput,
	outcome Outcome,
	severeError bool,
	deterministicFailure bool,
	reviewerCost int64,
) *Evidence {
	return &Evidence{
		ProfileID: task.ProfileID, Strategy: task.Strategy, Route: task.Route,
		TaskType: task.TaskType, CandidateModel: task.CandidateModel,
		ReferenceModel: task.ReferenceModel, ReviewerModel: task.ReviewerModel,
		Outcome: outcome, SevereError: severeError,
		DeterministicFailure:  deterministicFailure,
		CandidateCostMicroUSD: candidate.CostMicroUSD,
		ReferenceCostMicroUSD: reference.CostMicroUSD,
		ReviewerCostMicroUSD:  reviewerCost,
		CandidateLatencyMS:    candidate.LatencyMS,
		ReferenceLatencyMS:    reference.LatencyMS,
	}
}

func randomSwap() bool {
	var value [1]byte
	if _, err := rand.Read(value[:]); err != nil {
		return false
	}
	return value[0]&1 == 1
}
