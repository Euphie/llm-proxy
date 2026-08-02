package routing

import (
	"math"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestCallLedgerRecordsOnlyStructuredCallMetadataInSequence(t *testing.T) {
	ledger := NewCallLedger("trace-123")
	vision := ledger.Begin(CallTicket{
		Kind: CallVision, Model: "vision", Target: "primary",
		ImageIndex: 2, RetryIndex: 1, EstimatedMicroUSD: 40,
	}, 0, 0)
	ledger.Complete(vision, 503, "overload")
	answer := ledger.Begin(CallTicket{
		Kind: CallAnswer, Model: "strong", Target: "region-b",
		ImageIndex: -1, RetryIndex: 0, EstimatedMicroUSD: 70,
	}, 1, 1)
	ledger.Complete(answer, 200, "success")

	rows := ledger.Snapshot()
	if len(rows) != 2 || rows[0].Sequence != 1 || rows[1].Sequence != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].CorrelationID != "trace-123" || rows[0].Kind != CallVision ||
		rows[0].ImageIndex != 2 || rows[0].RetryIndex != 1 ||
		rows[0].EstimatedMicroUSD != 40 || rows[0].ActualCostKnown ||
		rows[0].StatusCode != 503 || rows[0].Outcome != "overload" {
		t.Fatalf("vision row=%+v", rows[0])
	}
	if rows[1].ModelSwitchIndex != 1 || rows[1].TargetSwitchIndex != 1 ||
		rows[1].StatusCode != 200 || rows[1].Outcome != "success" {
		t.Fatalf("answer row=%+v", rows[1])
	}
}

func TestCallLedgerCompletesWithKnownActualCost(t *testing.T) {
	ledger := NewCallLedger("trace-known")
	sequence := ledger.Begin(CallTicket{
		Kind: CallAnswer, Model: "zero", Target: "primary", ImageIndex: -1,
		EstimatedMicroUSD: 99,
	}, 0, 0)
	ledger.CompleteWithUsage(sequence, 200, "success", CallUsage{
		InputTokens: 12, OutputTokens: 3, InputPresent: true, OutputPresent: true, Present: true,
	}, profile.ModelCapability{
		HasInputPrice: true, InputPriceMicroUSDPerMillion: 0,
		HasOutputPrice: true, OutputPriceMicroUSDPerMillion: 0,
	})

	row := ledger.Snapshot()[0]
	if !row.ActualCostKnown || row.ActualMicroUSD != 0 || row.EstimatedMicroUSD != 99 {
		t.Fatalf("row=%+v", row)
	}
}

func TestCallLedgerActualCostUnknownForMissingCachePricesOrOverflow(t *testing.T) {
	model := profile.ModelCapability{
		HasInputPrice: true, InputPriceMicroUSDPerMillion: math.MaxInt64,
		HasOutputPrice: true, OutputPriceMicroUSDPerMillion: math.MaxInt64,
	}
	for _, test := range []struct {
		name  string
		usage CallUsage
	}{
		{name: "cache pricing absent", usage: CallUsage{InputTokens: 10, InputPresent: true, OutputPresent: true, CacheReadTokens: 1, Present: true}},
		{name: "overflow", usage: CallUsage{InputTokens: math.MaxInt, OutputTokens: math.MaxInt, InputPresent: true, OutputPresent: true, Present: true}},
		{name: "partial SSE", usage: CallUsage{InputTokens: 10, InputPresent: true, Present: true}},
		{name: "usage absent", usage: CallUsage{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := NewCallLedger("trace-unknown")
			sequence := ledger.Begin(CallTicket{Kind: CallAnswer, EstimatedMicroUSD: 17}, 0, 0)
			ledger.CompleteWithUsage(sequence, 200, "success", test.usage, model)
			row := ledger.Snapshot()[0]
			if row.ActualCostKnown || row.ActualMicroUSD != 0 || row.EstimatedMicroUSD != 17 {
				t.Fatalf("row=%+v", row)
			}
		})
	}
}

func TestCallLedgerAggregateDistinguishesKnownAndUnknownActualCost(t *testing.T) {
	ledger := NewCallLedger("trace-aggregate")
	first := ledger.Begin(CallTicket{Kind: CallAnalyzer, EstimatedMicroUSD: 3}, 0, 0)
	second := ledger.Begin(CallTicket{Kind: CallAnswer, EstimatedMicroUSD: 7}, 0, 0)
	ledger.CompleteWithActual(first, 200, "success", true, 2)
	ledger.Complete(second, 503, "upstream")

	aggregate := ledger.Aggregate()
	if aggregate.EstimatedConsumedMicroUSD != 10 || aggregate.KnownActualMicroUSD != 2 ||
		aggregate.AllActualCostsKnown || aggregate.PhysicalCalls != 2 ||
		aggregate.CorrelationID != "trace-aggregate" {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}

func TestCallLedgerAggregateMarksOverflowedActualCostUnknown(t *testing.T) {
	ledger := NewCallLedger("trace-overflow")
	first := ledger.Begin(CallTicket{Kind: CallAnswer}, 0, 0)
	second := ledger.Begin(CallTicket{Kind: CallAnswer}, 0, 0)
	ledger.CompleteWithActual(first, 200, "success", true, math.MaxInt64)
	ledger.CompleteWithActual(second, 200, "success", true, 1)

	aggregate := ledger.Aggregate()
	if aggregate.AllActualCostsKnown || aggregate.KnownActualMicroUSD != math.MaxInt64 {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}
