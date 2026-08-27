package agenttrajectory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/routing"
)

var (
	ErrConflict = errors.New("agent trajectory state conflict")
	ErrNotFound = errors.New("agent trajectory not found")
)

type Source string

const (
	SourceSession         Source = "session"
	SourceRequestSnapshot Source = "request_snapshot"
)

type Status string

const (
	StatusCollecting  Status = "collecting"
	StatusCompleted   Status = "completed"
	StatusQueued      Status = "queued"
	StatusEvaluating  Status = "evaluating"
	StatusEvaluated   Status = "evaluated"
	StatusTimedOut    Status = "timed_out"
	StatusInterrupted Status = "interrupted"
	StatusSkipped     Status = "skipped"
	StatusFailed      Status = "failed"
	StatusDeleted     Status = "deleted"
)

type Record struct {
	ID                     int64             `json:"id"`
	ProfileID              int64             `json:"profile_id"`
	ProfileSlug            string            `json:"profile_slug"`
	Protocol               routing.Operation `json:"protocol"`
	Source                 Source            `json:"source"`
	Status                 Status            `json:"status"`
	SessionKey             []byte            `json:"-"`
	Strategy               string            `json:"strategy"`
	Route                  string            `json:"route"`
	TaskType               string            `json:"task_type"`
	Difficulty             string            `json:"difficulty"`
	Risk                   string            `json:"risk"`
	VisionMode             string            `json:"vision_mode"`
	ModelPath              []string          `json:"model_path"`
	ToolCalls              int               `json:"tool_calls"`
	Turns                  int               `json:"turns"`
	ElapsedMS              int64             `json:"elapsed_ms"`
	Truncated              bool              `json:"truncated"`
	ReasonCode             string            `json:"reason_code,omitempty"`
	CandidateCostMicroUSD  int64             `json:"candidate_cost_micro_usd"`
	EvaluationCostMicroUSD int64             `json:"evaluation_cost_micro_usd"`
	EvidenceRecorded       bool              `json:"evidence_recorded"`
	EvaluationResult       *EvaluationResult `json:"evaluation_result,omitempty"`
	Payload                EncryptedPayload  `json:"-"`
	ObservationDigest      []byte            `json:"-"`
	StartedAt              time.Time         `json:"started_at"`
	CompletedAt            *time.Time        `json:"completed_at,omitempty"`
	ExpiresAt              time.Time         `json:"expires_at"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type EvaluationResult struct {
	CandidateModel        string            `json:"candidate_model"`
	ReferenceModel        string            `json:"reference_model"`
	ReviewerModel         string            `json:"reviewer_model"`
	Outcome               string            `json:"outcome"`
	Dimensions            map[string]string `json:"dimensions"`
	SevereError           bool              `json:"severe_error"`
	DeterministicFailure  bool              `json:"deterministic_failure"`
	CandidateCostMicroUSD int64             `json:"candidate_cost_micro_usd"`
	ReferenceCostMicroUSD int64             `json:"reference_cost_micro_usd"`
	ReviewerCostMicroUSD  int64             `json:"reviewer_cost_micro_usd"`
	CandidateLatencyMS    int64             `json:"candidate_latency_ms"`
	ReferenceLatencyMS    int64             `json:"reference_latency_ms"`
}

type CreateInput struct {
	ProfileID             int64
	ProfileSlug           string
	Protocol              routing.Operation
	Source                Source
	SessionKey            []byte
	Strategy              string
	Route                 string
	TaskType              string
	Difficulty            string
	Risk                  string
	VisionMode            string
	ModelPath             []string
	ToolCalls             int
	Turns                 int
	ElapsedMS             int64
	Truncated             bool
	ReasonCode            string
	CandidateCostMicroUSD int64
	Payload               EncryptedPayload
	ObservationDigest     []byte
	StartedAt             time.Time
	ExpiresAt             time.Time
}

type ContentUpdate struct {
	ModelPath             []string
	ToolCalls             int
	Turns                 int
	ElapsedMS             int64
	Truncated             bool
	ReasonCode            string
	CandidateCostMicroUSD int64
	Payload               EncryptedPayload
	ObservationDigest     []byte
	CompletedAt           *time.Time
}

type ListFilter struct {
	ProfileID int64
	Status    Status
	Source    Source
	Risk      string
	Model     string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

type ListResult struct {
	Items []Record `json:"items"`
	Total int64    `json:"total"`
}

type Summary struct {
	Total                  int64 `json:"total"`
	Collecting             int64 `json:"collecting"`
	Completed              int64 `json:"completed"`
	Evaluated              int64 `json:"evaluated"`
	EvidenceRecorded       int64 `json:"evidence_recorded"`
	TimedOut               int64 `json:"timed_out"`
	Interrupted            int64 `json:"interrupted"`
	Skipped                int64 `json:"skipped"`
	Failed                 int64 `json:"failed"`
	EvaluationCostMicroUSD int64 `json:"evaluation_cost_micro_usd"`
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (s *Store) Create(ctx context.Context, input CreateInput) (Record, error) {
	if s == nil || s.db == nil || !validCreateInput(input) {
		return Record{}, ErrConflict
	}
	startedAt := input.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = s.now().UTC()
	}
	expiresAt := input.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = startedAt.Add(7 * 24 * time.Hour)
	}
	if !expiresAt.After(startedAt) {
		return Record{}, ErrConflict
	}
	modelPathJSON, err := json.Marshal(normalizeModelPath(input.ModelPath))
	if err != nil {
		return Record{}, fmt.Errorf("encode agent trajectory model path: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO routing_agent_trajectories (
			profile_id, profile_slug, protocol, source, status, session_key,
			strategy_name, route_id, task_type, difficulty, risk, vision_mode,
			model_path_json, tool_calls, turns, elapsed_ms, truncated, reason_code,
			candidate_cost_micro_usd, evaluation_cost_micro_usd, evidence_recorded,
			encryption_version, nonce, ciphertext, observation_digest,
			started_at, completed_at, expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, NULL, ?, ?, ?)
	`,
		input.ProfileID, input.ProfileSlug, input.Protocol, input.Source, StatusCollecting, nullableSessionKey(input.SessionKey),
		input.Strategy, input.Route, input.TaskType, input.Difficulty, input.Risk, input.VisionMode,
		string(modelPathJSON), input.ToolCalls, input.Turns, input.ElapsedMS, boolInt(input.Truncated), input.ReasonCode, input.CandidateCostMicroUSD,
		input.Payload.Version, input.Payload.Nonce, input.Payload.Ciphertext,
		nullableObservationDigest(input.ObservationDigest),
		startedAt.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano), now, now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Record{}, ErrConflict
		}
		return Record{}, fmt.Errorf("create agent trajectory: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Record{}, fmt.Errorf("read agent trajectory id: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *Store) UpdateCollecting(ctx context.Context, id int64, update ContentUpdate) error {
	return s.updateContent(ctx, id, update, false)
}

func (s *Store) Complete(ctx context.Context, id int64, update ContentUpdate) error {
	return s.updateContent(ctx, id, update, true)
}

func (s *Store) updateContent(ctx context.Context, id int64, update ContentUpdate, complete bool) error {
	if s == nil || s.db == nil || id <= 0 || !validContentUpdate(update) {
		return ErrConflict
	}
	modelPathJSON, err := json.Marshal(normalizeModelPath(update.ModelPath))
	if err != nil {
		return fmt.Errorf("encode agent trajectory model path: %w", err)
	}
	status := StatusCollecting
	completedAt := any(nil)
	if complete {
		status = StatusCompleted
		value := s.now().UTC()
		if update.CompletedAt != nil {
			value = update.CompletedAt.UTC()
		}
		completedAt = value.Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routing_agent_trajectories
		SET status = ?, model_path_json = ?, tool_calls = ?, turns = ?, elapsed_ms = ?,
			truncated = ?, reason_code = ?, candidate_cost_micro_usd = ?, encryption_version = ?, nonce = ?, ciphertext = ?,
			observation_digest = COALESCE(?, observation_digest),
			completed_at = COALESCE(?, completed_at), updated_at = ?
		WHERE id = ? AND status = 'collecting'
	`, status, string(modelPathJSON), update.ToolCalls, update.Turns, update.ElapsedMS,
		boolInt(update.Truncated), update.ReasonCode, update.CandidateCostMicroUSD, update.Payload.Version, update.Payload.Nonce,
		update.Payload.Ciphertext, nullableObservationDigest(update.ObservationDigest), completedAt,
		s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("update agent trajectory content: %w", err)
	}
	return requireTransition(result)
}

func (s *Store) MarkQueued(ctx context.Context, id int64) error {
	return s.transition(ctx, id, StatusQueued, "", 0, false, StatusCompleted)
}

func (s *Store) MarkEvaluating(ctx context.Context, id int64) error {
	return s.transition(ctx, id, StatusEvaluating, "", 0, false, StatusQueued)
}

func (s *Store) MarkEvaluated(ctx context.Context, id, costMicroUSD int64, evaluationResult EvaluationResult) error {
	if s == nil || s.db == nil || id <= 0 || costMicroUSD < 0 || !validEvaluationResult(evaluationResult) {
		return ErrConflict
	}
	encoded, err := json.Marshal(evaluationResult)
	if err != nil {
		return fmt.Errorf("encode agent trajectory evaluation result: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routing_agent_trajectories
		SET status = 'evaluated', reason_code = '', evaluation_cost_micro_usd = ?,
			evidence_recorded = 1, evaluation_result_json = ?, updated_at = ?
		WHERE id = ? AND status = 'evaluating'
	`, costMicroUSD, string(encoded), s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("mark agent trajectory evaluated: %w", err)
	}
	return requireTransition(result)
}

func (s *Store) MarkSkipped(ctx context.Context, id int64, reasonCode string) error {
	return s.transition(ctx, id, StatusSkipped, reasonCode, 0, false,
		StatusCompleted, StatusQueued, StatusEvaluating)
}

func (s *Store) MarkFailed(ctx context.Context, id int64, reasonCode string, costMicroUSD int64) error {
	if costMicroUSD < 0 {
		return ErrConflict
	}
	return s.transition(ctx, id, StatusFailed, reasonCode, costMicroUSD, false,
		StatusCollecting, StatusCompleted, StatusQueued, StatusEvaluating)
}

func (s *Store) MarkTimedOut(ctx context.Context, id int64, reasonCode string) error {
	return s.transition(ctx, id, StatusTimedOut, reasonCode, 0, false, StatusCollecting)
}

func (s *Store) transition(
	ctx context.Context,
	id int64,
	to Status,
	reasonCode string,
	costMicroUSD int64,
	evidenceRecorded bool,
	from ...Status,
) error {
	if s == nil || s.db == nil || id <= 0 || !validStatus(to) || len(from) == 0 || len(reasonCode) > 128 {
		return ErrConflict
	}
	placeholders := make([]string, len(from))
	args := []any{to, reasonCode, costMicroUSD, boolInt(evidenceRecorded), s.now().UTC().Format(time.RFC3339Nano), id}
	for index, status := range from {
		placeholders[index] = "?"
		args = append(args, status)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routing_agent_trajectories
		SET status = ?, reason_code = ?, evaluation_cost_micro_usd = ?, evidence_recorded = ?, updated_at = ?
		WHERE id = ? AND status IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("transition agent trajectory: %w", err)
	}
	return requireTransition(result)
}

func (s *Store) MarkInterrupted(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routing_agent_trajectories
		SET status = 'interrupted', reason_code = 'service_restarted', updated_at = ?
		WHERE status IN ('completed', 'queued', 'evaluating')
	`, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("interrupt running agent trajectory evaluations: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) TimeoutIdle(ctx context.Context, updatedBefore time.Time) (int64, error) {
	if s == nil || s.db == nil || updatedBefore.IsZero() {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routing_agent_trajectories
		SET status = 'timed_out', reason_code = 'idle_timeout', updated_at = ?
		WHERE status = 'collecting' AND updated_at <= ?
	`, s.now().UTC().Format(time.RFC3339Nano), updatedBefore.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("time out idle agent trajectories: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) FindCollectingBySession(
	ctx context.Context,
	profileID int64,
	sessionKey []byte,
) (Record, bool, error) {
	if s == nil || s.db == nil || profileID <= 0 || len(sessionKey) != 32 {
		return Record{}, false, nil
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, selectRecordSQL+`
		WHERE profile_id = ? AND session_key = ? AND status = 'collecting'
		LIMIT 1
	`, profileID, sessionKey))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("find collecting agent trajectory: %w", err)
	}
	return record, true, nil
}

func (s *Store) FindByObservationDigest(
	ctx context.Context,
	profileID int64,
	sessionKey []byte,
	digest []byte,
) (Record, bool, error) {
	if s == nil || s.db == nil || profileID <= 0 || len(sessionKey) != 32 || len(digest) != 32 {
		return Record{}, false, nil
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, selectRecordSQL+`
		WHERE profile_id = ? AND session_key = ? AND observation_digest = ? AND source = 'session'
		LIMIT 1
	`, profileID, sessionKey, digest))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("find agent trajectory observation: %w", err)
	}
	return record, true, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Record, error) {
	if s == nil || s.db == nil || id <= 0 {
		return Record{}, ErrNotFound
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, selectRecordSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("get agent trajectory: %w", err)
	}
	return record, nil
}

func (s *Store) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	if s == nil || s.db == nil || filter.ProfileID < 0 || filter.Offset < 0 ||
		(filter.Status != "" && !validStatus(filter.Status)) ||
		(filter.Source != "" && !validSource(filter.Source)) {
		return ListResult{}, ErrConflict
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return ListResult{}, ErrConflict
	}
	where, args := listWhere(filter)
	var result ListResult
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routing_agent_trajectories`+where, args...).Scan(&result.Total); err != nil {
		return ListResult{}, fmt.Errorf("count agent trajectories: %w", err)
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, selectMetadataSQL+where+`
		ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list agent trajectories: %w", err)
	}
	defer rows.Close()
	result.Items = make([]Record, 0)
	for rows.Next() {
		record, err := scanMetadata(rows)
		if err != nil {
			return ListResult{}, fmt.Errorf("scan agent trajectory: %w", err)
		}
		result.Items = append(result.Items, record)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate agent trajectories: %w", err)
	}
	return result, nil
}

func (s *Store) Summary(ctx context.Context, filter ListFilter) (Summary, error) {
	if s == nil || s.db == nil || filter.ProfileID < 0 ||
		(filter.Status != "" && !validStatus(filter.Status)) ||
		(filter.Source != "" && !validSource(filter.Source)) {
		return Summary{}, ErrConflict
	}
	where, args := listWhere(filter)
	var summary Summary
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(status = 'collecting'), 0),
		       COALESCE(SUM(status IN ('completed', 'queued', 'evaluating', 'evaluated', 'skipped')), 0),
		       COALESCE(SUM(status = 'evaluated'), 0),
		       COALESCE(SUM(evidence_recorded = 1), 0),
		       COALESCE(SUM(status = 'timed_out'), 0),
		       COALESCE(SUM(status = 'interrupted'), 0),
		       COALESCE(SUM(status = 'skipped'), 0),
		       COALESCE(SUM(status = 'failed'), 0),
		       COALESCE(SUM(evaluation_cost_micro_usd), 0)
		FROM routing_agent_trajectories`+where,
		args...,
	).Scan(
		&summary.Total, &summary.Collecting, &summary.Completed, &summary.Evaluated,
		&summary.EvidenceRecorded, &summary.TimedOut, &summary.Interrupted,
		&summary.Skipped, &summary.Failed, &summary.EvaluationCostMicroUSD,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("summarize agent trajectories: %w", err)
	}
	return summary, nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	if s == nil || s.db == nil || id <= 0 {
		return ErrNotFound
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM routing_agent_trajectories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent trajectory: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect deleted agent trajectory: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM routing_agent_trajectories WHERE expires_at <= ?
	`, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete expired agent trajectories: %w", err)
	}
	return result.RowsAffected()
}

const selectRecordSQL = `
	SELECT id, profile_id, profile_slug, protocol, source, status, session_key,
	       strategy_name, route_id, task_type, difficulty, risk, vision_mode,
	       model_path_json, tool_calls, turns, elapsed_ms, truncated, reason_code,
	       candidate_cost_micro_usd, evaluation_cost_micro_usd, evidence_recorded,
	       evaluation_result_json,
	       encryption_version, nonce, ciphertext, observation_digest,
	       started_at, completed_at, expires_at, created_at, updated_at
	FROM routing_agent_trajectories`

const selectMetadataSQL = `
	SELECT id, profile_id, profile_slug, protocol, source, status,
	       strategy_name, route_id, task_type, difficulty, risk, vision_mode,
	       model_path_json, tool_calls, turns, elapsed_ms, truncated, reason_code,
	       candidate_cost_micro_usd, evaluation_cost_micro_usd, evidence_recorded,
	       evaluation_result_json,
	       started_at, completed_at, expires_at, created_at, updated_at
	FROM routing_agent_trajectories`

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var modelPathJSON, evaluationResultJSON, startedAt, expiresAt, createdAt, updatedAt string
	var completedAt sql.NullString
	var truncated, evidenceRecorded int
	err := row.Scan(
		&record.ID, &record.ProfileID, &record.ProfileSlug, &record.Protocol, &record.Source, &record.Status,
		&record.SessionKey, &record.Strategy, &record.Route, &record.TaskType, &record.Difficulty,
		&record.Risk, &record.VisionMode, &modelPathJSON, &record.ToolCalls, &record.Turns,
		&record.ElapsedMS, &truncated, &record.ReasonCode, &record.CandidateCostMicroUSD, &record.EvaluationCostMicroUSD,
		&evidenceRecorded, &evaluationResultJSON, &record.Payload.Version, &record.Payload.Nonce, &record.Payload.Ciphertext,
		&record.ObservationDigest,
		&startedAt, &completedAt, &expiresAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return Record{}, err
	}
	if err := finishRecord(&record, modelPathJSON, startedAt, completedAt, expiresAt, createdAt, updatedAt); err != nil {
		return Record{}, err
	}
	record.Truncated = truncated != 0
	record.EvidenceRecorded = evidenceRecorded != 0
	if err := decodeEvaluationResult(&record, evaluationResultJSON); err != nil {
		return Record{}, err
	}
	return record, nil
}

func scanMetadata(row rowScanner) (Record, error) {
	var record Record
	var modelPathJSON, evaluationResultJSON, startedAt, expiresAt, createdAt, updatedAt string
	var completedAt sql.NullString
	var truncated, evidenceRecorded int
	err := row.Scan(
		&record.ID, &record.ProfileID, &record.ProfileSlug, &record.Protocol, &record.Source, &record.Status,
		&record.Strategy, &record.Route, &record.TaskType, &record.Difficulty, &record.Risk,
		&record.VisionMode, &modelPathJSON, &record.ToolCalls, &record.Turns, &record.ElapsedMS,
		&truncated, &record.ReasonCode, &record.CandidateCostMicroUSD, &record.EvaluationCostMicroUSD, &evidenceRecorded,
		&evaluationResultJSON,
		&startedAt, &completedAt, &expiresAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return Record{}, err
	}
	if err := finishRecord(&record, modelPathJSON, startedAt, completedAt, expiresAt, createdAt, updatedAt); err != nil {
		return Record{}, err
	}
	record.Truncated = truncated != 0
	record.EvidenceRecorded = evidenceRecorded != 0
	if err := decodeEvaluationResult(&record, evaluationResultJSON); err != nil {
		return Record{}, err
	}
	return record, nil
}

func decodeEvaluationResult(record *Record, raw string) error {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return nil
	}
	var result EvaluationResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return err
	}
	record.EvaluationResult = &result
	return nil
}

func validEvaluationResult(result EvaluationResult) bool {
	if strings.TrimSpace(result.CandidateModel) == "" || strings.TrimSpace(result.ReferenceModel) == "" ||
		strings.TrimSpace(result.ReviewerModel) == "" || len(result.Dimensions) != 5 {
		return false
	}
	switch result.Outcome {
	case "candidate_win", "tie", "reference_win":
	default:
		return false
	}
	for _, outcome := range result.Dimensions {
		if outcome != "candidate_win" && outcome != "tie" && outcome != "reference_win" {
			return false
		}
	}
	return result.CandidateCostMicroUSD >= 0 && result.ReferenceCostMicroUSD >= 0 &&
		result.ReviewerCostMicroUSD >= 0 && result.CandidateLatencyMS >= 0 && result.ReferenceLatencyMS >= 0
}

func finishRecord(
	record *Record,
	modelPathJSON, startedAt string,
	completedAt sql.NullString,
	expiresAt, createdAt, updatedAt string,
) error {
	if err := json.Unmarshal([]byte(modelPathJSON), &record.ModelPath); err != nil {
		return err
	}
	var err error
	if record.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return err
	}
	if completedAt.Valid {
		value, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return err
		}
		record.CompletedAt = &value
	}
	if record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		return err
	}
	if record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return err
	}
	if record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return err
	}
	return nil
}

func listWhere(filter ListFilter) (string, []any) {
	conditions := make([]string, 0)
	args := make([]any, 0)
	if filter.ProfileID > 0 {
		conditions = append(conditions, "profile_id = ?")
		args = append(args, filter.ProfileID)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, filter.Source)
	}
	if filter.Risk != "" {
		conditions = append(conditions, "risk = ?")
		args = append(args, filter.Risk)
	}
	if filter.Model != "" {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(model_path_json) WHERE value = ?)")
		args = append(args, filter.Model)
	}
	if filter.From != nil {
		conditions = append(conditions, "started_at >= ?")
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if filter.To != nil {
		conditions = append(conditions, "started_at < ?")
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func validCreateInput(input CreateInput) bool {
	return input.ProfileID > 0 && strings.TrimSpace(input.ProfileSlug) != "" &&
		supportedOperation(input.Protocol) && validSource(input.Source) &&
		((input.Source == SourceSession && len(input.SessionKey) == 32) ||
			(input.Source == SourceRequestSnapshot && len(input.SessionKey) == 0)) &&
		(input.ObservationDigest == nil || len(input.ObservationDigest) == 32) &&
		input.ToolCalls >= 0 && input.Turns >= 0 && input.ElapsedMS >= 0 && input.CandidateCostMicroUSD >= 0 &&
		input.Payload.Version > 0 && len(input.Payload.Nonce) > 0 && len(input.Payload.Ciphertext) > 0 &&
		len(input.ReasonCode) <= 128
}

func validContentUpdate(update ContentUpdate) bool {
	return update.ToolCalls >= 0 && update.Turns >= 0 && update.ElapsedMS >= 0 && update.CandidateCostMicroUSD >= 0 &&
		(update.ObservationDigest == nil || len(update.ObservationDigest) == 32) &&
		update.Payload.Version > 0 && len(update.Payload.Nonce) > 0 && len(update.Payload.Ciphertext) > 0 &&
		len(update.ReasonCode) <= 128
}

func validSource(source Source) bool {
	return source == SourceSession || source == SourceRequestSnapshot
}

func validStatus(status Status) bool {
	switch status {
	case StatusCollecting, StatusCompleted, StatusQueued, StatusEvaluating, StatusEvaluated,
		StatusTimedOut, StatusInterrupted, StatusSkipped, StatusFailed, StatusDeleted:
		return true
	default:
		return false
	}
}

func normalizeModelPath(path []string) []string {
	if path == nil {
		return []string{}
	}
	return path
}

func nullableSessionKey(key []byte) any {
	if len(key) == 0 {
		return nil
	}
	return key
}

func nullableObservationDigest(digest []byte) any {
	if len(digest) == 0 {
		return nil
	}
	return digest
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireTransition(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect agent trajectory transition: %w", err)
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}
