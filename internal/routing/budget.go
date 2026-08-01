package routing

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Euphie/llm-proxy/internal/profile"
)

var ErrAttemptBudgetExceeded = errors.New("attempt budget exceeded")

type CallKind string

const (
	CallAnalyzer CallKind = "analyzer"
	CallVision   CallKind = "vision"
	CallAnswer   CallKind = "answer"
)

type AttemptBudget struct {
	mu     sync.Mutex
	limits profile.AttemptBudgetRuntime
	used   AttemptBudgetSnapshot
}

type AttemptBudgetSnapshot struct {
	AnswerAttempts        int
	AuxiliaryCalls        int
	TotalOutboundCalls    int
	ModelSwitches         int
	TargetSwitches        int
	WorstCaseCostMicroUSD int64
	RetriesByTarget       map[string]int
}

func NewAttemptBudget(
	parent context.Context,
	limits profile.AttemptBudgetRuntime,
) (*AttemptBudget, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, limits.Deadline)
	return &AttemptBudget{
		limits: limits,
		used: AttemptBudgetSnapshot{
			RetriesByTarget: make(map[string]int),
		},
	}, ctx, cancel
}

func (b *AttemptBudget) ReserveCall(ctx context.Context, kind CallKind, cost int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.reserveCallLocked(kind, cost)
}

func (b *AttemptBudget) ReserveRetry(
	ctx context.Context,
	target string,
	cost int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.canReserveModelSwitchCallLocked(cost)
}

func (b *AttemptBudget) ReserveModelSwitchCall(
	ctx context.Context,
	cost int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.canReserveModelSwitchCallLocked(cost); err != nil {
		return err
	}
	b.used.ModelSwitches++
	b.applyCallLocked(CallAnswer, cost)
	return nil
}

func (b *AttemptBudget) ReserveTargetSwitch() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used.TargetSwitches >= b.limits.MaxTargetSwitches {
		return fmt.Errorf("%w: target switches", ErrAttemptBudgetExceeded)
	}
	b.used.TargetSwitches++
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
	if b.used.TotalOutboundCalls >= b.limits.MaxTotalOutboundCalls {
		return fmt.Errorf("%w: total outbound calls", ErrAttemptBudgetExceeded)
	}
	switch kind {
	case CallAnalyzer, CallVision:
		if b.used.AuxiliaryCalls >= b.limits.MaxAuxiliaryCalls {
			return fmt.Errorf("%w: auxiliary calls", ErrAttemptBudgetExceeded)
		}
	case CallAnswer:
		if b.used.AnswerAttempts >= b.limits.MaxAnswerAttempts {
			return fmt.Errorf("%w: answer attempts", ErrAttemptBudgetExceeded)
		}
	default:
		return fmt.Errorf("%w: unknown call kind %q", ErrAttemptBudgetExceeded, kind)
	}
	nextCost := addCost(b.used.WorstCaseCostMicroUSD, cost)
	if nextCost > b.limits.MaxWorstCaseCostMicroUSD {
		return fmt.Errorf("%w: worst-case cost", ErrAttemptBudgetExceeded)
	}
	return nil
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
