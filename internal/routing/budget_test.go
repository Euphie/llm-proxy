package routing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

func TestAttemptBudgetZeroCostLimitAllowsPositiveCost(t *testing.T) {
	limits := profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 3,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 0,
	}

	t.Run("direct call", func(t *testing.T) {
		budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
		defer cancel()
		if err := budget.ReserveCall(ctx, CallAnswer, 25); err != nil {
			t.Fatalf("reserve positive cost with unlimited budget: %v", err)
		}
	})

	t.Run("attempt lease", func(t *testing.T) {
		budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
		defer cancel()
		lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
			Model: "fast", Target: "primary", AnswerCallCostMicroUSD: 25,
		})
		if err != nil {
			t.Fatalf("reserve attempt with unlimited budget: %v", err)
		}
		lease.ReleaseUnused()
	})
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

func TestAttemptBudgetAtomicallyReservesModelSwitchAndAnswer(t *testing.T) {
	limits := profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 3,
		MaxModelSwitches: 1, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: 100,
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
	defer cancel()

	if err := budget.ReserveCall(ctx, CallAnswer, 10); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveModelSwitchCall(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveModelSwitchCall(ctx, 30); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("second switch error=%v", err)
	}
	snapshot := budget.Snapshot()
	if snapshot.ModelSwitches != 1 || snapshot.AnswerAttempts != 2 ||
		snapshot.TotalOutboundCalls != 2 || snapshot.WorstCaseCostMicroUSD != 30 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAttemptBudgetFailedModelSwitchDoesNotConsumeSwitchSlot(t *testing.T) {
	limits := profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		MaxModelSwitches: 1, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: 100,
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
	defer cancel()

	if err := budget.ReserveCall(ctx, CallAnswer, 10); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveModelSwitchCall(ctx, 20); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("switch error=%v", err)
	}
	if snapshot := budget.Snapshot(); snapshot.ModelSwitches != 0 {
		t.Fatalf("failed switch consumed budget: %+v", snapshot)
	}
}

func TestAttemptBudgetAtomicallyReservesTargetSwitchAndAnswer(t *testing.T) {
	limits := profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 2, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 3,
		MaxTargetSwitches: 1, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: 100,
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
	defer cancel()

	if err := budget.ReserveCall(ctx, CallAnswer, 10); err != nil {
		t.Fatal(err)
	}
	if err := budget.CanReserveTargetSwitchCall(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveTargetSwitchCall(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveTargetSwitchCall(ctx, 30); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("second switch error=%v", err)
	}
	snapshot := budget.Snapshot()
	if snapshot.TargetSwitches != 1 || snapshot.AnswerAttempts != 2 ||
		snapshot.TotalOutboundCalls != 2 || snapshot.WorstCaseCostMicroUSD != 30 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAttemptBudgetFailedTargetSwitchDoesNotConsumeSwitchSlot(t *testing.T) {
	limits := profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		MaxTargetSwitches: 1, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: 100,
	}
	budget, ctx, cancel := NewAttemptBudget(context.Background(), limits)
	defer cancel()
	if err := budget.ReserveCall(ctx, CallAnswer, 10); err != nil {
		t.Fatal(err)
	}
	if err := budget.ReserveTargetSwitchCall(ctx, 20); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("switch error=%v", err)
	}
	if snapshot := budget.Snapshot(); snapshot.TargetSwitches != 0 || snapshot.AnswerAttempts != 1 {
		t.Fatalf("failed switch mutated budget: %+v", snapshot)
	}
}

func TestAttemptBudgetRejectsCompositeAttemptBeforeAnyCallWhenWholeAttemptDoesNotFit(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()

	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "text-only", Target: "primary",
		VisionCalls: 1, VisionCallCostMicroUSD: 60,
		AnswerCallCostMicroUSD: 50,
	})
	if !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("ReserveAttempt() error=%v, want ErrAttemptBudgetExceeded", err)
	}
	if lease != nil {
		t.Fatal("ReserveAttempt() returned a lease after failed admission")
	}
	snapshot := budget.Snapshot()
	if snapshot.TotalOutboundCalls != 0 || snapshot.HeldOutboundCalls != 0 ||
		snapshot.WorstCaseCostMicroUSD != 0 || snapshot.HeldCostMicroUSD != 0 {
		t.Fatalf("failed admission mutated budget: %+v", snapshot)
	}
}

func TestAttemptLeaseMovesHeldCapacityToConsumedAndReleasesUnused(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 3,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 200,
	})
	defer cancel()

	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "text-only", Target: "primary",
		VisionCalls: 2, VisionCallCostMicroUSD: 40,
		AnswerCallCostMicroUSD: 70,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := budget.Snapshot(); got.HeldOutboundCalls != 3 ||
		got.HeldCostMicroUSD != 150 || got.TotalOutboundCalls != 0 {
		t.Fatalf("snapshot after admission=%+v", got)
	}
	if _, err := lease.ConsumeVision(ctx, 0, 0); err != nil {
		t.Fatal(err)
	}
	lease.ReleaseUnused()
	if got := budget.Snapshot(); got.HeldOutboundCalls != 0 ||
		got.HeldCostMicroUSD != 0 || got.TotalOutboundCalls != 1 ||
		got.AuxiliaryCalls != 1 || got.WorstCaseCostMicroUSD != 40 {
		t.Fatalf("snapshot after consume/release=%+v", got)
	}
}

func TestAttemptBudgetLegacyReservationCannotOversubscribeHeldLease(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "text-only", Target: "primary",
		VisionCalls: 1, VisionCallCostMicroUSD: 40,
		AnswerCallCostMicroUSD: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.ReleaseUnused()

	result := make(chan error, 1)
	go func() {
		result <- budget.ReserveCall(ctx, CallAnalyzer, 1)
	}()
	if err := <-result; !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("ReserveCall() error=%v, want held-capacity rejection", err)
	}
	if got := budget.Snapshot(); got.TotalOutboundCalls != 0 ||
		got.HeldOutboundCalls != 2 || got.HeldCostMicroUSD != 100 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestAttemptLeaseEnforcesFrozenVisionCallsPerImage(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 3, MaxTotalOutboundCalls: 4,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "text-only", Target: "primary",
		VisionCallsPerImage: []int{2, 1}, VisionCallCostMicroUSD: 10,
		AnswerCallCostMicroUSD: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.ReleaseUnused()
	for retry := range 2 {
		if _, err := lease.ConsumeVision(ctx, 0, retry); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lease.ConsumeVision(ctx, 0, 2); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("third image-0 call error=%v, want frozen quota rejection", err)
	}
	if _, err := lease.ConsumeVision(ctx, 1, 0); err != nil {
		t.Fatalf("image-1 call: %v", err)
	}
}

func TestAttemptBudgetSnapshotFreezesEffectiveAbsoluteDeadline(t *testing.T) {
	parentDeadline := time.Now().Add(30 * time.Second)
	parent, parentCancel := context.WithDeadline(context.Background(), parentDeadline)
	defer parentCancel()
	budget, _, cancel := NewAttemptBudget(parent, profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()

	first := budget.Snapshot().Deadline
	time.Sleep(time.Millisecond)
	second := budget.Snapshot().Deadline
	if first.IsZero() || !first.Equal(second) || first.After(parentDeadline) ||
		first.Before(parentDeadline.Add(-time.Second)) {
		t.Fatalf("deadlines first=%v second=%v parent=%v", first, second, parentDeadline)
	}
}

func TestAttemptBudgetConcurrentLeasesCannotOversubscribe(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	start := make(chan struct{})
	var admitted atomic.Int32
	var leasesMu sync.Mutex
	var leases []*AttemptLease
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
				Model: "text-only", Target: "primary",
				VisionCalls: 1, VisionCallCostMicroUSD: 40,
				AnswerCallCostMicroUSD: 60,
			})
			if err == nil {
				admitted.Add(1)
				leasesMu.Lock()
				leases = append(leases, lease)
				leasesMu.Unlock()
				return
			}
			if !errors.Is(err, ErrAttemptBudgetExceeded) {
				t.Errorf("ReserveAttempt() error=%v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted leases=%d, want 1", admitted.Load())
	}
	for _, lease := range leases {
		lease.ReleaseUnused()
	}
}

func TestAttemptBudgetReservationOverflowFailsWithoutMutation(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: maxInt, MaxAuxiliaryCalls: maxInt,
		MaxTotalOutboundCalls: maxInt, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: int64(^uint64(0) >> 1),
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		VisionCalls: maxInt, VisionCallCostMicroUSD: 2,
		AnswerCallCostMicroUSD: 1,
	})
	if !errors.Is(err, ErrAttemptBudgetExceeded) || lease != nil {
		t.Fatalf("ReserveAttempt() lease=%v error=%v", lease, err)
	}
	if got := budget.Snapshot(); got.TotalOutboundCalls != 0 ||
		got.HeldOutboundCalls != 0 || got.WorstCaseCostMicroUSD != 0 ||
		got.HeldCostMicroUSD != 0 {
		t.Fatalf("overflow mutated budget: %+v", got)
	}
}

func TestAttemptLeaseChecksDeadlineBeforeConsumingTicket(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		Deadline: 5 * time.Millisecond, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		VisionCalls: 1, VisionCallCostMicroUSD: 10, AnswerCallCostMicroUSD: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if _, err := lease.ConsumeVision(context.Background(), 0, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConsumeVision() error=%v, want budget deadline", err)
	}
	if got := budget.Snapshot(); got.TotalOutboundCalls != 0 || got.HeldOutboundCalls != 2 {
		t.Fatalf("deadline consume mutated budget: %+v", got)
	}
	lease.ReleaseUnused()
}

func TestAttemptBudgetAtomicallyAdmitsNodeSwitchWithCompositeCapacity(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 2,
		MaxModelSwitches: 0, MaxTargetSwitches: 1,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	failed, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "strong", Target: "primary", ModelSwitch: true,
		VisionCalls: 1, VisionCallCostMicroUSD: 40, AnswerCallCostMicroUSD: 60,
	})
	if !errors.Is(err, ErrAttemptBudgetExceeded) || failed != nil {
		t.Fatalf("model-switch admission lease=%v error=%v", failed, err)
	}
	if got := budget.Snapshot(); got.ModelSwitches != 0 || got.HeldOutboundCalls != 0 {
		t.Fatalf("failed switch admission mutated budget: %+v", got)
	}

	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "fast", Target: "backup", TargetSwitch: true,
		VisionCalls: 1, VisionCallCostMicroUSD: 40, AnswerCallCostMicroUSD: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.ReleaseUnused()
	if got := budget.Snapshot(); got.TargetSwitches != 1 || got.ModelSwitches != 0 ||
		got.HeldOutboundCalls != 0 || got.TotalOutboundCalls != 0 {
		t.Fatalf("successful switch admission snapshot=%+v", got)
	}
}

func TestAttemptLeaseConsumesOnlyFrozenAnswerRetryTickets(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 3, MaxAuxiliaryCalls: 1, MaxTotalOutboundCalls: 3,
		MaxRetriesPerTarget: 2, Deadline: time.Minute,
		MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		Model: "fast", Target: "primary", AnswerCalls: 3,
		AnswerCallCostMicroUSD: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.ReleaseUnused()
	if _, err := lease.ConsumeAnswer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.ConsumeAnswerRetry(ctx, 1); err != nil {
		t.Fatalf("retry 1: %v", err)
	}
	if _, err := lease.ConsumeAnswerRetry(ctx, 1); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("duplicate retry index error=%v", err)
	}
	if _, err := lease.ConsumeAnswerRetry(ctx, 2); err != nil {
		t.Fatalf("retry 2: %v", err)
	}
	if _, err := lease.ConsumeAnswerRetry(ctx, 3); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("extra retry error=%v, want frozen ticket rejection", err)
	}
	if got := budget.Snapshot(); got.AnswerAttempts != 3 ||
		got.RetriesByTarget["primary"] != 2 || got.HeldOutboundCalls != 0 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestAttemptLeaseReleasesUnusedVisionBeforeAnswer(t *testing.T) {
	budget, ctx, cancel := NewAttemptBudget(context.Background(), profile.AttemptBudgetRuntime{
		MaxAnswerAttempts: 1, MaxAuxiliaryCalls: 2, MaxTotalOutboundCalls: 3,
		Deadline: time.Minute, MaxWorstCaseCostMicroUSD: 100,
	})
	defer cancel()
	lease, err := budget.ReserveAttempt(ctx, AttemptReservation{
		VisionCalls: 2, VisionCallCostMicroUSD: 10,
		AnswerCallCostMicroUSD: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.ReleaseUnusedVision()
	if got := budget.Snapshot(); got.HeldAuxiliaryCalls != 0 ||
		got.HeldOutboundCalls != 1 || got.HeldCostMicroUSD != 20 {
		t.Fatalf("snapshot=%+v", got)
	}
	if _, err := lease.ConsumeAnswer(ctx); err != nil {
		t.Fatal(err)
	}
	lease.ReleaseUnused()
}
