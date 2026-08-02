package stats

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RoutingCallFilter struct {
	TraceID       *int64
	CorrelationID string
	Limit         int
}

type RoutingCallRow struct {
	ID                int64     `json:"id"`
	CreatedAt         time.Time `json:"created_at"`
	TraceID           int64     `json:"trace_id"`
	CorrelationID     string    `json:"correlation_id"`
	Sequence          int       `json:"sequence"`
	Kind              string    `json:"kind"`
	Model             string    `json:"model"`
	Target            string    `json:"target"`
	ImageIndex        int       `json:"image_index"`
	RetryIndex        int       `json:"retry_index"`
	ModelSwitchIndex  int       `json:"model_switch_index"`
	TargetSwitchIndex int       `json:"target_switch_index"`
	EstimatedMicroUSD int64     `json:"estimated_cost_micro_usd"`
	ActualCostKnown   bool      `json:"actual_cost_known"`
	ActualMicroUSD    int64     `json:"actual_cost_micro_usd"`
	StatusCode        int       `json:"status_code"`
	Outcome           string    `json:"outcome"`
}

func (s *DB) QueryRoutingCalls(
	ctx context.Context,
	filter RoutingCallFilter,
) ([]RoutingCallRow, error) {
	if filter.TraceID == nil && strings.TrimSpace(filter.CorrelationID) == "" {
		return nil, fmt.Errorf("routing calls: trace_id or correlation_id is required")
	}
	if filter.TraceID != nil && *filter.TraceID <= 0 {
		return nil, fmt.Errorf("routing calls: trace_id must be positive")
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		return nil, fmt.Errorf("routing calls: limit must be between 1 and 500")
	}
	predicates := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if filter.TraceID != nil {
		predicates = append(predicates, "trace_id = ?")
		args = append(args, *filter.TraceID)
	}
	if strings.TrimSpace(filter.CorrelationID) != "" {
		predicates = append(predicates, "correlation_id = ?")
		args = append(args, filter.CorrelationID)
	}
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, trace_id, correlation_id, sequence, kind,
		       logical_model, target, image_index, retry_index,
		       model_switch_index, target_switch_index,
		       estimated_cost_micro_usd, actual_cost_known,
		       actual_cost_micro_usd, status_code, outcome
		FROM routing_calls
		WHERE `+strings.Join(predicates, " AND ")+`
		ORDER BY trace_id DESC, sequence ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("routing calls: query: %w", err)
	}
	defer rows.Close()

	result := make([]RoutingCallRow, 0)
	for rows.Next() {
		var row RoutingCallRow
		var createdAt string
		var actualKnown int
		if err := rows.Scan(
			&row.ID, &createdAt, &row.TraceID, &row.CorrelationID,
			&row.Sequence, &row.Kind, &row.Model, &row.Target,
			&row.ImageIndex, &row.RetryIndex, &row.ModelSwitchIndex,
			&row.TargetSwitchIndex, &row.EstimatedMicroUSD, &actualKnown,
			&row.ActualMicroUSD, &row.StatusCode, &row.Outcome,
		); err != nil {
			return nil, fmt.Errorf("routing calls: scan: %w", err)
		}
		row.CreatedAt, err = time.Parse(usageTimeFormat, createdAt)
		if err != nil {
			return nil, fmt.Errorf("routing calls: parse created_at: %w", err)
		}
		row.ActualCostKnown = actualKnown == 1
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing calls: read: %w", err)
	}
	return result, nil
}
