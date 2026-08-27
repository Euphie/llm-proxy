package agenttrajectory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Euphie/llm-proxy/internal/routing"
)

const (
	defaultIdleTimeout      = 30 * time.Minute
	defaultRetention        = 7 * 24 * time.Hour
	defaultMaxEvents        = 256
	defaultMaxTurns         = 64
	defaultMaxEventBytes    = 256 << 10
	defaultMaxPlaintextSize = 2 << 20
)

type Observation struct {
	ProfileID               int64
	ProfileSlug             string
	Protocol                routing.Operation
	SessionKey              *routing.SessionKey
	Strategy                string
	Route                   string
	TaskType                string
	Difficulty              string
	Risk                    string
	VisionMode              string
	Model                   string
	ModelPath               []string
	SelfEscalations         []SelfEscalation
	RequestBody             []byte
	ResponseBody            []byte
	StartedAt               time.Time
	LatencyMS               int64
	CandidateCostMicroUSD   int64
	FinalOutputCostMicroUSD int64
	FinalOutputLatencyMS    int64
	FinalOutputMetricsKnown bool
}

type CompletedTrajectory struct {
	Record    Record
	Plaintext Plaintext
	Eligible  bool
}

type Collector struct {
	store  *Store
	cipher *Cipher
	now    func() time.Time

	idleTimeout   time.Duration
	retention     time.Duration
	maxEvents     int
	maxTurns      int
	maxEventBytes int
	maxPlaintext  int

	sessionLocks [64]sync.Mutex
}

func NewCollector(store *Store, cipher *Cipher, now func() time.Time) *Collector {
	if now == nil {
		now = time.Now
	}
	return &Collector{
		store: store, cipher: cipher, now: now,
		idleTimeout: defaultIdleTimeout, retention: defaultRetention,
		maxEvents: defaultMaxEvents, maxTurns: defaultMaxTurns,
		maxEventBytes: defaultMaxEventBytes, maxPlaintext: defaultMaxPlaintextSize,
	}
}

func (c *Collector) Observe(
	ctx context.Context,
	observation Observation,
) (CompletedTrajectory, bool, error) {
	if err := c.validateObservation(observation); err != nil {
		return CompletedTrajectory{}, false, err
	}
	if observation.SessionKey != nil {
		lock := &c.sessionLocks[binary.BigEndian.Uint64(observation.SessionKey[:8])%uint64(len(c.sessionLocks))]
		lock.Lock()
		defer lock.Unlock()
	}
	digest := observationDigest(observation)
	var persistedDigest []byte
	if observation.SessionKey != nil {
		persistedDigest = digest[:]
		if completed, ok, err := c.persistedCompletion(ctx, observation, digest); ok || err != nil {
			return completed, ok, err
		}
	}
	turn, err := ParseTurn(observation.Protocol, observation.RequestBody, observation.ResponseBody)
	if err != nil {
		return CompletedTrajectory{}, false, err
	}
	if observation.SessionKey == nil && turn.Completion == CompletionWaitingForTool {
		return CompletedTrajectory{}, false, nil
	}

	now := c.now().UTC()
	startedAt := observation.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = now
	}
	incoming := append(latestTaskHistory(turn.History), turn.Response...)
	events := incoming
	modelPath := appendModels(nil, observation.ModelPath)
	modelPath = appendModel(modelPath, observation.Model)
	selfEscalations := append([]SelfEscalation(nil), observation.SelfEscalations...)
	turns := estimateSnapshotTurns(incoming)
	costMicroUSD := observation.CandidateCostMicroUSD
	finalOutputCostMicroUSD := observation.FinalOutputCostMicroUSD
	finalOutputLatencyMS := observation.FinalOutputLatencyMS
	finalOutputMetricsKnown := observation.FinalOutputMetricsKnown
	var existing Record
	var hasExisting bool
	if observation.SessionKey != nil {
		existing, hasExisting, err = c.store.FindCollectingBySession(ctx, observation.ProfileID, observation.SessionKey[:])
		if err != nil {
			return CompletedTrajectory{}, false, err
		}
		if hasExisting {
			stored, err := c.cipher.Open(existing.Payload)
			if err != nil {
				_ = c.store.MarkFailed(ctx, existing.ID, "decrypt_failed", 0)
				return CompletedTrajectory{}, false, err
			}
			var changed bool
			events, changed = mergeEventSnapshots(stored.Events, incoming)
			if !changed && turn.Completion == CompletionWaitingForTool {
				return CompletedTrajectory{}, false, nil
			}
			if changed {
				turns = existing.Turns + 1
			} else {
				turns = existing.Turns
			}
			if bytes.Equal(existing.ObservationDigest, digest[:]) {
				modelPath = append([]string(nil), existing.ModelPath...)
				selfEscalations = append([]SelfEscalation(nil), stored.SelfEscalations...)
				costMicroUSD = existing.CandidateCostMicroUSD
				finalOutputCostMicroUSD = stored.FinalOutputCostMicroUSD
				finalOutputLatencyMS = stored.FinalOutputLatencyMS
				finalOutputMetricsKnown = stored.FinalOutputMetricsKnown
			} else {
				modelPath = appendModels(existing.ModelPath, observation.ModelPath)
				modelPath = appendModel(modelPath, observation.Model)
				selfEscalations = appendSelfEscalations(stored.SelfEscalations, observation.SelfEscalations)
				costMicroUSD += existing.CandidateCostMicroUSD
			}
			startedAt = existing.StartedAt
		}
	}

	events, truncated := c.boundEvents(events)
	if turns > c.maxTurns {
		turns = c.maxTurns
		truncated = true
	}
	plaintext := Plaintext{
		Events: Redact(events), ModelPath: modelPath, SelfEscalations: selfEscalations,
		FinalOutputCostMicroUSD: finalOutputCostMicroUSD,
		FinalOutputLatencyMS:    finalOutputLatencyMS,
		FinalOutputMetricsKnown: finalOutputMetricsKnown,
	}
	if turn.Completion == CompletionComplete {
		plaintext.FinalText = redactText(turn.FinalText)
	}
	var plaintextTruncated bool
	plaintext, plaintextTruncated = c.boundPlaintext(plaintext)
	truncated = truncated || plaintextTruncated
	events = plaintext.Events
	payload, err := c.cipher.Seal(plaintext)
	if err != nil {
		return CompletedTrajectory{}, false, err
	}
	elapsedMS := observation.LatencyMS
	if !startedAt.IsZero() && now.After(startedAt) {
		elapsedMS = now.Sub(startedAt).Milliseconds()
	}
	update := ContentUpdate{
		ModelPath: modelPath, ToolCalls: countEventKind(events, EventToolCall), Turns: turns,
		ElapsedMS: elapsedMS, Truncated: truncated, CandidateCostMicroUSD: costMicroUSD,
		Payload: payload, ObservationDigest: persistedDigest,
	}

	record := existing
	if !hasExisting {
		source := SourceRequestSnapshot
		var sessionKey []byte
		if observation.SessionKey != nil {
			source = SourceSession
			sessionKey = observation.SessionKey[:]
		}
		record, err = c.store.Create(ctx, CreateInput{
			ProfileID: observation.ProfileID, ProfileSlug: observation.ProfileSlug,
			Protocol: observation.Protocol, Source: source, SessionKey: sessionKey,
			Strategy: observation.Strategy, Route: observation.Route, TaskType: observation.TaskType,
			Difficulty: observation.Difficulty, Risk: observation.Risk, VisionMode: observation.VisionMode,
			ModelPath: modelPath, ToolCalls: update.ToolCalls, Turns: turns, ElapsedMS: elapsedMS,
			Truncated: truncated, CandidateCostMicroUSD: costMicroUSD, Payload: payload,
			ObservationDigest: persistedDigest,
			StartedAt:         startedAt, ExpiresAt: now.Add(c.retention),
		})
		if errors.Is(err, ErrConflict) && observation.SessionKey != nil {
			if replayed, found, findErr := c.persistedCompletion(ctx, observation, digest); findErr != nil || found {
				return replayed, found, findErr
			}
		}
		if err != nil {
			return CompletedTrajectory{}, false, err
		}
	}

	switch turn.Completion {
	case CompletionWaitingForTool:
		if hasExisting {
			if err := c.store.UpdateCollecting(ctx, record.ID, update); err != nil {
				return CompletedTrajectory{}, false, err
			}
		}
		return CompletedTrajectory{}, false, nil
	case CompletionFailed:
		if hasExisting {
			if err := c.store.UpdateCollecting(ctx, record.ID, update); err != nil {
				return CompletedTrajectory{}, false, err
			}
		}
		if err := c.store.MarkFailed(ctx, record.ID, "protocol_failure", 0); err != nil {
			return CompletedTrajectory{}, false, err
		}
		return c.terminal(ctx, record.ID, plaintext, false)
	case CompletionComplete:
		completedAt := now
		update.CompletedAt = &completedAt
		if err := c.store.Complete(ctx, record.ID, update); err != nil {
			return CompletedTrajectory{}, false, err
		}
	default:
		return CompletedTrajectory{}, false, ErrInvalidProtocol
	}

	reason := eligibilityReason(observation.Risk, events, plaintext.FinalText, truncated)
	if reason != "" {
		if err := c.store.MarkSkipped(ctx, record.ID, reason); err != nil {
			return CompletedTrajectory{}, false, err
		}
		return c.terminal(ctx, record.ID, plaintext, false)
	}
	return c.terminal(ctx, record.ID, plaintext, true)
}

func (c *Collector) ExpireIdle(ctx context.Context) (int64, error) {
	if c == nil || c.store == nil {
		return 0, nil
	}
	return c.store.TimeoutIdle(ctx, c.now().UTC().Add(-c.idleTimeout))
}

func (c *Collector) terminal(
	ctx context.Context,
	id int64,
	plaintext Plaintext,
	eligible bool,
) (CompletedTrajectory, bool, error) {
	record, err := c.store.Get(ctx, id)
	if err != nil {
		return CompletedTrajectory{}, false, err
	}
	return CompletedTrajectory{Record: record, Plaintext: plaintext, Eligible: eligible}, true, nil
}

func (c *Collector) persistedCompletion(
	ctx context.Context,
	observation Observation,
	digest [sha256.Size]byte,
) (CompletedTrajectory, bool, error) {
	record, found, err := c.store.FindByObservationDigest(
		ctx, observation.ProfileID, observation.SessionKey[:], digest[:],
	)
	if err != nil || !found || record.Status == StatusCollecting {
		return CompletedTrajectory{}, false, err
	}
	plaintext, err := c.cipher.Open(record.Payload)
	if err != nil {
		return CompletedTrajectory{}, false, err
	}
	return CompletedTrajectory{Record: record, Plaintext: plaintext, Eligible: false}, true, nil
}

func (c *Collector) validateObservation(observation Observation) error {
	if c == nil || c.store == nil || c.cipher == nil || observation.ProfileID <= 0 ||
		strings.TrimSpace(observation.ProfileSlug) == "" || !supportedOperation(observation.Protocol) ||
		strings.TrimSpace(observation.Model) == "" || len(observation.RequestBody) == 0 ||
		len(observation.ResponseBody) == 0 || observation.LatencyMS < 0 || observation.CandidateCostMicroUSD < 0 ||
		observation.FinalOutputCostMicroUSD < 0 || observation.FinalOutputLatencyMS < 0 {
		return fmt.Errorf("%w: invalid trajectory observation", ErrInvalidProtocol)
	}
	return nil
}

func (c *Collector) boundEvents(events []Event) ([]Event, bool) {
	result := append([]Event(nil), events...)
	truncated := false
	for index := range result {
		encoded, _ := json.Marshal(result[index])
		if len(encoded) <= c.maxEventBytes {
			continue
		}
		result[index].Text = truncateString(result[index].Text, c.maxEventBytes/4)
		result[index].CallID = truncateString(result[index].CallID, c.maxEventBytes/8)
		result[index].ToolName = truncateString(result[index].ToolName, c.maxEventBytes/8)
		if len(result[index].Arguments) > c.maxEventBytes/4 {
			result[index].Arguments = json.RawMessage(`"[TRUNCATED]"`)
		}
		if len(result[index].Result) > c.maxEventBytes/4 {
			result[index].Result = json.RawMessage(`"[TRUNCATED]"`)
		}
		truncated = true
	}
	if len(result) > c.maxEvents {
		if c.maxEvents <= 1 {
			result = result[len(result)-1:]
		} else {
			result = append(append([]Event{}, result[:c.maxEvents-1]...), result[len(result)-1])
		}
		truncated = true
	}
	for len(result) > 1 {
		encoded, _ := json.Marshal(Plaintext{Events: result})
		if len(encoded) <= c.maxPlaintext {
			break
		}
		result = append(result[:len(result)-2], result[len(result)-1])
		truncated = true
	}
	return result, truncated
}

func (c *Collector) boundPlaintext(plaintext Plaintext) (Plaintext, bool) {
	if encodedPlaintextSize(plaintext) <= c.maxPlaintext {
		return plaintext, false
	}
	truncated := true
	if plaintext.FinalText != "" {
		original := plaintext.FinalText
		best := ""
		low, high := 0, len(original)
		for low <= high {
			middle := low + (high-low)/2
			candidate := strings.ToValidUTF8(original[:middle], "") + "[TRUNCATED]"
			probe := plaintext
			probe.FinalText = candidate
			if encodedPlaintextSize(probe) <= c.maxPlaintext {
				best = candidate
				low = middle + 1
			} else {
				high = middle - 1
			}
		}
		plaintext.FinalText = best
	}
	for len(plaintext.Events) > 0 && encodedPlaintextSize(plaintext) > c.maxPlaintext {
		plaintext.Events = append([]Event(nil), plaintext.Events[1:]...)
	}
	if encodedPlaintextSize(plaintext) > c.maxPlaintext {
		plaintext.FinalText = ""
	}
	return plaintext, truncated
}

func encodedPlaintextSize(plaintext Plaintext) int {
	encoded, err := json.Marshal(plaintext)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(encoded)
}

func mergeEventSnapshots(existing, incoming []Event) ([]Event, bool) {
	if len(incoming) == 0 {
		return append([]Event(nil), existing...), false
	}
	common := 0
	for common < len(existing) && common < len(incoming) && equalEvent(existing[common], incoming[common]) {
		common++
	}
	if common == len(incoming) {
		return append([]Event(nil), existing...), false
	}
	if common == len(existing) {
		return append(append([]Event(nil), existing...), incoming[common:]...), true
	}
	maxOverlap := min(len(existing), len(incoming))
	for overlap := maxOverlap; overlap > 0; overlap-- {
		matched := true
		for index := 0; index < overlap; index++ {
			if !equalEvent(existing[len(existing)-overlap+index], incoming[index]) {
				matched = false
				break
			}
		}
		if matched {
			return append(append([]Event(nil), existing...), incoming[overlap:]...), len(incoming) > overlap
		}
	}
	return append(append([]Event(nil), existing...), incoming...), true
}

func equalEvent(left, right Event) bool {
	return left.Role == right.Role && left.Kind == right.Kind && left.CallID == right.CallID &&
		left.ToolName == right.ToolName && left.Text == right.Text &&
		bytes.Equal(left.Arguments, right.Arguments) && bytes.Equal(left.Result, right.Result)
}

func appendModel(path []string, model string) []string {
	result := append([]string(nil), path...)
	if len(result) == 0 || result[len(result)-1] != model {
		return append(result, model)
	}
	return result
}

func appendModels(path, models []string) []string {
	result := append([]string(nil), path...)
	for _, model := range models {
		if strings.TrimSpace(model) != "" {
			result = appendModel(result, model)
		}
	}
	return result
}

func appendSelfEscalations(existing, incoming []SelfEscalation) []SelfEscalation {
	result := append([]SelfEscalation(nil), existing...)
	for _, escalation := range incoming {
		if escalation.FromModel == "" || escalation.ToModel == "" || escalation.FromModel == escalation.ToModel {
			continue
		}
		duplicate := false
		for _, current := range result {
			if current == escalation {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, escalation)
		}
	}
	return result
}

func estimateSnapshotTurns(events []Event) int {
	turns := 1
	previousResult := false
	for _, event := range events {
		isResult := event.Kind == EventToolResult
		if isResult && !previousResult {
			turns++
		}
		previousResult = isResult
	}
	return turns
}

func countEventKind(events []Event, kind EventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func eligibilityReason(risk string, events []Event, finalText string, truncated bool) string {
	if strings.EqualFold(risk, string(routing.RiskHigh)) {
		return "high_risk"
	}
	if truncated {
		return "trajectory_truncated"
	}
	if !hasTaskPrompt(events) {
		return "missing_task_prompt"
	}
	if utf8.RuneCountInString(strings.TrimSpace(finalText)) < 8 {
		return "insufficient_final_answer"
	}
	toolCalls, complete := completeToolEvidence(events)
	if toolCalls == 0 {
		return "no_tool_calls"
	}
	if !complete {
		return "incomplete_tool_evidence"
	}
	return ""
}

func latestTaskHistory(events []Event) []Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == EventUserMessage && strings.TrimSpace(events[index].Text) != "" {
			return append([]Event(nil), events[index:]...)
		}
	}
	return append([]Event(nil), events...)
}

func hasTaskPrompt(events []Event) bool {
	for _, event := range events {
		if event.Kind == EventUserMessage && strings.TrimSpace(event.Text) != "" {
			return true
		}
	}
	return false
}

func completeToolEvidence(events []Event) (int, bool) {
	pending := make(map[string]struct{})
	toolCalls := 0
	complete := true
	for _, event := range events {
		switch event.Kind {
		case EventToolCall:
			toolCalls++
			if strings.TrimSpace(event.CallID) == "" || strings.TrimSpace(event.ToolName) == "" {
				complete = false
				continue
			}
			pending[event.CallID] = struct{}{}
		case EventToolResult:
			if _, ok := pending[event.CallID]; !ok || !usefulToolResult(event.Result) {
				complete = false
				continue
			}
			delete(pending, event.CallID)
		}
	}
	return toolCalls, complete && len(pending) == 0
}

func usefulToolResult(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var value any
	if json.Unmarshal(trimmed, &value) != nil {
		return false
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		return text != "" && text != "[TRUNCATED]"
	}
	return true
}

func observationDigest(observation Observation) [sha256.Size]byte {
	hash := sha256.New()
	writeDigestPart(hash, []byte(observation.Protocol))
	writeDigestPart(hash, []byte(observation.Model))
	modelPath, _ := json.Marshal(observation.ModelPath)
	writeDigestPart(hash, modelPath)
	escalations, _ := json.Marshal(observation.SelfEscalations)
	writeDigestPart(hash, escalations)
	if observation.SessionKey != nil {
		writeDigestPart(hash, observation.SessionKey[:])
	}
	writeDigestPart(hash, observation.RequestBody)
	writeDigestPart(hash, observation.ResponseBody)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestPart(writer digestWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(value)
}

func truncateString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "") + "[TRUNCATED]"
}
