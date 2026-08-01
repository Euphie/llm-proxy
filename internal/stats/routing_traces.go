package stats

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type RoutingTraceFilter struct {
	ProfileID *int64
	From      time.Time
	To        time.Time
	Limit     int
}

type RoutingTraceRow struct {
	ID                           int64     `json:"id"`
	CreatedAt                    time.Time `json:"created_at"`
	ProfileID                    *int64    `json:"profile_id,omitempty"`
	ProfileSlug                  string    `json:"profile_slug"`
	Protocol                     string    `json:"protocol"`
	Path                         string    `json:"path"`
	Strategy                     string    `json:"strategy"`
	Route                        string    `json:"route"`
	TaskType                     string    `json:"task_type"`
	Risk                         string    `json:"risk"`
	ClassificationSource         string    `json:"classification_source"`
	InitialModel                 string    `json:"initial_model"`
	FinalModel                   string    `json:"final_model"`
	InitialTarget                string    `json:"initial_target"`
	FinalTarget                  string    `json:"final_target"`
	VisionMode                   string    `json:"vision_mode"`
	StatusCode                   int       `json:"status_code"`
	ClientCommitted              bool      `json:"client_committed"`
	AnswerAttempts               int       `json:"answer_attempts"`
	AuxiliaryCalls               int       `json:"auxiliary_calls"`
	TotalOutboundCalls           int       `json:"total_outbound_calls"`
	ModelSwitches                int       `json:"model_switches"`
	TargetSwitches               int       `json:"target_switches"`
	PlannedWorstCaseCostMicroUSD int64     `json:"planned_worst_case_cost_micro_usd"`
	ReservedCostMicroUSD         int64     `json:"reserved_cost_micro_usd"`
	ElapsedMilliseconds          int64     `json:"elapsed_ms"`
}

func (s *DB) QueryRoutingTraces(
	ctx context.Context,
	filter RoutingTraceFilter,
) ([]RoutingTraceRow, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	predicates := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.ProfileID != nil {
		predicates = append(predicates, "profile_id = ?")
		args = append(args, *filter.ProfileID)
	}
	if !filter.From.IsZero() {
		predicates = append(predicates, "created_at >= ?")
		args = append(args, filter.From.UTC().Format(usageTimeFormat))
	}
	if !filter.To.IsZero() {
		predicates = append(predicates, "created_at <= ?")
		args = append(args, filter.To.UTC().Format(usageTimeFormat))
	}
	where := ""
	if len(predicates) > 0 {
		where = " WHERE " + strings.Join(predicates, " AND ")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, profile_id, profile_slug, protocol, path,
		       strategy_name, route_id, task_type, risk, classification_source,
		       initial_model, final_model, initial_target, final_target,
		       vision_mode, status_code,
		       client_committed, answer_attempts, auxiliary_calls,
		       total_outbound_calls, model_switches, target_switches,
		       planned_worst_case_cost_micro_usd, reserved_cost_micro_usd,
		       elapsed_ms
		FROM routing_traces`+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("routing traces: query: %w", err)
	}
	defer rows.Close()

	result := make([]RoutingTraceRow, 0)
	for rows.Next() {
		var row RoutingTraceRow
		var createdAt string
		var profileID sql.NullInt64
		var committed int
		if err := rows.Scan(
			&row.ID, &createdAt, &profileID, &row.ProfileSlug, &row.Protocol,
			&row.Path, &row.Strategy, &row.Route, &row.TaskType, &row.Risk,
			&row.ClassificationSource, &row.InitialModel, &row.FinalModel,
			&row.InitialTarget, &row.FinalTarget,
			&row.VisionMode, &row.StatusCode, &committed, &row.AnswerAttempts,
			&row.AuxiliaryCalls, &row.TotalOutboundCalls, &row.ModelSwitches,
			&row.TargetSwitches, &row.PlannedWorstCaseCostMicroUSD,
			&row.ReservedCostMicroUSD, &row.ElapsedMilliseconds,
		); err != nil {
			return nil, fmt.Errorf("routing traces: scan: %w", err)
		}
		row.CreatedAt, err = time.Parse(usageTimeFormat, createdAt)
		if err != nil {
			return nil, fmt.Errorf("routing traces: parse created_at: %w", err)
		}
		if profileID.Valid {
			id := profileID.Int64
			row.ProfileID = &id
		}
		row.ClientCommitted = committed == 1
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing traces: read: %w", err)
	}
	return result, nil
}
