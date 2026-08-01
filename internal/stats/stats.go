// Package stats provides async token usage recording backed by SQLite.
package stats

import (
	"context"
	"database/sql"
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
	ProfileID                    int64
	ProfileSlug                  string
	Protocol                     string
	Path                         string
	Strategy                     string
	Route                        string
	TaskType                     string
	Risk                         string
	ClassificationSource         string
	InitialModel                 string
	FinalModel                   string
	VisionMode                   string
	StatusCode                   int
	ClientCommitted              bool
	AnswerAttempts               int
	AuxiliaryCalls               int
	TotalOutboundCalls           int
	ModelSwitches                int
	TargetSwitches               int
	PlannedWorstCaseCostMicroUSD int64
	ReservedCostMicroUSD         int64
	ElapsedMilliseconds          int64
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
	s.recordAsync(func() {
		_, err := s.db.Exec(
			`INSERT INTO routing_traces (
				created_at, profile_id, profile_slug, protocol, path,
				strategy_name, route_id, task_type, risk, classification_source,
				initial_model, final_model, vision_mode, status_code,
				client_committed, answer_attempts, auxiliary_calls,
				total_outbound_calls, model_switches, target_switches,
				planned_worst_case_cost_micro_usd, reserved_cost_micro_usd,
				elapsed_ms
			) VALUES (
				?, (SELECT id FROM profiles WHERE id = ?), ?, ?, ?,
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)`,
			time.Now().UTC().Format(usageTimeFormat),
			trace.ProfileID,
			trace.ProfileSlug,
			trace.Protocol,
			trace.Path,
			trace.Strategy,
			trace.Route,
			trace.TaskType,
			trace.Risk,
			trace.ClassificationSource,
			trace.InitialModel,
			trace.FinalModel,
			trace.VisionMode,
			trace.StatusCode,
			boolInt(trace.ClientCommitted),
			trace.AnswerAttempts,
			trace.AuxiliaryCalls,
			trace.TotalOutboundCalls,
			trace.ModelSwitches,
			trace.TargetSwitches,
			trace.PlannedWorstCaseCostMicroUSD,
			trace.ReservedCostMicroUSD,
			trace.ElapsedMilliseconds,
		)
		if err != nil {
			slog.Warn("routing trace: write failed", "err", err)
		}
	})
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
