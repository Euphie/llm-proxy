package routing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

const maxCost = math.MaxInt64

var ErrAttemptBudgetExceeded = errors.New("attempt budget exceeded")

type CallKind string

const (
	CallAnalyzer CallKind = "analyzer"
	CallVision   CallKind = "vision"
	CallAnswer   CallKind = "answer"
)

type AttemptBudget struct {
	mu       sync.Mutex
	limits   profile.AttemptBudgetRuntime
	used     AttemptBudgetSnapshot
	held     budgetCapacity
	deadline time.Time
	context  context.Context
}

type AttemptBudgetSnapshot struct {
	AnswerAttempts        int
	AuxiliaryCalls        int
	TotalOutboundCalls    int
	ModelSwitches         int
	WorstCaseCostMicroUSD int64
	RetriesByTarget       map[string]int
	HeldAnswerAttempts    int
	HeldAuxiliaryCalls    int
	HeldOutboundCalls     int
	HeldCostMicroUSD      int64
	Deadline              time.Time
}

type budgetCapacity struct {
	answerAttempts int
	auxiliaryCalls int
	totalCalls     int
	costMicroUSD   int64
}

func NewAttemptBudget(
	parent context.Context,
	limits profile.AttemptBudgetRuntime,
) (*AttemptBudget, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, limits.Deadline)
	deadline, _ := ctx.Deadline()
	return &AttemptBudget{
		limits:   limits,
		deadline: deadline,
		context:  ctx,
		used: AttemptBudgetSnapshot{
			RetriesByTarget: make(map[string]int),
		},
	}, ctx, cancel
}

func (b *AttemptBudget) ReserveCall(ctx context.Context, kind CallKind, cost int64) error {
	if err := b.contextError(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return err
	}
	return b.reserveCallLocked(kind, cost)
}

func (b *AttemptBudget) ReserveRetry(
	ctx context.Context,
	target string,
	cost int64,
) error {
	if err := b.contextError(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return err
	}
	if target == "" || b.used.RetriesByTarget[target] >= b.limits.MaxRetriesPerTarget {
		return fmt.Errorf("%w: retries for target %q", ErrAttemptBudgetExceeded, target)
	}
	if err := b.canReserveCallLocked(CallAnswer, cost); err != nil {
		return err
	}
	b.used.RetriesByTarget[target]++
	b.applyCallLocked(CallAnswer, cost)
	return nil
}

func (b *AttemptBudget) ReserveModelSwitch() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used.ModelSwitches >= b.limits.MaxModelSwitches {
		return fmt.Errorf("%w: model switches", ErrAttemptBudgetExceeded)
	}
	b.used.ModelSwitches++
	return nil
}

func (b *AttemptBudget) CanReserveModelSwitchCall(
	ctx context.Context,
	cost int64,
) error {
	if err := b.contextError(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return err
	}
	return b.canReserveModelSwitchCallLocked(cost)
}

func (b *AttemptBudget) ReserveModelSwitchCall(
	ctx context.Context,
	cost int64,
) error {
	if err := b.contextError(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return err
	}
	if err := b.canReserveModelSwitchCallLocked(cost); err != nil {
		return err
	}
	b.used.ModelSwitches++
	b.applyCallLocked(CallAnswer, cost)
	return nil
}

func (b *AttemptBudget) contextError(caller context.Context) error {
	if err := caller.Err(); err != nil {
		return err
	}
	if b.context != nil {
		return b.context.Err()
	}
	return nil
}

func (b *AttemptBudget) Snapshot() AttemptBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := b.used
	snapshot.RetriesByTarget = make(map[string]int, len(b.used.RetriesByTarget))
	for target, retries := range b.used.RetriesByTarget {
		snapshot.RetriesByTarget[target] = retries
	}
	snapshot.HeldAnswerAttempts = b.held.answerAttempts
	snapshot.HeldAuxiliaryCalls = b.held.auxiliaryCalls
	snapshot.HeldOutboundCalls = b.held.totalCalls
	snapshot.HeldCostMicroUSD = b.held.costMicroUSD
	snapshot.Deadline = b.deadline
	return snapshot
}

func (b *AttemptBudget) reserveCallLocked(kind CallKind, cost int64) error {
	if err := b.canReserveCallLocked(kind, cost); err != nil {
		return err
	}
	b.applyCallLocked(kind, cost)
	return nil
}

func (b *AttemptBudget) canReserveCallLocked(kind CallKind, cost int64) error {
	if cost < 0 {
		return fmt.Errorf("%w: negative cost", ErrAttemptBudgetExceeded)
	}
	if exceedsCount(b.used.TotalOutboundCalls, b.held.totalCalls, 1, b.limits.MaxTotalOutboundCalls) {
		return fmt.Errorf("%w: total outbound calls", ErrAttemptBudgetExceeded)
	}
	switch kind {
	case CallAnalyzer, CallVision:
		if exceedsCount(b.used.AuxiliaryCalls, b.held.auxiliaryCalls, 1, b.limits.MaxAuxiliaryCalls) {
			return fmt.Errorf("%w: auxiliary calls", ErrAttemptBudgetExceeded)
		}
	case CallAnswer:
		if exceedsCount(b.used.AnswerAttempts, b.held.answerAttempts, 1, b.limits.MaxAnswerAttempts) {
			return fmt.Errorf("%w: answer attempts", ErrAttemptBudgetExceeded)
		}
	default:
		return fmt.Errorf("%w: unknown call kind %q", ErrAttemptBudgetExceeded, kind)
	}
	usedAndHeld := addCost(b.used.WorstCaseCostMicroUSD, b.held.costMicroUSD)
	nextCost := addCost(usedAndHeld, cost)
	if usedAndHeld == maxCost || exceedsCostLimit(
		nextCost,
		b.limits.MaxWorstCaseCostMicroUSD,
	) {
		return fmt.Errorf("%w: worst-case cost", ErrAttemptBudgetExceeded)
	}
	return nil
}

func exceedsCostLimit(cost, limit int64) bool {
	return cost == maxCost || limit > 0 && cost > limit
}

func (b *AttemptBudget) canReserveModelSwitchCallLocked(cost int64) error {
	if b.used.ModelSwitches >= b.limits.MaxModelSwitches {
		return fmt.Errorf("%w: model switches", ErrAttemptBudgetExceeded)
	}
	return b.canReserveCallLocked(CallAnswer, cost)
}

func (b *AttemptBudget) applyCallLocked(kind CallKind, cost int64) {
	b.used.TotalOutboundCalls++
	if kind == CallAnswer {
		b.used.AnswerAttempts++
	} else {
		b.used.AuxiliaryCalls++
	}
	b.used.WorstCaseCostMicroUSD = addCost(b.used.WorstCaseCostMicroUSD, cost)
}
