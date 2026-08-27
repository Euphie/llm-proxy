package evaluation

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
)

var ErrInvalidComparison = errors.New("invalid routing evaluation comparison")

type Dimension string

const (
	DimensionCorrectness          Dimension = "correctness"
	DimensionCompleteness         Dimension = "completeness"
	DimensionInstructionFollowing Dimension = "instruction_following"
	DimensionFormatToolSafety     Dimension = "format_tool_safety"
	DimensionTaskCompletion       Dimension = "task_completion"
)

var ReviewDimensions = []Dimension{
	DimensionCorrectness,
	DimensionCompleteness,
	DimensionInstructionFollowing,
	DimensionFormatToolSafety,
	DimensionTaskCompletion,
}

var DimensionWeightsBPS = map[Dimension]int{
	DimensionCorrectness:          4000,
	DimensionCompleteness:         2000,
	DimensionInstructionFollowing: 2000,
	DimensionFormatToolSafety:     1000,
	DimensionTaskCompletion:       1000,
}

type ModelOutput struct {
	Text                 string
	CostMicroUSD         int64
	LatencyMS            int64
	ToolCallAttempted    bool
	DeterministicFailure bool
	SevereError          bool
}

type EscalationObservation string

const (
	EscalationNotObserved  EscalationObservation = ""
	EscalationRequested    EscalationObservation = "requested"
	EscalationNotRequested EscalationObservation = "not_requested"
)

type ComparisonTask struct {
	ProfileID             int64
	Strategy              string
	Route                 string
	TaskType              string
	Difficulty            string
	Risk                  string
	VisionMode            string
	CandidateModel        string
	ReferenceModel        string
	ReviewerModel         string
	EscalationObservation EscalationObservation
	Context               string
	Question              string
	Candidate             *ModelOutput
	Reference             *ModelOutput
	Generate              func(context.Context, string) (ModelOutput, error)
	Review                func(context.Context, ReviewInput) (ReviewVerdict, error)
}

type ReviewInput struct {
	Context  string `json:"context,omitempty"`
	Question string `json:"question"`
	A        string `json:"a"`
	B        string `json:"b"`
}

type Winner string

const (
	WinnerA   Winner = "a"
	WinnerB   Winner = "b"
	WinnerTie Winner = "tie"
)

type ReviewVerdict struct {
	Dimensions           map[Dimension]Winner
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
		dimensions := make(map[Dimension]Outcome, len(ReviewDimensions))
		for _, dimension := range ReviewDimensions {
			dimensions[dimension] = outcome
		}
		return Result{
			SpentMicroUSD: spent,
			Evidence: evidenceFor(
				task, *candidate, *reference, outcome, dimensions,
				candidate.SevereError, candidate.DeterministicFailure, 0,
			),
		}, nil
	}

	swapped := w.swap()
	input := ReviewInput{Context: task.Context, Question: task.Question, A: candidate.Text, B: reference.Text}
	if swapped {
		input.A, input.B = input.B, input.A
	}
	verdict, err := task.Review(ctx, input)
	spent += max(verdict.ReviewerCostMicroUSD, int64(0))
	if err != nil {
		return Result{SpentMicroUSD: spent}, err
	}
	if verdict.ReviewerCostMicroUSD < 0 || !validDimensionWinners(verdict.Dimensions) {
		return Result{SpentMicroUSD: spent}, ErrInvalidComparison
	}
	dimensions := mapDimensionWinners(verdict.Dimensions, swapped)
	outcome := weightedOutcome(dimensions)
	severeCandidate := verdict.SevereA
	if swapped {
		severeCandidate = verdict.SevereB
	}
	return Result{
		SpentMicroUSD: spent,
		Evidence: evidenceFor(
			task, *candidate, *reference, outcome, dimensions, severeCandidate, false,
			verdict.ReviewerCostMicroUSD,
		),
	}, nil
}

func validateComparison(task ComparisonTask) error {
	if task.ProfileID <= 0 || strings.TrimSpace(task.Strategy) == "" ||
		strings.TrimSpace(task.Route) == "" || strings.TrimSpace(task.TaskType) == "" ||
		strings.TrimSpace(task.Difficulty) == "" || strings.TrimSpace(task.Risk) == "" ||
		strings.TrimSpace(task.VisionMode) == "" ||
		strings.TrimSpace(task.CandidateModel) == "" || strings.TrimSpace(task.ReferenceModel) == "" ||
		strings.TrimSpace(task.ReviewerModel) == "" || task.CandidateModel == task.ReferenceModel ||
		task.Generate == nil || task.Review == nil || (task.Candidate == nil) == (task.Reference == nil) ||
		(task.EscalationObservation != EscalationNotObserved &&
			task.EscalationObservation != EscalationRequested &&
			task.EscalationObservation != EscalationNotRequested) {
		return ErrInvalidComparison
	}
	return nil
}

func validDimensionWinners(dimensions map[Dimension]Winner) bool {
	if len(dimensions) != len(ReviewDimensions) {
		return false
	}
	for _, dimension := range ReviewDimensions {
		winner, ok := dimensions[dimension]
		if !ok || (winner != WinnerA && winner != WinnerB && winner != WinnerTie) {
			return false
		}
	}
	return true
}

func mapDimensionWinners(dimensions map[Dimension]Winner, swapped bool) map[Dimension]Outcome {
	result := make(map[Dimension]Outcome, len(ReviewDimensions))
	for _, dimension := range ReviewDimensions {
		result[dimension] = mapWinner(dimensions[dimension], swapped)
	}
	return result
}

func weightedOutcome(dimensions map[Dimension]Outcome) Outcome {
	candidateScore := 0
	referenceScore := 0
	for dimension, outcome := range dimensions {
		weight := DimensionWeightsBPS[dimension]
		switch outcome {
		case OutcomeCandidateWin:
			candidateScore += weight
		case OutcomeReferenceWin:
			referenceScore += weight
		case OutcomeTie:
			candidateScore += weight / 2
			referenceScore += weight / 2
		}
	}
	switch {
	case candidateScore > referenceScore:
		return OutcomeCandidateWin
	case referenceScore > candidateScore:
		return OutcomeReferenceWin
	default:
		return OutcomeTie
	}
}

func normalizeOutput(output *ModelOutput) {
	if strings.TrimSpace(output.Text) == "" || output.ToolCallAttempted {
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
	dimensions map[Dimension]Outcome,
	severeError bool,
	deterministicFailure bool,
	reviewerCost int64,
) *Evidence {
	eligible := task.EscalationObservation != EscalationNotObserved
	requested := task.EscalationObservation == EscalationRequested
	candidateInsufficient := outcome == OutcomeReferenceWin || severeError || deterministicFailure
	return &Evidence{
		ProfileID: task.ProfileID, Strategy: task.Strategy, Route: task.Route,
		TaskType: task.TaskType, Difficulty: task.Difficulty, Risk: task.Risk,
		VisionMode: task.VisionMode, CandidateModel: task.CandidateModel,
		ReferenceModel: task.ReferenceModel, ReviewerModel: task.ReviewerModel,
		Outcome: outcome, Dimensions: dimensions, SevereError: severeError,
		SelfEscalationEligible:    eligible,
		SelfEscalationRequested:   requested,
		SelfEscalationSupported:   requested && candidateInsufficient,
		SelfEscalationUnnecessary: requested && !candidateInsufficient,
		SelfEscalationMissed:      task.EscalationObservation == EscalationNotRequested && candidateInsufficient,
		DeterministicFailure:      deterministicFailure,
		CandidateCostMicroUSD:     candidate.CostMicroUSD,
		ReferenceCostMicroUSD:     reference.CostMicroUSD,
		ReviewerCostMicroUSD:      reviewerCost,
		CandidateLatencyMS:        candidate.LatencyMS,
		ReferenceLatencyMS:        reference.LatencyMS,
	}
}

func randomSwap() bool {
	var value [1]byte
	if _, err := rand.Read(value[:]); err != nil {
		return false
	}
	return value[0]&1 == 1
}
