package routing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type CallGraphSnapshot struct {
	Attempts                   []PhysicalAttemptSnapshot
	AnswerCalls                int
	AuxiliaryCalls             int
	TotalOutboundCalls         int
	ModelSwitches              int
	TargetSwitches             int
	WorstCaseCostMicroUSD      int64
	ConsumedBeforePlanCalls    int
	ConsumedBeforePlanMicroUSD int64
	Deadline                   time.Time
}

type PhysicalAttemptSnapshot struct {
	Model                  string
	TargetID               string
	ModelIndex             int
	TargetIndex            int
	VisionCallsPerImage    []int
	AnswerCalls            int
	AnswerCallCostMicroUSD int64
	VisionCallCostMicroUSD int64
	ModelSwitch            bool
	TargetSwitch           bool
}

func (p *Planner) finalizePlan(
	request Request,
	plan ExecutionPlan,
	budget *AttemptBudgetSnapshot,
	planErr error,
) (ExecutionPlan, error) {
	if planErr != nil {
		return plan, planErr
	}
	state := AttemptBudgetSnapshot{}
	if budget != nil {
		state = *budget
	}
	selectedAttempts := plan.modelAttempts
	var graph CallGraphSnapshot
	var err error
	selectedIndex := 0
	for ; selectedIndex < len(plan.modelAttempts); selectedIndex++ {
		selectedAttempts = plan.modelAttempts[selectedIndex:]
		graph, err = p.freezeCallGraph(request, selectedAttempts, state)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrAttemptBudgetExceeded) {
			return ExecutionPlan{}, err
		}
	}
	if err != nil {
		return ExecutionPlan{}, err
	}
	if selectedIndex > 0 {
		selected := selectedAttempts[0]
		plan.model = selected.model
		plan.visionMode = selected.visionMode
		plan.usesStrongBaseline = selected.model == p.auto.StrongBaselineModel
		plan.answerCallCostMicroUSD = selected.answerCallCostMicroUSD
		plan.visionCallCostMicroUSD = selected.visionCallCostMicroUSD
		plan.estimatedCostMicroUSD = addCost(
			selected.answerCallCostMicroUSD,
			multiplyCost(selected.visionCallCostMicroUSD, request.Facts.ImageCount),
		)
		plan.reason = "first budget-compatible fallback"
	}
	plan.modelAttempts = trimModelAttemptsToGraph(selectedAttempts, graph)
	plan.callGraph = graph
	plan.estimatedCostMicroUSD = addCost(
		state.WorstCaseCostMicroUSD,
		plan.estimatedCostMicroUSD,
	)
	plan.worstCaseCostMicroUSD = addCost(
		state.WorstCaseCostMicroUSD,
		graph.WorstCaseCostMicroUSD,
	)
	return plan, nil
}

func trimModelAttemptsToGraph(
	attempts []ModelAttemptPlan,
	graph CallGraphSnapshot,
) []ModelAttemptPlan {
	trimmed := make([]ModelAttemptPlan, 0, len(attempts))
	for _, node := range graph.Attempts {
		for len(trimmed) <= node.ModelIndex {
			if len(trimmed) >= len(attempts) {
				return trimmed
			}
			attempt := attempts[len(trimmed)]
			attempt.targets = nil
			trimmed = append(trimmed, attempt)
		}
		if node.ModelIndex >= len(attempts) || node.TargetIndex >= len(attempts[node.ModelIndex].targets) {
			continue
		}
		trimmed[node.ModelIndex].targets = append(
			trimmed[node.ModelIndex].targets,
			attempts[node.ModelIndex].targets[node.TargetIndex],
		)
	}
	return trimmed
}

func (p *Planner) freezeCallGraph(
	request Request,
	attempts []ModelAttemptPlan,
	state AttemptBudgetSnapshot,
) (CallGraphSnapshot, error) {
	graph := CallGraphSnapshot{
		ConsumedBeforePlanCalls:    state.TotalOutboundCalls,
		ConsumedBeforePlanMicroUSD: state.WorstCaseCostMicroUSD,
		Deadline:                   state.Deadline,
	}
	remainingAnswers, answersOK := remainingCount(
		p.strategy.Budget.MaxAnswerAttempts, state.AnswerAttempts, state.HeldAnswerAttempts,
	)
	remainingAuxiliary, auxiliaryOK := remainingCount(
		p.strategy.Budget.MaxAuxiliaryCalls, state.AuxiliaryCalls, state.HeldAuxiliaryCalls,
	)
	remainingTotal, totalOK := remainingCount(
		p.strategy.Budget.MaxTotalOutboundCalls, state.TotalOutboundCalls, state.HeldOutboundCalls,
	)
	remainingCost, costOK := remainingCostCapacity(
		p.strategy.Budget.MaxWorstCaseCostMicroUSD,
		state.WorstCaseCostMicroUSD,
		state.HeldCostMicroUSD,
	)
	remainingModelSwitches, modelSwitchesOK := remainingCount(
		p.strategy.Budget.MaxModelSwitches, state.ModelSwitches, 0,
	)
	remainingTargetSwitches, targetSwitchesOK := remainingCount(
		p.strategy.Budget.MaxTargetSwitches, state.TargetSwitches, 0,
	)
	if !answersOK || !auxiliaryOK || !totalOK || !costOK || !modelSwitchesOK ||
		!targetSwitchesOK || remainingAnswers < 1 || remainingTotal < 1 {
		return CallGraphSnapshot{}, fmt.Errorf("%w: no remaining graph capacity", ErrAttemptBudgetExceeded)
	}

	limits := callGraphLimits{
		answers: remainingAnswers, auxiliary: remainingAuxiliary, total: remainingTotal,
		modelSwitches: remainingModelSwitches, targetSwitches: remainingTargetSwitches,
		cost: remainingCost,
	}
	initial := callGraphPath{retries: cloneRetryCounts(state.RetriesByTarget)}
	queue := []callGraphWork{{path: initial}}
	seen := make(map[string]struct{})
	reachable := make(map[[2]int]PhysicalAttemptSnapshot)
	processedStates := 0

	for len(queue) > 0 {
		work := queue[0]
		queue = queue[1:]
		if work.modelIndex >= len(attempts) {
			continue
		}
		attempt := attempts[work.modelIndex]
		if work.targetIndex >= len(attempt.targets) {
			continue
		}
		key := work.key()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		processedStates++
		if processedStates > maxCallGraphPathStates {
			return CallGraphSnapshot{}, fmt.Errorf("%w: frozen call graph state limit", ErrAttemptBudgetExceeded)
		}

		entered, ok := work.enter(limits)
		if !ok {
			queue = enqueueModelFallback(queue, work, attempts)
			continue
		}
		target := attempt.targets[work.targetIndex]
		node, visionCalls, ok := p.callGraphNode(request, attempt, work.modelIndex, work.targetIndex, target)
		if !ok || visionCalls > maxCallGraphPathStates {
			return CallGraphSnapshot{}, fmt.Errorf("%w: frozen call graph overflow", ErrAttemptBudgetExceeded)
		}
		heldVisionCalls := visionCalls
		if entered.visionCached {
			heldVisionCalls = 0
		}
		if !entered.canHoldAttempt(heldVisionCalls, node, limits) {
			if work.modelIndex == 0 && work.targetIndex == 0 && work.switchKind == callGraphSwitchNone {
				return CallGraphSnapshot{}, fmt.Errorf("%w: initial call graph node", ErrAttemptBudgetExceeded)
			}
			queue = enqueueModelFallback(queue, work, attempts)
			continue
		}

		nodeKey := [2]int{work.modelIndex, work.targetIndex}
		planned := reachable[nodeKey]
		if planned.Model == "" {
			planned = node
		}
		recordCallGraphPath(&graph, entered)

		if attempt.visionMode == VisionComposite && !entered.visionCached {
			for calls := 1; calls <= visionCalls; calls++ {
				failed, fits := entered.addCalls(CallVision, calls, node.VisionCallCostMicroUSD, limits)
				if !fits {
					break
				}
				recordCallGraphPath(&graph, failed)
				queue = enqueueNextCallGraphNode(queue, work, failed, attempts)
			}
		}

		firstSuccessCalls := 0
		lastSuccessCalls := 0
		if attempt.visionMode == VisionComposite && !entered.visionCached {
			firstSuccessCalls = request.Facts.ImageCount
			lastSuccessCalls = visionCalls
		}
		for calls := firstSuccessCalls; calls <= lastSuccessCalls; calls++ {
			succeededVision, fits := entered.addCalls(CallVision, calls, node.VisionCallCostMicroUSD, limits)
			if !fits {
				break
			}
			if attempt.visionMode == VisionComposite {
				succeededVision.visionCached = true
			}
			for _, configuredRetries := range p.configuredRetryCounts {
				answered, answerCalls, fits := addFrozenAnswers(
					succeededVision,
					target.id,
					node.AnswerCallCostMicroUSD,
					configuredRetries,
					p.strategy.Budget.MaxRetriesPerTarget,
					limits,
				)
				if !fits {
					continue
				}
				planned.AnswerCalls = max(planned.AnswerCalls, answerCalls)
				recordCallGraphPath(&graph, answered)
				queue = enqueueNextCallGraphNode(queue, work, answered, attempts)
			}
		}
		reachable[nodeKey] = planned
	}

	keys := make([][2]int, 0, len(reachable))
	for key := range reachable {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] == keys[j][0] {
			return keys[i][1] < keys[j][1]
		}
		return keys[i][0] < keys[j][0]
	})
	for _, key := range keys {
		graph.Attempts = append(graph.Attempts, reachable[key])
	}
	if len(graph.Attempts) == 0 {
		return CallGraphSnapshot{}, fmt.Errorf("%w: frozen call graph", ErrAttemptBudgetExceeded)
	}
	return graph, nil
}

const maxCallGraphPathStates = 100_000

type callGraphSwitch uint8

const (
	callGraphSwitchNone callGraphSwitch = iota
	callGraphSwitchTarget
	callGraphSwitchModel
)

type callGraphLimits struct {
	answers, auxiliary, total     int
	modelSwitches, targetSwitches int
	cost                          int64
}

type callGraphPath struct {
	answers, auxiliary, total     int
	modelSwitches, targetSwitches int
	cost                          int64
	visionCached                  bool
	retries                       map[string]int
}

type callGraphWork struct {
	modelIndex, targetIndex int
	switchKind              callGraphSwitch
	path                    callGraphPath
}

func (w callGraphWork) key() string {
	keys := make([]string, 0, len(w.path.retries))
	for target := range w.path.retries {
		keys = append(keys, target)
	}
	sort.Strings(keys)
	var retries strings.Builder
	for _, target := range keys {
		fmt.Fprintf(&retries, "%s=%d;", target, w.path.retries[target])
	}
	return fmt.Sprintf(
		"%d/%d/%d/%d/%d/%d/%d/%d/%t/%s",
		w.modelIndex, w.targetIndex, w.switchKind,
		w.path.answers, w.path.auxiliary, w.path.total,
		w.path.modelSwitches, w.path.targetSwitches,
		w.path.visionCached, retries.String(),
	) + fmt.Sprintf("/%d", w.path.cost)
}

func (w callGraphWork) enter(limits callGraphLimits) (callGraphPath, bool) {
	path := w.path.clone()
	switch w.switchKind {
	case callGraphSwitchNone:
	case callGraphSwitchTarget:
		if path.targetSwitches >= limits.targetSwitches {
			return callGraphPath{}, false
		}
		path.targetSwitches++
	case callGraphSwitchModel:
		if path.modelSwitches >= limits.modelSwitches {
			return callGraphPath{}, false
		}
		path.modelSwitches++
	default:
		return callGraphPath{}, false
	}
	return path, true
}

func (p callGraphPath) clone() callGraphPath {
	p.retries = cloneRetryCounts(p.retries)
	return p
}

func cloneRetryCounts(source map[string]int) map[string]int {
	cloned := make(map[string]int, len(source))
	for target, retries := range source {
		cloned[target] = retries
	}
	return cloned
}

func (p callGraphPath) canHoldAttempt(
	visionCalls int,
	node PhysicalAttemptSnapshot,
	limits callGraphLimits,
) bool {
	held, ok := p.addCalls(CallVision, visionCalls, node.VisionCallCostMicroUSD, limits)
	if !ok {
		return false
	}
	_, ok = held.addCalls(CallAnswer, 1, node.AnswerCallCostMicroUSD, limits)
	return ok
}

func (p callGraphPath) addCalls(
	kind CallKind,
	calls int,
	perCallCost int64,
	limits callGraphLimits,
) (callGraphPath, bool) {
	if calls < 0 || perCallCost < 0 {
		return callGraphPath{}, false
	}
	next := p.clone()
	var ok bool
	switch kind {
	case CallVision:
		next.auxiliary, ok = addCount(next.auxiliary, calls)
		if !ok || next.auxiliary > limits.auxiliary {
			return callGraphPath{}, false
		}
	case CallAnswer:
		next.answers, ok = addCount(next.answers, calls)
		if !ok || next.answers > limits.answers {
			return callGraphPath{}, false
		}
	default:
		return callGraphPath{}, false
	}
	next.total, ok = addCount(next.total, calls)
	if !ok || next.total > limits.total {
		return callGraphPath{}, false
	}
	cost := multiplyCost(perCallCost, calls)
	next.cost = addCost(next.cost, cost)
	if cost == maxCost || next.cost == maxCost || next.cost > limits.cost {
		return callGraphPath{}, false
	}
	return next, true
}

func addFrozenAnswers(
	path callGraphPath,
	target string,
	answerCost int64,
	configuredRetries int,
	maxRetriesPerTarget int,
	limits callGraphLimits,
) (callGraphPath, int, bool) {
	answered, ok := path.addCalls(CallAnswer, 1, answerCost, limits)
	if !ok {
		return callGraphPath{}, 0, false
	}
	answerCalls := 1
	for answerCalls-1 < configuredRetries && answered.retries[target] < maxRetriesPerTarget {
		retried, fits := answered.addCalls(CallAnswer, 1, answerCost, limits)
		if !fits {
			break
		}
		retried.retries[target]++
		answered = retried
		answerCalls++
	}
	return answered, answerCalls, true
}

func (p *Planner) callGraphNode(
	request Request,
	attempt ModelAttemptPlan,
	modelIndex int,
	targetIndex int,
	target TargetPlan,
) (PhysicalAttemptSnapshot, int, bool) {
	node := PhysicalAttemptSnapshot{
		Model: attempt.model, TargetID: target.id,
		ModelIndex: modelIndex, TargetIndex: targetIndex,
		AnswerCallCostMicroUSD: attempt.answerCallCostMicroUSD,
		VisionCallCostMicroUSD: attempt.visionCallCostMicroUSD,
		ModelSwitch:            modelIndex > 0 && targetIndex == 0,
		TargetSwitch:           targetIndex > 0,
	}
	if attempt.visionMode != VisionComposite {
		return node, 0, true
	}
	perImageCalls, ok := addCount(1, p.maxConfiguredRetries)
	if !ok {
		return PhysicalAttemptSnapshot{}, 0, false
	}
	visionCalls, ok := multiplyCount(request.Facts.ImageCount, perImageCalls)
	if !ok {
		return PhysicalAttemptSnapshot{}, 0, false
	}
	node.VisionCallsPerImage = make([]int, request.Facts.ImageCount)
	for imageIndex := range node.VisionCallsPerImage {
		node.VisionCallsPerImage[imageIndex] = perImageCalls
	}
	return node, visionCalls, true
}

func recordCallGraphPath(graph *CallGraphSnapshot, path callGraphPath) {
	graph.AnswerCalls = max(graph.AnswerCalls, path.answers)
	graph.AuxiliaryCalls = max(graph.AuxiliaryCalls, path.auxiliary)
	graph.TotalOutboundCalls = max(graph.TotalOutboundCalls, path.total)
	graph.ModelSwitches = max(graph.ModelSwitches, path.modelSwitches)
	graph.TargetSwitches = max(graph.TargetSwitches, path.targetSwitches)
	graph.WorstCaseCostMicroUSD = max(graph.WorstCaseCostMicroUSD, path.cost)
}

func enqueueNextCallGraphNode(
	queue []callGraphWork,
	current callGraphWork,
	path callGraphPath,
	attempts []ModelAttemptPlan,
) []callGraphWork {
	if current.targetIndex+1 < len(attempts[current.modelIndex].targets) {
		return append(queue, callGraphWork{
			modelIndex: current.modelIndex, targetIndex: current.targetIndex + 1,
			switchKind: callGraphSwitchTarget, path: path,
		})
	}
	if current.modelIndex+1 < len(attempts) {
		return append(queue, callGraphWork{
			modelIndex: current.modelIndex + 1,
			switchKind: callGraphSwitchModel, path: path,
		})
	}
	return queue
}

func enqueueModelFallback(
	queue []callGraphWork,
	current callGraphWork,
	attempts []ModelAttemptPlan,
) []callGraphWork {
	if current.modelIndex+1 >= len(attempts) {
		return queue
	}
	return append(queue, callGraphWork{
		modelIndex: current.modelIndex + 1,
		switchKind: callGraphSwitchModel,
		path:       current.path,
	})
}

func remainingCount(limit, consumed, held int) (int, bool) {
	if limit < 0 || consumed < 0 || held < 0 || consumed > limit || held > limit-consumed {
		return 0, false
	}
	return limit - consumed - held, true
}

func remainingCostCapacity(limit, consumed, held int64) (int64, bool) {
	if limit < 0 || consumed < 0 || held < 0 || consumed > limit || held > limit-consumed {
		return 0, false
	}
	return limit - consumed - held, true
}

func multiplyCount(value, count int) (int, bool) {
	if value < 0 || count < 0 || value != 0 && count > int(^uint(0)>>1)/value {
		return 0, false
	}
	return value * count, true
}

func (p ExecutionPlan) CallGraph() CallGraphSnapshot {
	graph := p.callGraph
	graph.Attempts = append([]PhysicalAttemptSnapshot(nil), p.callGraph.Attempts...)
	for index := range graph.Attempts {
		graph.Attempts[index].VisionCallsPerImage = append(
			[]int(nil),
			p.callGraph.Attempts[index].VisionCallsPerImage...,
		)
	}
	return graph
}

func (p PhysicalAttemptSnapshot) Reservation() AttemptReservation {
	return AttemptReservation{
		Model: p.Model, Target: p.TargetID,
		VisionCallsPerImage:    append([]int(nil), p.VisionCallsPerImage...),
		VisionCallCostMicroUSD: p.VisionCallCostMicroUSD,
		AnswerCalls:            p.AnswerCalls,
		AnswerCallCostMicroUSD: p.AnswerCallCostMicroUSD,
		ModelSwitch:            p.ModelSwitch,
		TargetSwitch:           p.TargetSwitch,
	}
}
