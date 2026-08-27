package evaluation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const dayFormat = "2006-01-02"

var (
	ErrBudgetExceeded    = errors.New("routing evaluation daily budget exceeded")
	ErrReservationClosed = errors.New("routing evaluation budget reservation is closed")
	ErrInvalidEvidence   = errors.New("invalid routing quality evidence")
)

type Outcome string

const (
	OutcomeCandidateWin Outcome = "candidate_win"
	OutcomeTie          Outcome = "tie"
	OutcomeReferenceWin Outcome = "reference_win"
)

type BudgetSnapshot struct {
	Day              string `json:"day"`
	ReservedMicroUSD int64  `json:"reserved_micro_usd"`
	SpentMicroUSD    int64  `json:"spent_micro_usd"`
}

type Evidence struct {
	ProfileID                 int64
	Strategy                  string
	Route                     string
	TaskType                  string
	Difficulty                string
	Risk                      string
	VisionMode                string
	CandidateModel            string
	ReferenceModel            string
	ReviewerModel             string
	Outcome                   Outcome
	Dimensions                map[Dimension]Outcome
	SevereError               bool
	DeterministicFailure      bool
	CandidateCostMicroUSD     int64
	ReferenceCostMicroUSD     int64
	ReviewerCostMicroUSD      int64
	CandidateLatencyMS        int64
	ReferenceLatencyMS        int64
	SelfEscalationEligible    bool
	SelfEscalationRequested   bool
	SelfEscalationSupported   bool
	SelfEscalationUnnecessary bool
	SelfEscalationMissed      bool
}

type EvidenceAggregate struct {
	Day                           string                           `json:"day"`
	Strategy                      string                           `json:"strategy"`
	Route                         string                           `json:"route"`
	TaskType                      string                           `json:"task_type"`
	Difficulty                    string                           `json:"difficulty"`
	Risk                          string                           `json:"risk"`
	VisionMode                    string                           `json:"vision_mode"`
	CandidateModel                string                           `json:"candidate_model"`
	ReferenceModel                string                           `json:"reference_model"`
	ReviewerModel                 string                           `json:"reviewer_model"`
	Samples                       int64                            `json:"samples"`
	CandidateWins                 int64                            `json:"candidate_wins"`
	Ties                          int64                            `json:"ties"`
	ReferenceWins                 int64                            `json:"reference_wins"`
	SevereErrors                  int64                            `json:"severe_errors"`
	DeterministicFailures         int64                            `json:"deterministic_failures"`
	CandidateCostMicroUSD         int64                            `json:"candidate_cost_micro_usd"`
	ReferenceCostMicroUSD         int64                            `json:"reference_cost_micro_usd"`
	ReviewerCostMicroUSD          int64                            `json:"reviewer_cost_micro_usd"`
	CandidateLatencyMS            int64                            `json:"candidate_latency_ms"`
	ReferenceLatencyMS            int64                            `json:"reference_latency_ms"`
	SelfEscalationEligibleSamples int64                            `json:"self_escalation_eligible_samples"`
	SelfEscalations               int64                            `json:"self_escalations"`
	SupportedSelfEscalations      int64                            `json:"supported_self_escalations"`
	UnnecessarySelfEscalations    int64                            `json:"unnecessary_self_escalations"`
	MissedSelfEscalations         int64                            `json:"missed_self_escalations"`
	Dimensions                    map[Dimension]DimensionAggregate `json:"dimensions,omitempty"`
}

type DimensionAggregate struct {
	Dimension     Dimension `json:"dimension"`
	Samples       int64     `json:"samples"`
	CandidateWins int64     `json:"candidate_wins"`
	Ties          int64     `json:"ties"`
	ReferenceWins int64     `json:"reference_wins"`
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

type Reservation struct {
	store     *Store
	profileID int64
	day       string
	amount    int64

	mu     sync.Mutex
	closed bool
}

func (s *Store) ReserveBudget(
	ctx context.Context,
	profileID int64,
	limitMicroUSD int64,
	amountMicroUSD int64,
) (*Reservation, error) {
	if profileID <= 0 || limitMicroUSD <= 0 || amountMicroUSD <= 0 || amountMicroUSD > limitMicroUSD {
		return nil, ErrBudgetExceeded
	}
	day := s.now().UTC().Format(dayFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin evaluation budget reservation: %w", err)
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO routing_evaluation_budgets (
			profile_id, budget_day, reserved_micro_usd, spent_micro_usd, updated_at
		) VALUES (?, ?, 0, 0, ?)
		ON CONFLICT(profile_id, budget_day) DO NOTHING
	`, profileID, day, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("create evaluation budget: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE routing_evaluation_budgets
		SET reserved_micro_usd = reserved_micro_usd + ?, updated_at = ?
		WHERE profile_id = ? AND budget_day = ?
		  AND spent_micro_usd + reserved_micro_usd + ? <= ?
	`, amountMicroUSD, s.now().UTC().Format(time.RFC3339Nano), profileID, day, amountMicroUSD, limitMicroUSD)
	if err != nil {
		return nil, fmt.Errorf("reserve evaluation budget: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("inspect evaluation budget reservation: %w", err)
	}
	if updated != 1 {
		return nil, ErrBudgetExceeded
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit evaluation budget reservation: %w", err)
	}
	return &Reservation{store: s, profileID: profileID, day: day, amount: amountMicroUSD}, nil
}

func (r *Reservation) Commit(ctx context.Context, actualMicroUSD int64) error {
	if actualMicroUSD < 0 || actualMicroUSD > r.amount {
		return ErrBudgetExceeded
	}
	return r.finish(ctx, actualMicroUSD)
}

func (r *Reservation) Release(ctx context.Context) error {
	return r.finish(ctx, 0)
}

func (r *Reservation) finish(ctx context.Context, spentMicroUSD int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrReservationClosed
	}
	result, err := r.store.db.ExecContext(ctx, `
		UPDATE routing_evaluation_budgets
		SET reserved_micro_usd = reserved_micro_usd - ?,
			spent_micro_usd = spent_micro_usd + ?,
			updated_at = ?
		WHERE profile_id = ? AND budget_day = ? AND reserved_micro_usd >= ?
	`, r.amount, spentMicroUSD, r.store.now().UTC().Format(time.RFC3339Nano),
		r.profileID, r.day, r.amount)
	if err != nil {
		return fmt.Errorf("finish evaluation budget reservation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect finished evaluation budget reservation: %w", err)
	}
	if updated != 1 {
		return ErrReservationClosed
	}
	r.closed = true
	return nil
}

func (s *Store) Budget(
	ctx context.Context,
	profileID int64,
	day time.Time,
) (BudgetSnapshot, error) {
	snapshot := BudgetSnapshot{Day: day.UTC().Format(dayFormat)}
	err := s.db.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, spent_micro_usd
		FROM routing_evaluation_budgets
		WHERE profile_id = ? AND budget_day = ?
	`, profileID, snapshot.Day).Scan(&snapshot.ReservedMicroUSD, &snapshot.SpentMicroUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return BudgetSnapshot{}, fmt.Errorf("read evaluation budget: %w", err)
	}
	return snapshot, nil
}

func (s *Store) ResetReservations(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE routing_evaluation_budgets
		SET reserved_micro_usd = 0, updated_at = ?
		WHERE reserved_micro_usd != 0
	`, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("reset evaluation budget reservations: %w", err)
	}
	return nil
}

func (s *Store) RecordEvidence(ctx context.Context, evidence Evidence) error {
	if err := validateEvidence(evidence); err != nil {
		return err
	}
	candidateWin := boolInt(evidence.Outcome == OutcomeCandidateWin)
	tie := boolInt(evidence.Outcome == OutcomeTie)
	referenceWin := boolInt(evidence.Outcome == OutcomeReferenceWin)
	day := s.now().UTC().Format(dayFormat)
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routing quality evidence: %w", err)
	}
	defer rollback(tx)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO routing_quality_evidence_v2 (
			profile_id, evidence_day, strategy_name, route_id, task_type,
			difficulty, risk, vision_mode,
			candidate_model, reference_model, reviewer_model,
			samples, candidate_wins, ties, reference_wins,
			severe_errors, deterministic_failures,
			self_escalation_eligible_samples, self_escalations,
			supported_self_escalations, unnecessary_self_escalations,
			missed_self_escalations,
			candidate_cost_micro_usd, reference_cost_micro_usd, reviewer_cost_micro_usd,
			candidate_latency_ms, reference_latency_ms, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			1, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?
		)
		ON CONFLICT(
			profile_id, evidence_day, strategy_name, route_id, task_type,
			difficulty, risk, vision_mode,
			candidate_model, reference_model, reviewer_model
		) DO UPDATE SET
			samples = samples + 1,
			candidate_wins = candidate_wins + excluded.candidate_wins,
			ties = ties + excluded.ties,
			reference_wins = reference_wins + excluded.reference_wins,
			severe_errors = severe_errors + excluded.severe_errors,
			deterministic_failures = deterministic_failures + excluded.deterministic_failures,
			self_escalation_eligible_samples = self_escalation_eligible_samples + excluded.self_escalation_eligible_samples,
			self_escalations = self_escalations + excluded.self_escalations,
			supported_self_escalations = supported_self_escalations + excluded.supported_self_escalations,
			unnecessary_self_escalations = unnecessary_self_escalations + excluded.unnecessary_self_escalations,
			missed_self_escalations = missed_self_escalations + excluded.missed_self_escalations,
			candidate_cost_micro_usd = candidate_cost_micro_usd + excluded.candidate_cost_micro_usd,
			reference_cost_micro_usd = reference_cost_micro_usd + excluded.reference_cost_micro_usd,
			reviewer_cost_micro_usd = reviewer_cost_micro_usd + excluded.reviewer_cost_micro_usd,
			candidate_latency_ms = candidate_latency_ms + excluded.candidate_latency_ms,
			reference_latency_ms = reference_latency_ms + excluded.reference_latency_ms,
			updated_at = excluded.updated_at
	`, evidence.ProfileID, day, evidence.Strategy, evidence.Route, evidence.TaskType,
		evidence.Difficulty, evidence.Risk, evidence.VisionMode,
		evidence.CandidateModel, evidence.ReferenceModel, evidence.ReviewerModel,
		candidateWin, tie, referenceWin, boolInt(evidence.SevereError),
		boolInt(evidence.DeterministicFailure), boolInt(evidence.SelfEscalationEligible),
		boolInt(evidence.SelfEscalationRequested), boolInt(evidence.SelfEscalationSupported),
		boolInt(evidence.SelfEscalationUnnecessary), boolInt(evidence.SelfEscalationMissed),
		evidence.CandidateCostMicroUSD,
		evidence.ReferenceCostMicroUSD, evidence.ReviewerCostMicroUSD,
		evidence.CandidateLatencyMS, evidence.ReferenceLatencyMS, now)
	if err != nil {
		return fmt.Errorf("record routing quality evidence: %w", err)
	}
	for _, dimension := range ReviewDimensions {
		outcome := evidence.Dimensions[dimension]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO routing_quality_dimension_evidence (
				profile_id, evidence_day, strategy_name, route_id, task_type,
				difficulty, risk, vision_mode,
				candidate_model, reference_model, reviewer_model, dimension,
				samples, candidate_wins, ties, reference_wins, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
			ON CONFLICT(
				profile_id, evidence_day, strategy_name, route_id, task_type,
				difficulty, risk, vision_mode,
				candidate_model, reference_model, reviewer_model, dimension
			) DO UPDATE SET
				samples = samples + 1,
				candidate_wins = candidate_wins + excluded.candidate_wins,
				ties = ties + excluded.ties,
				reference_wins = reference_wins + excluded.reference_wins,
				updated_at = excluded.updated_at
		`, evidence.ProfileID, day, evidence.Strategy, evidence.Route, evidence.TaskType,
			evidence.Difficulty, evidence.Risk, evidence.VisionMode,
			evidence.CandidateModel, evidence.ReferenceModel, evidence.ReviewerModel,
			dimension, boolInt(outcome == OutcomeCandidateWin), boolInt(outcome == OutcomeTie),
			boolInt(outcome == OutcomeReferenceWin), now); err != nil {
			return fmt.Errorf("record routing dimension evidence: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routing quality evidence: %w", err)
	}
	return nil
}

func (s *Store) ListEvidence(ctx context.Context, profileID int64) ([]EvidenceAggregate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin routing quality evidence snapshot: %w", err)
	}
	defer tx.Rollback()
	result, err := listEvidenceAggregates(ctx, tx, profileID)
	if err != nil {
		return nil, err
	}
	if err := attachDimensionEvidence(ctx, tx, profileID, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit routing quality evidence snapshot: %w", err)
	}
	return result, nil
}

func (s *Store) OnlineStability(
	ctx context.Context,
	profileID int64,
	strategy string,
	route string,
	model string,
) (OnlineStabilityAggregate, error) {
	return s.OnlineStabilityFor(ctx, profileID, strategy, route, model, "", "")
}

func (s *Store) OnlineStabilityFor(
	ctx context.Context,
	profileID int64,
	strategy string,
	route string,
	model string,
	taskType string,
	difficulty string,
) (OnlineStabilityAggregate, error) {
	if s == nil || profileID <= 0 || strings.TrimSpace(strategy) == "" ||
		strings.TrimSpace(route) == "" || strings.TrimSpace(model) == "" {
		return OnlineStabilityAggregate{}, ErrInvalidEvidence
	}
	var aggregate OnlineStabilityAggregate
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(created_at), COUNT(*),
		       COALESCE(SUM(CASE
		           WHEN status_code BETWEEN 200 AND 299 AND client_committed = 1
		                AND final_model = initial_model THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE
		           WHEN status_code BETWEEN 200 AND 299 AND client_committed = 1
		                AND final_model = initial_model AND answer_attempts = 1
		                AND model_switches = 0
		           THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(elapsed_ms), 0)
		FROM routing_traces
		WHERE profile_id = ? AND strategy_name = ? AND route_id = ? AND initial_model = ?
		  AND (? = '' OR task_type = ?)
		  AND (? = '' OR difficulty = ?)
		  AND date(created_at) IS NOT NULL
		GROUP BY date(created_at)
	`, profileID, strategy, route, model, taskType, taskType, difficulty, difficulty)
	if err != nil {
		return OnlineStabilityAggregate{}, fmt.Errorf("aggregate online routing stability: %w", err)
	}
	defer rows.Close()
	now := s.now().UTC()
	for rows.Next() {
		var day string
		var daily OnlineStabilityAggregate
		if err := rows.Scan(
			&day, &daily.Samples, &daily.SuccessfulCompletions,
			&daily.CleanCompletions, &daily.TotalLatencyMS,
		); err != nil {
			return OnlineStabilityAggregate{}, fmt.Errorf("scan online routing stability: %w", err)
		}
		date, parseErr := time.Parse(dayFormat, day)
		if parseErr != nil {
			continue
		}
		weight := math.Exp2(-max(now.Sub(date).Hours()/24, 0) / evidenceHalfLifeDays)
		aggregate.Samples += daily.Samples
		aggregate.SuccessfulCompletions += daily.SuccessfulCompletions
		aggregate.CleanCompletions += daily.CleanCompletions
		aggregate.TotalLatencyMS += daily.TotalLatencyMS
		aggregate.EffectiveSamples += weight * float64(daily.Samples)
		aggregate.EffectiveSuccessfulCompletions += weight * float64(daily.SuccessfulCompletions)
		aggregate.EffectiveCleanCompletions += weight * float64(daily.CleanCompletions)
		aggregate.EffectiveLatencyMS += weight * float64(daily.TotalLatencyMS)
	}
	if err := rows.Err(); err != nil {
		return OnlineStabilityAggregate{}, fmt.Errorf("read online routing stability: %w", err)
	}
	return aggregate, nil
}

func listEvidenceAggregates(
	ctx context.Context,
	query queryContext,
	profileID int64,
) ([]EvidenceAggregate, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT evidence_day, strategy_name, route_id, task_type, difficulty, risk, vision_mode,
			candidate_model, reference_model, reviewer_model,
			samples, candidate_wins, ties, reference_wins,
			severe_errors, deterministic_failures,
			self_escalation_eligible_samples, self_escalations,
			supported_self_escalations, unnecessary_self_escalations,
			missed_self_escalations,
			candidate_cost_micro_usd, reference_cost_micro_usd, reviewer_cost_micro_usd,
			candidate_latency_ms, reference_latency_ms
		FROM routing_quality_evidence_v2
		WHERE profile_id = ?
		ORDER BY evidence_day DESC, route_id, candidate_model, reference_model
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list routing quality evidence: %w", err)
	}
	defer rows.Close()
	result := make([]EvidenceAggregate, 0)
	for rows.Next() {
		var aggregate EvidenceAggregate
		if err := rows.Scan(
			&aggregate.Day, &aggregate.Strategy, &aggregate.Route, &aggregate.TaskType,
			&aggregate.Difficulty, &aggregate.Risk, &aggregate.VisionMode,
			&aggregate.CandidateModel, &aggregate.ReferenceModel, &aggregate.ReviewerModel,
			&aggregate.Samples, &aggregate.CandidateWins, &aggregate.Ties,
			&aggregate.ReferenceWins, &aggregate.SevereErrors,
			&aggregate.DeterministicFailures,
			&aggregate.SelfEscalationEligibleSamples, &aggregate.SelfEscalations,
			&aggregate.SupportedSelfEscalations, &aggregate.UnnecessarySelfEscalations,
			&aggregate.MissedSelfEscalations, &aggregate.CandidateCostMicroUSD,
			&aggregate.ReferenceCostMicroUSD, &aggregate.ReviewerCostMicroUSD,
			&aggregate.CandidateLatencyMS, &aggregate.ReferenceLatencyMS,
		); err != nil {
			return nil, fmt.Errorf("scan routing quality evidence: %w", err)
		}
		aggregate.Dimensions = make(map[Dimension]DimensionAggregate)
		result = append(result, aggregate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate routing quality evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close routing quality evidence: %w", err)
	}
	return result, nil
}

func attachDimensionEvidence(
	ctx context.Context,
	query queryContext,
	profileID int64,
	result []EvidenceAggregate,
) error {
	rows, err := query.QueryContext(ctx, `
		SELECT evidence_day, strategy_name, route_id, task_type, difficulty, risk, vision_mode,
			candidate_model, reference_model, reviewer_model, dimension,
			samples, candidate_wins, ties, reference_wins
		FROM routing_quality_dimension_evidence
		WHERE profile_id = ?
	`, profileID)
	if err != nil {
		return fmt.Errorf("list routing dimension evidence: %w", err)
	}
	defer rows.Close()
	byKey := make(map[string]*EvidenceAggregate, len(result))
	for index := range result {
		byKey[evidenceAggregateKey(result[index])] = &result[index]
	}
	for rows.Next() {
		var day, strategy, route, taskType, difficulty, risk, visionMode string
		var candidate, reference, reviewer string
		var aggregate DimensionAggregate
		if err := rows.Scan(
			&day, &strategy, &route, &taskType, &difficulty, &risk, &visionMode,
			&candidate, &reference, &reviewer, &aggregate.Dimension,
			&aggregate.Samples, &aggregate.CandidateWins, &aggregate.Ties,
			&aggregate.ReferenceWins,
		); err != nil {
			return fmt.Errorf("scan routing dimension evidence: %w", err)
		}
		key := strings.Join([]string{
			day, strategy, route, taskType, difficulty, risk, visionMode,
			candidate, reference, reviewer,
		}, "\x00")
		if parent := byKey[key]; parent != nil {
			parent.Dimensions[aggregate.Dimension] = aggregate
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate routing dimension evidence: %w", err)
	}
	return nil
}

func evidenceAggregateKey(aggregate EvidenceAggregate) string {
	return strings.Join([]string{
		aggregate.Day, aggregate.Strategy, aggregate.Route, aggregate.TaskType,
		aggregate.Difficulty, aggregate.Risk, aggregate.VisionMode,
		aggregate.CandidateModel, aggregate.ReferenceModel, aggregate.ReviewerModel,
	}, "\x00")
}

func validateEvidence(evidence Evidence) error {
	if evidence.ProfileID <= 0 ||
		strings.TrimSpace(evidence.Strategy) == "" ||
		strings.TrimSpace(evidence.Route) == "" ||
		strings.TrimSpace(evidence.TaskType) == "" ||
		strings.TrimSpace(evidence.Difficulty) == "" ||
		strings.TrimSpace(evidence.Risk) == "" ||
		strings.TrimSpace(evidence.VisionMode) == "" ||
		strings.TrimSpace(evidence.CandidateModel) == "" ||
		strings.TrimSpace(evidence.ReferenceModel) == "" ||
		strings.TrimSpace(evidence.ReviewerModel) == "" ||
		(evidence.Outcome != OutcomeCandidateWin && evidence.Outcome != OutcomeTie &&
			evidence.Outcome != OutcomeReferenceWin) ||
		evidence.CandidateCostMicroUSD < 0 || evidence.ReferenceCostMicroUSD < 0 ||
		evidence.ReviewerCostMicroUSD < 0 || evidence.CandidateLatencyMS < 0 ||
		evidence.ReferenceLatencyMS < 0 || !validDimensionOutcomes(evidence.Dimensions) ||
		!validSelfEscalationEvidence(evidence) {
		return ErrInvalidEvidence
	}
	return nil
}

func validDimensionOutcomes(dimensions map[Dimension]Outcome) bool {
	if len(dimensions) != len(ReviewDimensions) {
		return false
	}
	for _, dimension := range ReviewDimensions {
		outcome, ok := dimensions[dimension]
		if !ok || (outcome != OutcomeCandidateWin && outcome != OutcomeTie &&
			outcome != OutcomeReferenceWin) {
			return false
		}
	}
	return true
}

func validSelfEscalationEvidence(evidence Evidence) bool {
	if !evidence.SelfEscalationEligible {
		return !evidence.SelfEscalationRequested && !evidence.SelfEscalationSupported &&
			!evidence.SelfEscalationUnnecessary && !evidence.SelfEscalationMissed
	}
	if evidence.SelfEscalationRequested {
		return evidence.SelfEscalationSupported != evidence.SelfEscalationUnnecessary &&
			!evidence.SelfEscalationMissed
	}
	return !evidence.SelfEscalationSupported && !evidence.SelfEscalationUnnecessary
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
