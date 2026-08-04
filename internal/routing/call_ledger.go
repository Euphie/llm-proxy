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
	Usage             CallUsage
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
	InputIncludesCache  bool
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
	entry.Usage = usage
	entry.ActualCostKnown = known
	if known {
		entry.ActualMicroUSD = actual
	}
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
		(usage.CacheReadTokens > 0 && !model.HasCacheReadPrice) ||
		(usage.CacheCreationTokens > 0 && !model.HasCacheWritePrice) {
		return 0, false
	}
	uncachedInput := usage.InputTokens
	if usage.InputIncludesCache {
		cachedInput := usage.CacheReadTokens + usage.CacheCreationTokens
		if cachedInput < 0 || cachedInput > usage.InputTokens {
			return 0, false
		}
		uncachedInput -= cachedInput
	}
	components := []struct {
		tokens int
		price  int64
	}{
		{uncachedInput, model.InputPriceMicroUSDPerMillion},
		{usage.CacheReadTokens, model.CacheReadPriceMicroUSDPerMillion},
		{usage.CacheCreationTokens, model.CacheWritePriceMicroUSDPerMillion},
		{usage.OutputTokens, model.OutputPriceMicroUSDPerMillion},
	}
	totalNumerator := int64(0)
	for _, component := range components {
		if component.tokens < 0 || component.price < 0 ||
			(component.tokens > 0 && component.price > 0 &&
				int64(component.tokens) > math.MaxInt64/component.price) {
			return 0, false
		}
		numerator := int64(component.tokens) * component.price
		if totalNumerator > math.MaxInt64-numerator {
			return 0, false
		}
		totalNumerator += numerator
	}
	result := totalNumerator / 1_000_000
	if totalNumerator%1_000_000 != 0 {
		result++
	}
	return result, true
}

func ActualCallCost(usage CallUsage, model profile.ModelCapability) (int64, bool) {
	return actualCallCost(usage, model)
}
