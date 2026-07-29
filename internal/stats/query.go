package stats

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Filter struct {
	ProfileID *int64
	Protocol  string
	Model     string
	Kind      string
	From      time.Time
	To        time.Time
}

type UsageRow struct {
	Key                 string `json:"key"`
	Requests            int    `json:"requests"`
	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	TotalTokens         int    `json:"total_tokens"`
}

type Response struct {
	Summary UsageRow   `json:"summary"`
	ByDay   []UsageRow `json:"by_day"`
	ByModel []UsageRow `json:"by_model"`
}

type ProfileSummary struct {
	Requests     int `json:"requests"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (s *DB) Query(ctx context.Context, filter Filter) (Response, error) {
	tx, err := s.beginQueryTx(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("stats: begin query snapshot: %w", err)
	}
	defer tx.Rollback()

	resp, err := queryResponse(ctx, tx, filter)
	if err != nil {
		return Response{}, err
	}
	if err := tx.Commit(); err != nil {
		return Response{}, fmt.Errorf("stats: commit query snapshot: %w", err)
	}
	return resp, nil
}

type queryTx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	Commit() error
	Rollback() error
}

func queryResponse(ctx context.Context, tx queryTx, filter Filter) (Response, error) {
	where, args := filterPredicates(filter)
	resp := Response{}

	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0)
		FROM usage`+where, args...).Scan(
		&resp.Summary.Requests,
		&resp.Summary.InputTokens,
		&resp.Summary.OutputTokens,
		&resp.Summary.CacheReadTokens,
		&resp.Summary.CacheCreationTokens,
	); err != nil {
		return Response{}, fmt.Errorf("stats: summary query: %w", err)
	}
	resp.Summary.Key = "total"
	resp.Summary.TotalTokens = resp.Summary.InputTokens + resp.Summary.OutputTokens

	byDayWhere := where
	if filter.From.IsZero() && filter.To.IsZero() {
		if byDayWhere == "" {
			byDayWhere = "\nWHERE created_at >= date('now', '-30 days')"
		} else {
			byDayWhere += " AND created_at >= date('now', '-30 days')"
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT date(created_at) AS d,
		       COUNT(*) AS requests,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0)
		FROM usage`+byDayWhere+`
		GROUP BY d
		ORDER BY d DESC`, args...)
	if err != nil {
		return Response{}, fmt.Errorf("stats: by-day query: %w", err)
	}
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(
			&row.Key,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
		); err != nil {
			rows.Close()
			return Response{}, fmt.Errorf("stats: scan by-day row: %w", err)
		}
		row.TotalTokens = row.InputTokens + row.OutputTokens
		resp.ByDay = append(resp.ByDay, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Response{}, fmt.Errorf("stats: read by-day rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Response{}, fmt.Errorf("stats: close by-day rows: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(LOWER(model), ''), '(unknown)') AS m,
		       COUNT(*) AS requests,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0)
		FROM usage`+where+`
		GROUP BY m
		ORDER BY SUM(input_tokens + output_tokens) DESC`, args...)
	if err != nil {
		return Response{}, fmt.Errorf("stats: by-model query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(
			&row.Key,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
		); err != nil {
			return Response{}, fmt.Errorf("stats: scan by-model row: %w", err)
		}
		row.TotalTokens = row.InputTokens + row.OutputTokens
		resp.ByModel = append(resp.ByModel, row)
	}
	if err := rows.Err(); err != nil {
		return Response{}, fmt.Errorf("stats: read by-model rows: %w", err)
	}

	return resp, nil
}

func (s *DB) ProfileSummaries(
	ctx context.Context,
	since time.Time,
) (map[int64]ProfileSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT profile_id,
		       COUNT(*),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0)
		FROM usage
		WHERE profile_id IS NOT NULL AND created_at >= ?
		GROUP BY profile_id
	`, since.UTC().Format(usageTimeFormat))
	if err != nil {
		return nil, fmt.Errorf("stats: profile summaries query: %w", err)
	}
	defer rows.Close()

	summaries := make(map[int64]ProfileSummary)
	for rows.Next() {
		var profileID int64
		var summary ProfileSummary
		if err := rows.Scan(
			&profileID,
			&summary.Requests,
			&summary.InputTokens,
			&summary.OutputTokens,
		); err != nil {
			return nil, fmt.Errorf("stats: scan profile summary: %w", err)
		}
		summaries[profileID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read profile summaries: %w", err)
	}
	return summaries, nil
}

func filterPredicates(filter Filter) (string, []any) {
	var predicates []string
	var args []any
	if filter.ProfileID != nil {
		predicates = append(predicates, "profile_id = ?")
		args = append(args, *filter.ProfileID)
	}
	if filter.Protocol != "" {
		predicates = append(predicates, "protocol = ?")
		args = append(args, filter.Protocol)
	}
	if filter.Model != "" {
		predicates = append(predicates, "LOWER(model) = LOWER(?)")
		args = append(args, filter.Model)
	}
	if filter.Kind != "" {
		predicates = append(predicates, "request_kind = ?")
		args = append(args, filter.Kind)
	}
	if !filter.From.IsZero() {
		predicates = append(predicates, "created_at >= ?")
		args = append(args, filter.From.UTC().Format(usageTimeFormat))
	}
	if !filter.To.IsZero() {
		predicates = append(predicates, "created_at <= ?")
		args = append(args, filter.To.UTC().Format(usageTimeFormat))
	}
	if len(predicates) == 0 {
		return "", nil
	}
	return "\nWHERE " + strings.Join(predicates, " AND "), args
}
