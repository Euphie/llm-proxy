// Package stats provides async token usage recording backed by SQLite.
package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const usageTimeFormat = "2006-01-02T15:04:05.000000000Z"

// DB wraps a SQLite database for async usage recording.
type DB struct {
	db             *sql.DB
	afterWrite     func()
	launchWorker   func(func())
	closingStarted chan struct{}
	beginQueryTx   func(context.Context) (queryTx, error)

	mu        sync.Mutex
	closing   bool
	inFlight  sync.WaitGroup
	closeOnce sync.Once
}

type RequestMeta struct {
	ProfileID   int64
	ProfileSlug string
	Protocol    string
	Kind        string
	Path        string
}

type RoutingTrace struct {
	CreatedAt                     time.Time
	CorrelationID                 string
	ProfileID                     int64
	ProfileSlug                   string
	Protocol                      string
	Path                          string
	Strategy                      string
	Route                         string
	TaskType                      string
	Difficulty                    string
	Risk                          string
	ClassificationSource          string
	ClassificationConfidenceBPS   int
	ClassificationReasonCodes     []string
	EstimatedInputTokens          int
	RequestedOutputTokens         int
	DecisionReason                string
	InitialModel                  string
	FinalModel                    string
	InitialTarget                 string
	FinalTarget                   string
	VisionMode                    string
	StatusCode                    int
	ClientCommitted               bool
	AnswerAttempts                int
	AuxiliaryCalls                int
	TotalOutboundCalls            int
	ModelSwitches                 int
	SelfEscalations               int
	SelfEscalationReason          string
	TargetSwitches                int
	PlannedWorstCaseCostMicroUSD  int64
	ConsumedEstimatedCostMicroUSD int64
	HeldCostMicroUSD              int64
	KnownActualCostMicroUSD       int64
	AllActualCostsKnown           bool
	ElapsedMilliseconds           int64
}

type CandidateDecision struct {
	Model                   string
	Decision                string
	ReasonCode              string
	QualityScoreBPS         int
	SevereErrorRateBPS      int
	ExpectedCostMicroUSD    int64
	AnswerWorstCostMicroUSD int64
	VisionCallCostMicroUSD  int64
	VisionMode              string
	UpstreamNodes           []string
}

type PhysicalCall struct {
	Sequence           int
	Kind               string
	Model              string
	Target             string
	ImageIndex         int
	RetryIndex         int
	ModelSwitchIndex   int
	TargetSwitchIndex  int
	EstimatedMicroUSD  int64
	ActualCostKnown    bool
	ActualMicroUSD     int64
	UsagePresent       bool
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	InputIncludesCache bool
	StatusCode         int
	Outcome            string
}

func New(db *sql.DB) *DB {
	return &DB{
		db:             db,
		launchWorker:   func(worker func()) { go worker() },
		closingStarted: make(chan struct{}),
		beginQueryTx: func(ctx context.Context) (queryTx, error) {
			return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		},
	}
}

// Close waits for pending writes. The shared database remains owned by the application.
func (s *DB) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		close(s.closingStarted)
		s.mu.Unlock()

		s.inFlight.Wait()
	})
	return nil
}

// RecordAsync uses p to parse token usage from the captured response body,
// then writes the record to the database asynchronously.
// Errors are logged but never returned to the caller.
func (s *DB) RecordAsync(meta RequestMeta, data []byte, p Parser) {
	s.recordAsync(func() {
		u, ok := p.Parse(data)
		if !ok {
			return
		}
		_, err := s.db.Exec(
			`INSERT INTO usage (
				created_at, profile_id, profile_slug, protocol, request_kind,
				model, path, input_tokens, output_tokens,
				cache_read_tokens, cache_creation_tokens
			) VALUES (
				?, (SELECT id FROM profiles WHERE id = ?), ?, ?, ?,
				?, ?, ?, ?, ?, ?
			)`,
			time.Now().UTC().Format(usageTimeFormat),
			meta.ProfileID,
			meta.ProfileSlug,
			meta.Protocol,
			meta.Kind,
			strings.ToLower(u.Model),
			meta.Path,
			u.InputTokens,
			u.OutputTokens,
			u.CacheReadTokens,
			u.CacheCreationTokens,
		)
		if err != nil {
			slog.Warn("stats: write failed", "err", err)
		}
	})
}

func (s *DB) RecordRoutingTraceAsync(trace RoutingTrace) {
	s.RecordRoutingTraceWithCallsAsync(trace, nil)
}

func (s *DB) RecordRoutingTraceWithCallsAsync(trace RoutingTrace, calls []PhysicalCall) {
	s.RecordRoutingTraceWithCallsAndCandidatesAsync(trace, calls, nil)
}

func (s *DB) RecordRoutingTraceWithCallsAndCandidatesAsync(
	trace RoutingTrace,
	calls []PhysicalCall,
	candidates []CandidateDecision,
) {
	calls = append([]PhysicalCall(nil), calls...)
	candidates = cloneCandidateDecisions(candidates)
	createdAt := trace.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	createdAtText := createdAt.Format(usageTimeFormat)
	s.recordAsync(func() {
		if (len(calls) > 0 || len(candidates) > 0) && strings.TrimSpace(trace.CorrelationID) == "" {
			slog.Warn("routing trace: child correlation is empty")
			return
		}
		for index, call := range calls {
			if call.Sequence != index+1 {
				slog.Warn("routing trace: call sequence is not contiguous", "sequence", call.Sequence)
				return
			}
		}
		if !validCandidateDecisions(candidates) {
			slog.Warn("routing trace: candidate decision is invalid")
			return
		}
		reasonCodes, ok := controlledStringListJSON(trace.ClassificationReasonCodes)
		if !ok {
			slog.Warn("routing trace: classification reason codes are invalid")
			return
		}
		tx, err := s.db.Begin()
		if err != nil {
			slog.Warn("routing trace: begin failed", "err", err)
			return
		}
		defer tx.Rollback()
		result, err := tx.Exec(
			`INSERT INTO routing_traces (
				created_at, correlation_id, profile_id, profile_slug, protocol, path,
				strategy_name, route_id, task_type, difficulty, risk, classification_source,
				classification_confidence_bps, classification_reason_codes,
				estimated_input_tokens, requested_output_tokens, decision_reason,
				initial_model, final_model, initial_target, final_target,
				vision_mode, status_code,
				client_committed, answer_attempts, auxiliary_calls,
				total_outbound_calls, model_switches, target_switches,
				self_escalations, self_escalation_reason,
				planned_worst_case_cost_micro_usd, consumed_estimated_cost_micro_usd,
				held_cost_micro_usd, known_actual_cost_micro_usd,
				all_actual_costs_known, elapsed_ms
			) VALUES (
				?, ?, (SELECT id FROM profiles WHERE id = ?), ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?,
				?, ?, ?,
				?, ?, ?,
				?, ?,
				?, ?,
				?, ?,
				?, ?
			)`,
			createdAtText,
			trace.CorrelationID,
			trace.ProfileID,
			trace.ProfileSlug,
			trace.Protocol,
			trace.Path,
			trace.Strategy,
			trace.Route,
			trace.TaskType,
			traceDifficulty(trace.Difficulty),
			trace.Risk,
			trace.ClassificationSource,
			trace.ClassificationConfidenceBPS,
			reasonCodes,
			trace.EstimatedInputTokens,
			trace.RequestedOutputTokens,
			trace.DecisionReason,
			trace.InitialModel,
			trace.FinalModel,
			trace.InitialTarget,
			trace.FinalTarget,
			trace.VisionMode,
			trace.StatusCode,
			boolInt(trace.ClientCommitted),
			trace.AnswerAttempts,
			trace.AuxiliaryCalls,
			trace.TotalOutboundCalls,
			trace.ModelSwitches,
			trace.TargetSwitches,
			trace.SelfEscalations,
			trace.SelfEscalationReason,
			trace.PlannedWorstCaseCostMicroUSD,
			trace.ConsumedEstimatedCostMicroUSD,
			trace.HeldCostMicroUSD,
			trace.KnownActualCostMicroUSD,
			boolInt(trace.AllActualCostsKnown),
			trace.ElapsedMilliseconds,
		)
		if err != nil {
			slog.Warn("routing trace: write failed", "err", err)
			return
		}
		traceID, err := result.LastInsertId()
		if err != nil {
			slog.Warn("routing trace: id failed", "err", err)
			return
		}
		for _, call := range calls {
			_, err = tx.Exec(`INSERT INTO routing_calls (
				created_at, trace_id, correlation_id, sequence, kind,
				logical_model, target, image_index, retry_index,
				model_switch_index, target_switch_index,
				estimated_cost_micro_usd, actual_cost_known,
				actual_cost_micro_usd, usage_present, input_tokens, output_tokens,
				cache_read_tokens, cache_write_tokens, input_includes_cache,
				status_code, outcome
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				createdAtText, traceID, trace.CorrelationID, call.Sequence, call.Kind,
				call.Model, call.Target, call.ImageIndex, call.RetryIndex,
				call.ModelSwitchIndex, call.TargetSwitchIndex,
				call.EstimatedMicroUSD, boolInt(call.ActualCostKnown),
				call.ActualMicroUSD, boolInt(call.UsagePresent), call.InputTokens,
				call.OutputTokens, call.CacheReadTokens, call.CacheWriteTokens,
				boolInt(call.InputIncludesCache), call.StatusCode, call.Outcome,
			)
			if err != nil {
				slog.Warn("routing call: write failed", "err", err)
				return
			}
		}
		for index, candidate := range candidates {
			upstreamNodes, _ := controlledStringListJSON(candidate.UpstreamNodes)
			_, err = tx.Exec(`INSERT INTO routing_candidate_decisions (
				trace_id, ordinal, model, decision, reason_code,
				quality_score_bps, severe_error_rate_bps,
				expected_cost_micro_usd, answer_worst_cost_micro_usd,
				vision_call_cost_micro_usd, vision_mode, upstream_nodes
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				traceID, index+1, candidate.Model, candidate.Decision, candidate.ReasonCode,
				candidate.QualityScoreBPS, candidate.SevereErrorRateBPS,
				candidate.ExpectedCostMicroUSD, candidate.AnswerWorstCostMicroUSD,
				candidate.VisionCallCostMicroUSD, candidate.VisionMode, upstreamNodes,
			)
			if err != nil {
				slog.Warn("routing candidate: write failed", "err", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Warn("routing trace: commit failed", "err", err)
		}
	})
}

func cloneCandidateDecisions(source []CandidateDecision) []CandidateDecision {
	cloned := append([]CandidateDecision(nil), source...)
	for index := range cloned {
		cloned[index].UpstreamNodes = append([]string(nil), cloned[index].UpstreamNodes...)
	}
	return cloned
}

func validCandidateDecisions(candidates []CandidateDecision) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Model) == "" || strings.TrimSpace(candidate.ReasonCode) == "" ||
			(candidate.Decision != "selected" && candidate.Decision != "eligible" && candidate.Decision != "rejected") ||
			candidate.QualityScoreBPS < 0 || candidate.QualityScoreBPS > 10_000 ||
			candidate.SevereErrorRateBPS < 0 || candidate.SevereErrorRateBPS > 10_000 ||
			candidate.ExpectedCostMicroUSD < 0 || candidate.AnswerWorstCostMicroUSD < 0 ||
			candidate.VisionCallCostMicroUSD < 0 {
			return false
		}
		if _, ok := controlledStringListJSON(candidate.UpstreamNodes); !ok {
			return false
		}
	}
	return true
}

func controlledStringListJSON(values []string) (string, bool) {
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || len(value) > 128 {
			return "", false
		}
	}
	body, err := json.Marshal(values)
	return string(body), err == nil
}

func traceDifficulty(value string) string {
	switch value {
	case "easy", "medium", "hard", "unknown":
		return value
	default:
		return "unknown"
	}
}

func (s *DB) recordAsync(write func()) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.inFlight.Add(1)
	s.mu.Unlock()

	s.launchWorker(func() {
		defer s.inFlight.Done()
		if s.afterWrite != nil {
			defer s.afterWrite()
		}
		write()
	})
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
