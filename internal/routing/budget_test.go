package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestAttemptBudgetReservesCallsAtomically(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 3,
		MaxRetriesPerTarget: 1, MaxModelSwitches: 1, MaxTargetSwitches: 1,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()

	if err := budget.ReserveCall(ctx, CallAnalyzer, 10); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveCall(ctx, CallVision, 10); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("second auxiliary error=%v", err)
	}
	if err := budget.ReserveCall(ctx, CallAnswer, 30); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveRetry(ctx, "primary", 30); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveRetry(ctx, "primary", 1); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("second retry error=%v", err)
	}
	if err := budget.ReserveCall(ctx, CallAnswer, 1); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("third answer error=%v", err)
	}

	snapshot := budget.Snapshot()
	if snapshot.TotalOutboundCalls != 3 || snapshot.AuxiliaryCalls != 1 ||
		snapshot.AnswerAttempts != 2 || snapshot.WorstCaseCostMicroUSD != 70 ||
		snapshot.RetriesByTarget["primary"] != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAttemptBudgetRejectsCostAndSwitchOverflowWithoutMutation(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		MaxModelSwitches: 1, MaxTargetSwitches: 1,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 10,
	})
	defer cancel()

	if err := budget.ReserveCall(ctx, CallAnswer, 11); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("cost error=%v", err)
	}
	if snapshot := budget.Snapshot(); snapshot.TotalOutboundCalls != 0 || snapshot.WorstCaseCostMicroUSD != 0 {
		t.Fatalf("failed reservation mutated budget: %+v", snapshot)
	}
	if err := budget.ReserveModelSwitch(); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveModelSwitch(); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("model switch error=%v", err)
	}
	if err := budget.ReserveTargetSwitch(); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveTargetSwitch(); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("target switch error=%v", err)
	}
}

func TestAttemptBudgetUsesOneDeadlineAndCancellationSignal(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	budget, ctx, cancel := NewAttemptBudget(parent, profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 10,
	})
	defer cancel()
	parentCancel()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("budget context did not follow parent cancellation")
	}
	if err := budget.ReserveCall(ctx, CallAnswer, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReserveCall() error=%v, want context.Canceled", err)
	}
}
