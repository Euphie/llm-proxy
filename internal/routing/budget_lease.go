package routing

import (
	"context"
	"fmt"
)

type AttemptReservation struct {
	Model                  string
	Target                 string
	VisionCalls            int
	VisionCallsPerImage    []int
	VisionCallCostMicroUSD int64
	AnswerCalls            int
	AnswerCallCostMicroUSD int64
	ModelSwitch            bool
	TargetSwitch           bool
}

type CallTicket struct {
	Kind              CallKind
	Model             string
	Target            string
	ImageIndex        int
	RetryIndex        int
	EstimatedMicroUSD int64
}

type AttemptLease struct {
	budget          *AttemptBudget
	model           string
	target          string
	visionRemaining int
	visionByImage   map[int]int
	visionCost      int64
	answerRemaining bool
	answerRetries   int
	nextAnswerRetry int
	answerCost      int64
	released        bool
}

func (b *AttemptBudget) ReserveAttempt(
	ctx context.Context,
	reservation AttemptReservation,
) (*AttemptLease, error) {
	if err := b.contextError(ctx); err != nil {
		return nil, err
	}
	visionCalls, visionByImage, ok := reservationVisionCalls(reservation)
	answerCalls := reservation.AnswerCalls
	if answerCalls == 0 {
		answerCalls = 1
	}
	if !ok || answerCalls < 1 || reservation.VisionCallCostMicroUSD < 0 ||
		reservation.AnswerCallCostMicroUSD < 0 ||
		reservation.ModelSwitch && reservation.TargetSwitch {
		return nil, fmt.Errorf("%w: invalid attempt reservation", ErrAttemptBudgetExceeded)
	}
	visionCost := multiplyCost(reservation.VisionCallCostMicroUSD, visionCalls)
	totalCost := addCost(visionCost, reservation.AnswerCallCostMicroUSD)
	totalCalls, ok := addCount(visionCalls, 1)
	if !ok || totalCost == maxCost {
		return nil, fmt.Errorf("%w: attempt reservation overflow", ErrAttemptBudgetExceeded)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return nil, err
	}
	if reservation.ModelSwitch && b.used.ModelSwitches >= b.limits.MaxModelSwitches {
		return nil, fmt.Errorf("%w: model switches", ErrAttemptBudgetExceeded)
	}
	if reservation.TargetSwitch && b.used.TargetSwitches >= b.limits.MaxTargetSwitches {
		return nil, fmt.Errorf("%w: target switches", ErrAttemptBudgetExceeded)
	}
	requested := budgetCapacity{
		answerAttempts: 1,
		auxiliaryCalls: visionCalls,
		totalCalls:     totalCalls,
		costMicroUSD:   totalCost,
	}
	if err := b.canHoldLocked(requested); err != nil {
		return nil, err
	}
	b.held.answerAttempts++
	b.held.auxiliaryCalls += visionCalls
	b.held.totalCalls += totalCalls
	b.held.costMicroUSD = addCost(b.held.costMicroUSD, totalCost)
	if reservation.ModelSwitch {
		b.used.ModelSwitches++
	}
	if reservation.TargetSwitch {
		b.used.TargetSwitches++
	}
	return &AttemptLease{
		budget: b, model: reservation.Model, target: reservation.Target,
		visionRemaining: visionCalls,
		visionByImage:   visionByImage,
		visionCost:      reservation.VisionCallCostMicroUSD,
		answerRemaining: true,
		answerRetries:   answerCalls - 1,
		nextAnswerRetry: 1,
		answerCost:      reservation.AnswerCallCostMicroUSD,
	}, nil
}

func (l *AttemptLease) ConsumeVision(
	ctx context.Context,
	imageIndex int,
	retryIndex int,
) (*CallTicket, error) {
	return l.consume(ctx, CallVision, imageIndex, retryIndex)
}

func (l *AttemptLease) ConsumeAnswer(ctx context.Context) (*CallTicket, error) {
	return l.consume(ctx, CallAnswer, -1, 0)
}

func (l *AttemptLease) ConsumeAnswerRetry(
	ctx context.Context,
	retryIndex int,
) (*CallTicket, error) {
	if l == nil || l.budget == nil {
		return nil, fmt.Errorf("%w: missing attempt lease", ErrAttemptBudgetExceeded)
	}
	if err := l.budget.contextError(ctx); err != nil {
		return nil, err
	}
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return nil, err
	}
	if l.released || l.answerRemaining || l.answerRetries <= 0 ||
		retryIndex != l.nextAnswerRetry {
		return nil, fmt.Errorf("%w: answer retry ticket", ErrAttemptBudgetExceeded)
	}
	if l.target == "" || b.used.RetriesByTarget[l.target] >= b.limits.MaxRetriesPerTarget {
		return nil, fmt.Errorf("%w: retries for target %q", ErrAttemptBudgetExceeded, l.target)
	}
	if err := b.canReserveCallLocked(CallAnswer, l.answerCost); err != nil {
		return nil, err
	}
	l.answerRetries--
	l.nextAnswerRetry++
	b.used.RetriesByTarget[l.target]++
	b.applyCallLocked(CallAnswer, l.answerCost)
	return &CallTicket{
		Kind: CallAnswer, Model: l.model, Target: l.target,
		ImageIndex: -1, RetryIndex: retryIndex,
		EstimatedMicroUSD: l.answerCost,
	}, nil
}

func (l *AttemptLease) ReleaseUnusedVision() {
	if l == nil || l.budget == nil {
		return
	}
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	l.releaseUnusedVisionLocked()
}

func (l *AttemptLease) ReleaseUnused() {
	if l == nil || l.budget == nil {
		return
	}
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if l.released {
		return
	}
	l.releaseUnusedVisionLocked()
	if l.answerRemaining {
		b.held.answerAttempts--
		b.held.totalCalls--
		b.held.costMicroUSD -= l.answerCost
	}
	l.visionRemaining = 0
	l.answerRetries = 0
	l.answerRemaining = false
	l.released = true
}

func (l *AttemptLease) releaseUnusedVisionLocked() {
	if l.visionRemaining <= 0 {
		return
	}
	b := l.budget
	b.held.auxiliaryCalls -= l.visionRemaining
	b.held.totalCalls -= l.visionRemaining
	b.held.costMicroUSD -= multiplyCost(l.visionCost, l.visionRemaining)
	l.visionRemaining = 0
	for imageIndex := range l.visionByImage {
		l.visionByImage[imageIndex] = 0
	}
}

func (l *AttemptLease) consume(
	ctx context.Context,
	kind CallKind,
	imageIndex int,
	retryIndex int,
) (*CallTicket, error) {
	if l == nil || l.budget == nil {
		return nil, fmt.Errorf("%w: missing attempt lease", ErrAttemptBudgetExceeded)
	}
	if err := l.budget.contextError(ctx); err != nil {
		return nil, err
	}
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextError(ctx); err != nil {
		return nil, err
	}
	if l.released {
		return nil, fmt.Errorf("%w: attempt lease released", ErrAttemptBudgetExceeded)
	}
	cost := int64(0)
	switch kind {
	case CallVision:
		if l.visionRemaining <= 0 {
			return nil, fmt.Errorf("%w: visual calls", ErrAttemptBudgetExceeded)
		}
		if l.visionByImage != nil {
			remaining, exists := l.visionByImage[imageIndex]
			if !exists || remaining <= 0 {
				return nil, fmt.Errorf("%w: visual calls for image %d", ErrAttemptBudgetExceeded, imageIndex)
			}
			l.visionByImage[imageIndex] = remaining - 1
		}
		l.visionRemaining--
		b.held.auxiliaryCalls--
		b.held.totalCalls--
		b.held.costMicroUSD -= l.visionCost
		cost = l.visionCost
	case CallAnswer:
		if !l.answerRemaining {
			return nil, fmt.Errorf("%w: initial answer", ErrAttemptBudgetExceeded)
		}
		l.answerRemaining = false
		b.held.answerAttempts--
		b.held.totalCalls--
		b.held.costMicroUSD -= l.answerCost
		cost = l.answerCost
	default:
		return nil, fmt.Errorf("%w: unsupported lease call kind %q", ErrAttemptBudgetExceeded, kind)
	}
	b.applyCallLocked(kind, cost)
	return &CallTicket{
		Kind: kind, Model: l.model, Target: l.target,
		ImageIndex: imageIndex, RetryIndex: retryIndex,
		EstimatedMicroUSD: cost,
	}, nil
}

func reservationVisionCalls(reservation AttemptReservation) (int, map[int]int, bool) {
	if reservation.VisionCalls < 0 ||
		(reservation.VisionCalls > 0 && len(reservation.VisionCallsPerImage) > 0) {
		return 0, nil, false
	}
	if len(reservation.VisionCallsPerImage) == 0 {
		return reservation.VisionCalls, nil, true
	}
	total := 0
	byImage := make(map[int]int, len(reservation.VisionCallsPerImage))
	for imageIndex, calls := range reservation.VisionCallsPerImage {
		if calls < 0 {
			return 0, nil, false
		}
		var ok bool
		total, ok = addCount(total, calls)
		if !ok {
			return 0, nil, false
		}
		byImage[imageIndex] = calls
	}
	return total, byImage, true
}

func (b *AttemptBudget) canHoldLocked(requested budgetCapacity) error {
	if exceedsCount(b.used.AnswerAttempts, b.held.answerAttempts, requested.answerAttempts, b.limits.MaxAnswerAttempts) {
		return fmt.Errorf("%w: answer attempts", ErrAttemptBudgetExceeded)
	}
	if exceedsCount(b.used.AuxiliaryCalls, b.held.auxiliaryCalls, requested.auxiliaryCalls, b.limits.MaxAuxiliaryCalls) {
		return fmt.Errorf("%w: auxiliary calls", ErrAttemptBudgetExceeded)
	}
	if exceedsCount(b.used.TotalOutboundCalls, b.held.totalCalls, requested.totalCalls, b.limits.MaxTotalOutboundCalls) {
		return fmt.Errorf("%w: total outbound calls", ErrAttemptBudgetExceeded)
	}
	usedAndHeld := addCost(b.used.WorstCaseCostMicroUSD, b.held.costMicroUSD)
	if usedAndHeld == maxCost || requested.costMicroUSD == maxCost ||
		exceedsCostLimit(
			addCost(usedAndHeld, requested.costMicroUSD),
			b.limits.MaxWorstCaseCostMicroUSD,
		) {
		return fmt.Errorf("%w: worst-case cost", ErrAttemptBudgetExceeded)
	}
	return nil
}

func exceedsCount(used, held, requested, limit int) bool {
	combined, ok := addCount(used, held)
	if !ok {
		return true
	}
	combined, ok = addCount(combined, requested)
	return !ok || combined > limit
}

func addCount(left, right int) (int, bool) {
	if left < 0 || right < 0 || left > int(^uint(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}
