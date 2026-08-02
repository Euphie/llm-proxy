package routing

import (
	"context"
	"math"
	"sync"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type CallLedgerEntry struct {
	CorrelationID     string
	Sequence          int
	Kind              CallKind
	Model             string
	Target            string
	ImageIndex        int
	RetryIndex        int
	ModelSwitchIndex  int
	TargetSwitchIndex int
	EstimatedMicroUSD int64
	ActualCostKnown   bool
	ActualMicroUSD    int64
	StatusCode        int
	Outcome           string
}

type CallLedger struct {
	mu            sync.Mutex
	correlationID string
	entries       []CallLedgerEntry
}

type CallUsage struct {
	InputTokens         int
	OutputTokens        int
	InputPresent        bool
	OutputPresent       bool
	CacheReadTokens     int
	CacheCreationTokens int
	Present             bool
}

type CallLedgerAggregate struct {
	CorrelationID             string
	PhysicalCalls             int
	EstimatedConsumedMicroUSD int64
	KnownActualMicroUSD       int64
	AllActualCostsKnown       bool
}

type callLedgerContextKey struct{}

func WithCallLedger(ctx context.Context, ledger *CallLedger) context.Context {
	return context.WithValue(ctx, callLedgerContextKey{}, ledger)
}

func CallLedgerFromContext(ctx context.Context) *CallLedger {
	ledger, _ := ctx.Value(callLedgerContextKey{}).(*CallLedger)
	return ledger
}

func NewCallLedger(correlationID string) *CallLedger {
	return &CallLedger{correlationID: correlationID}
}

func (l *CallLedger) Begin(
	ticket CallTicket,
	modelSwitchIndex int,
	targetSwitchIndex int,
) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sequence := len(l.entries) + 1
	l.entries = append(l.entries, CallLedgerEntry{
		CorrelationID: l.correlationID, Sequence: sequence,
		Kind: ticket.Kind, Model: ticket.Model, Target: ticket.Target,
		ImageIndex: ticket.ImageIndex, RetryIndex: ticket.RetryIndex,
		ModelSwitchIndex: modelSwitchIndex, TargetSwitchIndex: targetSwitchIndex,
		EstimatedMicroUSD: ticket.EstimatedMicroUSD,
	})
	return sequence
}

func (l *CallLedger) Complete(sequence int, statusCode int, outcome string) {
	l.CompleteWithActual(sequence, statusCode, outcome, false, 0)
}

func (l *CallLedger) CompleteWithUsage(
	sequence int,
	statusCode int,
	outcome string,
	usage CallUsage,
	model profile.ModelCapability,
) {
	actual, known := actualCallCost(usage, model)
	l.CompleteWithActual(sequence, statusCode, outcome, known, actual)
}

func (l *CallLedger) CompleteWithActual(
	sequence int,
	statusCode int,
	outcome string,
	actualKnown bool,
	actualMicroUSD int64,
) {
	if l == nil || sequence <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if sequence > len(l.entries) {
		return
	}
	entry := &l.entries[sequence-1]
	entry.StatusCode = statusCode
	entry.Outcome = outcome
	entry.ActualCostKnown = actualKnown
	if actualKnown {
		entry.ActualMicroUSD = actualMicroUSD
	}
}

func (l *CallLedger) Snapshot() []CallLedgerEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CallLedgerEntry(nil), l.entries...)
}

func (l *CallLedger) Aggregate() CallLedgerAggregate {
	if l == nil {
		return CallLedgerAggregate{AllActualCostsKnown: true}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	aggregate := CallLedgerAggregate{
		CorrelationID:       l.correlationID,
		PhysicalCalls:       len(l.entries),
		AllActualCostsKnown: true,
	}
	for _, entry := range l.entries {
		aggregate.EstimatedConsumedMicroUSD = addCost(
			aggregate.EstimatedConsumedMicroUSD,
			entry.EstimatedMicroUSD,
		)
		if !entry.ActualCostKnown {
			aggregate.AllActualCostsKnown = false
			continue
		}
		if entry.ActualMicroUSD < 0 ||
			aggregate.KnownActualMicroUSD > math.MaxInt64-entry.ActualMicroUSD {
			aggregate.KnownActualMicroUSD = math.MaxInt64
			aggregate.AllActualCostsKnown = false
			continue
		}
		aggregate.KnownActualMicroUSD += entry.ActualMicroUSD
	}
	return aggregate
}

func actualCallCost(usage CallUsage, model profile.ModelCapability) (int64, bool) {
	if !usage.Present || !usage.InputPresent || !usage.OutputPresent ||
		!model.HasInputPrice || !model.HasOutputPrice ||
		usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.CacheReadTokens < 0 || usage.CacheCreationTokens < 0 ||
		usage.CacheReadTokens > 0 || usage.CacheCreationTokens > 0 {
		return 0, false
	}
	input, ok := checkedTokenCost(usage.InputTokens, model.InputPriceMicroUSDPerMillion)
	if !ok {
		return 0, false
	}
	output, ok := checkedTokenCost(usage.OutputTokens, model.OutputPriceMicroUSDPerMillion)
	if !ok || input > math.MaxInt64-output {
		return 0, false
	}
	return input + output, true
}

func checkedTokenCost(tokens int, price int64) (int64, bool) {
	if tokens < 0 || price < 0 {
		return 0, false
	}
	if tokens == 0 || price == 0 {
		return 0, true
	}
	if int64(tokens) > math.MaxInt64/price {
		return 0, false
	}
	product := int64(tokens) * price
	result := product / 1_000_000
	if product%1_000_000 != 0 {
		if result == math.MaxInt64 {
			return 0, false
		}
		result++
	}
	return result, true
}
