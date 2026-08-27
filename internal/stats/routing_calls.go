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
	ID                 int64     `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
	TraceID            int64     `json:"trace_id"`
	CorrelationID      string    `json:"correlation_id"`
	Sequence           int       `json:"sequence"`
	Kind               string    `json:"kind"`
	Model              string    `json:"model"`
	ImageIndex         int       `json:"image_index"`
	RetryIndex         int       `json:"retry_index"`
	ModelSwitchIndex   int       `json:"model_switch_index"`
	EstimatedMicroUSD  int64     `json:"estimated_cost_micro_usd"`
	ActualCostKnown    bool      `json:"actual_cost_known"`
	ActualMicroUSD     int64     `json:"actual_cost_micro_usd"`
	UsagePresent       bool      `json:"usage_present"`
	InputTokens        int       `json:"input_tokens"`
	OutputTokens       int       `json:"output_tokens"`
	CacheReadTokens    int       `json:"cache_read_tokens"`
	CacheWriteTokens   int       `json:"cache_write_tokens"`
	InputIncludesCache bool      `json:"input_includes_cache"`
	StatusCode         int       `json:"status_code"`
	Outcome            string    `json:"outcome"`
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
		       logical_model, image_index, retry_index,
		       model_switch_index,
		       estimated_cost_micro_usd, actual_cost_known,
		       actual_cost_micro_usd, usage_present, input_tokens, output_tokens,
		       cache_read_tokens, cache_write_tokens, input_includes_cache,
		       status_code, outcome
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
		var usagePresent int
		var inputIncludesCache int
		if err := rows.Scan(
			&row.ID, &createdAt, &row.TraceID, &row.CorrelationID,
			&row.Sequence, &row.Kind, &row.Model,
			&row.ImageIndex, &row.RetryIndex, &row.ModelSwitchIndex,
			&row.EstimatedMicroUSD, &actualKnown,
			&row.ActualMicroUSD, &usagePresent, &row.InputTokens, &row.OutputTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &inputIncludesCache,
			&row.StatusCode, &row.Outcome,
		); err != nil {
			return nil, fmt.Errorf("routing calls: scan: %w", err)
		}
		row.CreatedAt, err = time.Parse(usageTimeFormat, createdAt)
		if err != nil {
			return nil, fmt.Errorf("routing calls: parse created_at: %w", err)
		}
		row.ActualCostKnown = actualKnown == 1
		row.UsagePresent = usagePresent == 1
		row.InputIncludesCache = inputIncludesCache == 1
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing calls: read: %w", err)
	}
	return result, nil
}

type ModelCacheMetrics struct {
	Samples             int64
	UncachedInputTokens int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
}

func (s *DB) QueryModelCacheMetrics(
	ctx context.Context,
	profileID int64,
) (map[string]ModelCacheMetrics, error) {
	if profileID <= 0 {
		return map[string]ModelCacheMetrics{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.logical_model,
		       COUNT(*),
		       SUM(CASE
		           WHEN c.input_includes_cache = 1
		           THEN c.input_tokens - c.cache_read_tokens - c.cache_write_tokens
		           ELSE c.input_tokens
		       END),
		       SUM(c.cache_read_tokens),
		       SUM(c.cache_write_tokens)
		FROM routing_calls c
		JOIN routing_traces t ON t.id = c.trace_id
		WHERE t.profile_id = ?
		  AND c.kind = 'answer'
		  AND c.usage_present = 1
		  AND c.status_code BETWEEN 200 AND 299
		  AND c.logical_model <> ''
		  AND c.created_at >= ?
		  AND c.input_tokens >= CASE
		      WHEN c.input_includes_cache = 1
		      THEN c.cache_read_tokens + c.cache_write_tokens
		      ELSE 0
		  END
		GROUP BY c.logical_model
	`, profileID, time.Now().UTC().AddDate(0, 0, -30).Format(usageTimeFormat))
	if err != nil {
		return nil, fmt.Errorf("routing cache metrics: query: %w", err)
	}
	defer rows.Close()
	result := make(map[string]ModelCacheMetrics)
	for rows.Next() {
		var model string
		var metrics ModelCacheMetrics
		if err := rows.Scan(
			&model, &metrics.Samples, &metrics.UncachedInputTokens,
			&metrics.CacheReadTokens, &metrics.CacheWriteTokens,
		); err != nil {
			return nil, fmt.Errorf("routing cache metrics: scan: %w", err)
		}
		result[model] = metrics
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing cache metrics: read: %w", err)
	}
	return result, nil
}
