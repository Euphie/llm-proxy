package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/routing"
	"github.com/Euphie/llm-proxy/internal/stats"
	"github.com/Euphie/llm-proxy/internal/vision"
)

type autoExecution struct {
	budget       *routing.AttemptBudget
	ledger       *routing.CallLedger
	nodes        map[[2]int]routing.PhysicalAttemptSnapshot
	models       profile.ModelCatalog
	answerParser stats.Parser
	visionParser stats.Parser
}

type autoNodeLease struct {
	lease             *routing.AttemptLease
	ledger            *routing.CallLedger
	modelSwitchIndex  int
	targetSwitchIndex int
	answerCalls       int
	models            profile.ModelCatalog
	answerParser      stats.Parser
	visionParser      stats.Parser
}

func newAutoExecution(
	plan routing.ExecutionPlan,
	budget *routing.AttemptBudget,
	ledger *routing.CallLedger,
	models profile.ModelCatalog,
	answerParser stats.Parser,
	visionParser stats.Parser,
) *autoExecution {
	nodes := make(map[[2]int]routing.PhysicalAttemptSnapshot)
	for _, node := range plan.CallGraph().Attempts {
		nodes[[2]int{node.ModelIndex, node.TargetIndex}] = node
	}
	return &autoExecution{
		budget: budget, ledger: ledger, nodes: nodes, models: models,
		answerParser: answerParser, visionParser: visionParser,
	}
}

func (e *autoExecution) reserveNode(
	ctx context.Context,
	modelIndex int,
	targetIndex int,
	visionCached bool,
) (*autoNodeLease, error) {
	node, ok := e.nodes[[2]int{modelIndex, targetIndex}]
	if !ok {
		return nil, fmt.Errorf("%w: call graph node", routing.ErrAttemptBudgetExceeded)
	}
	reservation := node.Reservation()
	if visionCached {
		reservation.VisionCallsPerImage = nil
		reservation.VisionCalls = 0
	}
	lease, err := e.budget.ReserveAttempt(ctx, reservation)
	if err != nil {
		return nil, err
	}
	snapshot := e.budget.Snapshot()
	return &autoNodeLease{
		lease: lease, ledger: e.ledger,
		modelSwitchIndex:  snapshot.ModelSwitches,
		targetSwitchIndex: snapshot.TargetSwitches,
		answerCalls:       node.AnswerCalls,
		models:            e.models,
		answerParser:      e.answerParser,
		visionParser:      e.visionParser,
	}, nil
}

func (n *autoNodeLease) allowsAnswerRetry(retryIndex int) bool {
	return n != nil && retryIndex > 0 && retryIndex < n.answerCalls
}

func (n *autoNodeLease) visionReservation(visionModel string) vision.CallReservation {
	return func(
		ctx context.Context,
		imageIndex int,
		retryIndex int,
	) (vision.CallCompletion, error) {
		ticket, err := n.lease.ConsumeVision(ctx, imageIndex, retryIndex)
		if err != nil {
			return nil, err
		}
		ticket.Model = visionModel
		sequence := n.ledger.Begin(*ticket, n.modelSwitchIndex, n.targetSwitchIndex)
		var once sync.Once
		return func(statusCode int, outcome string, responseBody []byte) {
			once.Do(func() {
				completeLedgerUsage(
					n.ledger, sequence, statusCode, outcome, responseBody,
					n.visionParser, n.models[visionModel],
				)
			})
		}, nil
	}
}

func (n *autoNodeLease) releaseUnusedVision() {
	if n != nil && n.lease != nil {
		n.lease.ReleaseUnusedVision()
	}
}

func (n *autoNodeLease) beginAnswer(
	ctx context.Context,
	retryIndex int,
) (routing.CallTicket, func(int, string, []byte), error) {
	var ticket *routing.CallTicket
	var err error
	if retryIndex == 0 {
		ticket, err = n.lease.ConsumeAnswer(ctx)
	} else {
		ticket, err = n.lease.ConsumeAnswerRetry(ctx, retryIndex)
	}
	if err != nil {
		return routing.CallTicket{}, nil, err
	}
	sequence := n.ledger.Begin(*ticket, n.modelSwitchIndex, n.targetSwitchIndex)
	var once sync.Once
	return *ticket, func(statusCode int, outcome string, responseBody []byte) {
		once.Do(func() {
			completeLedgerUsage(
				n.ledger, sequence, statusCode, outcome, responseBody,
				n.answerParser, n.models[ticket.Model],
			)
		})
	}, nil
}

func completeLedgerUsage(
	ledger *routing.CallLedger,
	sequence int,
	statusCode int,
	outcome string,
	responseBody []byte,
	parser stats.Parser,
	model profile.ModelCapability,
) {
	if parser == nil {
		ledger.Complete(sequence, statusCode, outcome)
		return
	}
	usage, ok := parser.Parse(responseBody)
	ledger.CompleteWithUsage(sequence, statusCode, outcome, routing.CallUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		InputPresent: usage.InputPresent, OutputPresent: usage.OutputPresent,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		Present:             ok,
	}, model)
}

func physicalCallsForStats(entries []routing.CallLedgerEntry) []stats.PhysicalCall {
	result := make([]stats.PhysicalCall, len(entries))
	for index, entry := range entries {
		result[index] = stats.PhysicalCall{
			Sequence: entry.Sequence, Kind: string(entry.Kind), Model: entry.Model,
			Target: entry.Target, ImageIndex: entry.ImageIndex,
			RetryIndex: entry.RetryIndex, ModelSwitchIndex: entry.ModelSwitchIndex,
			TargetSwitchIndex: entry.TargetSwitchIndex,
			EstimatedMicroUSD: entry.EstimatedMicroUSD,
			ActualCostKnown:   entry.ActualCostKnown, ActualMicroUSD: entry.ActualMicroUSD,
			StatusCode: entry.StatusCode, Outcome: entry.Outcome,
		}
	}
	return result
}

func (n *autoNodeLease) release() {
	if n != nil && n.lease != nil {
		n.lease.ReleaseUnused()
	}
}

var fallbackCorrelationSequence atomic.Uint64

func newCallCorrelationID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("local-%d", fallbackCorrelationSequence.Add(1))
}
