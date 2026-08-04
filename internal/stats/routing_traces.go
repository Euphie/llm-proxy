package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RoutingTraceFilter struct {
	ProfileID     *int64
	CorrelationID string
	From          time.Time
	To            time.Time
	Limit         int
	Page          int
	PageSize      int
	Category      string
	TaskType      string
	Difficulty    string
	Risk          string
	Model         string
	Strategy      string
	Route         string
	Source        string
	Status        string
	Vision        string
}

type RoutingTraceCategoryCounts struct {
	All         int `json:"all"`
	Normal      int `json:"normal"`
	HighRisk    int `json:"high_risk"`
	Fallback    int `json:"fallback"`
	Changed     int `json:"changed"`
	Failed      int `json:"failed"`
	CostAnomaly int `json:"cost_anomaly"`
}

type RoutingTracePage struct {
	Items          []RoutingTraceRow          `json:"items"`
	Page           int                        `json:"page"`
	PageSize       int                        `json:"page_size"`
	Total          int                        `json:"total"`
	TotalPages     int                        `json:"total_pages"`
	CategoryCounts RoutingTraceCategoryCounts `json:"category_counts"`
}

type RoutingCandidateDecisionRow struct {
	Ordinal                 int      `json:"ordinal"`
	Model                   string   `json:"model"`
	Decision                string   `json:"decision"`
	ReasonCode              string   `json:"reason_code"`
	QualityScoreBPS         int      `json:"quality_score_bps"`
	SevereErrorRateBPS      int      `json:"severe_error_rate_bps"`
	ExpectedCostMicroUSD    int64    `json:"expected_cost_micro_usd"`
	AnswerWorstCostMicroUSD int64    `json:"answer_worst_cost_micro_usd"`
	VisionCallCostMicroUSD  int64    `json:"vision_call_cost_micro_usd"`
	VisionMode              string   `json:"vision_mode"`
	UpstreamNodes           []string `json:"upstream_nodes"`
}

type RoutingTraceDetail struct {
	Trace      RoutingTraceRow               `json:"trace"`
	Candidates []RoutingCandidateDecisionRow `json:"candidates"`
	Calls      []RoutingCallRow              `json:"calls"`
}

var ErrRoutingTraceNotFound = errors.New("routing trace not found")

const maxRoutingTracePage = 1_000_000

type RoutingTraceRow struct {
	ID                            int64     `json:"id"`
	CreatedAt                     time.Time `json:"created_at"`
	CorrelationID                 string    `json:"correlation_id"`
	ProfileID                     *int64    `json:"profile_id,omitempty"`
	ProfileSlug                   string    `json:"profile_slug"`
	Protocol                      string    `json:"protocol"`
	Path                          string    `json:"path"`
	Strategy                      string    `json:"strategy"`
	Route                         string    `json:"route"`
	TaskType                      string    `json:"task_type"`
	Difficulty                    string    `json:"difficulty"`
	Risk                          string    `json:"risk"`
	ClassificationSource          string    `json:"classification_source"`
	ClassificationConfidenceBPS   int       `json:"classification_confidence_bps"`
	ClassificationReasonCodes     []string  `json:"classification_reason_codes"`
	EstimatedInputTokens          int       `json:"estimated_input_tokens"`
	RequestedOutputTokens         int       `json:"requested_output_tokens"`
	DecisionReason                string    `json:"decision_reason"`
	InitialModel                  string    `json:"initial_model"`
	FinalModel                    string    `json:"final_model"`
	InitialTarget                 string    `json:"initial_target"`
	FinalTarget                   string    `json:"final_target"`
	VisionMode                    string    `json:"vision_mode"`
	StatusCode                    int       `json:"status_code"`
	ClientCommitted               bool      `json:"client_committed"`
	AnswerAttempts                int       `json:"answer_attempts"`
	AuxiliaryCalls                int       `json:"auxiliary_calls"`
	TotalOutboundCalls            int       `json:"total_outbound_calls"`
	ModelSwitches                 int       `json:"model_switches"`
	SelfEscalations               int       `json:"self_escalations"`
	SelfEscalationReason          string    `json:"self_escalation_reason"`
	TargetSwitches                int       `json:"target_switches"`
	PlannedWorstCaseCostMicroUSD  int64     `json:"planned_worst_case_cost_micro_usd"`
	ConsumedEstimatedCostMicroUSD int64     `json:"consumed_estimated_cost_micro_usd"`
	HeldCostMicroUSD              int64     `json:"held_cost_micro_usd"`
	KnownActualCostMicroUSD       int64     `json:"known_actual_cost_micro_usd"`
	AllActualCostsKnown           bool      `json:"all_actual_costs_known"`
	ElapsedMilliseconds           int64     `json:"elapsed_ms"`
}

func (s *DB) QueryRoutingTraces(
	ctx context.Context,
	filter RoutingTraceFilter,
) ([]RoutingTraceRow, error) {
	if filter.PageSize == 0 {
		filter.PageSize = filter.Limit
	}
	if filter.PageSize == 0 {
		filter.PageSize = 100
	}
	filter.Page = 1
	page, err := s.QueryRoutingTracePage(ctx, filter)
	return page.Items, err
}

func (s *DB) QueryRoutingTracePage(
	ctx context.Context,
	filter RoutingTraceFilter,
) (RoutingTracePage, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Page > maxRoutingTracePage {
		return RoutingTracePage{}, fmt.Errorf("routing traces: page must not exceed %d", maxRoutingTracePage)
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 25
	}
	if filter.PageSize > 500 {
		return RoutingTracePage{}, fmt.Errorf("routing traces: page size must not exceed 500")
	}
	baseWhere, baseArgs := routingTracePredicates(filter, false)
	where, args := routingTracePredicates(filter, true)
	tx, err := s.beginQueryTx(ctx)
	if err != nil {
		return RoutingTracePage{}, fmt.Errorf("routing traces: begin query snapshot: %w", err)
	}
	defer tx.Rollback()
	page := RoutingTracePage{Page: filter.Page, PageSize: filter.PageSize, Items: []RoutingTraceRow{}}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM routing_traces`+where, args...).Scan(&page.Total); err != nil {
		return RoutingTracePage{}, fmt.Errorf("routing traces: count: %w", err)
	}
	if page.Total > 0 {
		page.TotalPages = (page.Total + filter.PageSize - 1) / filter.PageSize
	}
	if err := queryRoutingTraceCategoryCounts(ctx, tx, baseWhere, baseArgs, &page.CategoryCounts); err != nil {
		return RoutingTracePage{}, err
	}
	queryArgs := append(append([]any(nil), args...), filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := tx.QueryContext(ctx, routingTraceSelect+where+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return RoutingTracePage{}, fmt.Errorf("routing traces: query: %w", err)
	}
	for rows.Next() {
		row, scanErr := scanRoutingTrace(rows)
		if scanErr != nil {
			rows.Close()
			return RoutingTracePage{}, scanErr
		}
		page.Items = append(page.Items, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RoutingTracePage{}, fmt.Errorf("routing traces: read: %w", err)
	}
	if err := rows.Close(); err != nil {
		return RoutingTracePage{}, fmt.Errorf("routing traces: close rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RoutingTracePage{}, fmt.Errorf("routing traces: commit query snapshot: %w", err)
	}
	return page, nil
}

const routingTraceSelect = `
	SELECT id, created_at, correlation_id, profile_id, profile_slug, protocol, path,
	       strategy_name, route_id, task_type, difficulty, risk, classification_source,
	       classification_confidence_bps, classification_reason_codes,
	       estimated_input_tokens, requested_output_tokens, decision_reason,
	       initial_model, final_model, initial_target, final_target,
	       vision_mode, status_code, client_committed, answer_attempts, auxiliary_calls,
	       total_outbound_calls, model_switches, target_switches,
	       self_escalations, self_escalation_reason,
	       planned_worst_case_cost_micro_usd, consumed_estimated_cost_micro_usd,
	       held_cost_micro_usd, known_actual_cost_micro_usd,
	       all_actual_costs_known, elapsed_ms
	FROM routing_traces`

func routingTracePredicates(filter RoutingTraceFilter, includeCategory bool) (string, []any) {
	predicates := make([]string, 0, 12)
	args := make([]any, 0, 12)
	add := func(predicate string, value any) {
		predicates = append(predicates, predicate)
		args = append(args, value)
	}
	if filter.ProfileID != nil {
		add("profile_id = ?", *filter.ProfileID)
	}
	if filter.CorrelationID != "" {
		add("correlation_id = ?", filter.CorrelationID)
	}
	if !filter.From.IsZero() {
		add("created_at >= ?", filter.From.UTC().Format(usageTimeFormat))
	}
	if !filter.To.IsZero() {
		add("created_at <= ?", filter.To.UTC().Format(usageTimeFormat))
	}
	for _, item := range []struct{ value, column string }{
		{filter.TaskType, "task_type"}, {filter.Difficulty, "difficulty"},
		{filter.Risk, "risk"}, {filter.Strategy, "strategy_name"},
		{filter.Route, "route_id"}, {filter.Source, "classification_source"},
	} {
		if item.value != "" {
			add(item.column+" = ?", item.value)
		}
	}
	if filter.Model != "" {
		predicates = append(predicates, "(initial_model = ? OR final_model = ?)")
		args = append(args, filter.Model, filter.Model)
	}
	if filter.Status == "success" {
		predicates = append(predicates, "status_code BETWEEN 200 AND 299 AND client_committed = 1")
	} else if filter.Status == "failed" {
		predicates = append(predicates, "(status_code NOT BETWEEN 200 AND 299 OR client_committed = 0)")
	}
	if filter.Vision == "none" {
		predicates = append(predicates, "vision_mode = 'none'")
	} else if filter.Vision == "used" {
		predicates = append(predicates, "vision_mode <> 'none'")
	} else if filter.Vision != "" {
		add("vision_mode = ?", filter.Vision)
	}
	if includeCategory {
		if category := routingCategoryPredicate(filter.Category); category != "" {
			predicates = append(predicates, category)
		}
	}
	if len(predicates) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(predicates, " AND "), args
}

func routingCategoryPredicate(category string) string {
	switch category {
	case "normal":
		return "risk = 'normal' AND status_code BETWEEN 200 AND 299 AND client_committed = 1 AND self_escalations = 0 AND model_switches = 0 AND target_switches = 0"
	case "high_risk":
		return "risk = 'high' AND classification_source <> 'fallback'"
	case "fallback":
		return "classification_source = 'fallback'"
	case "changed":
		return "(self_escalations > 0 OR model_switches > 0 OR target_switches > 0)"
	case "failed":
		return "(status_code NOT BETWEEN 200 AND 299 OR client_committed = 0)"
	case "cost_anomaly":
		return "(known_actual_cost_micro_usd > planned_worst_case_cost_micro_usd OR consumed_estimated_cost_micro_usd > planned_worst_case_cost_micro_usd)"
	default:
		return ""
	}
}

func queryRoutingTraceCategoryCounts(
	ctx context.Context,
	tx queryTx,
	where string,
	args []any,
	counts *RoutingTraceCategoryCounts,
) error {
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("normal")+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("high_risk")+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("fallback")+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("changed")+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("failed")+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+routingCategoryPredicate("cost_anomaly")+` THEN 1 ELSE 0 END), 0)
		FROM routing_traces`+where, args...).Scan(
		&counts.All, &counts.Normal, &counts.HighRisk, &counts.Fallback, &counts.Changed,
		&counts.Failed, &counts.CostAnomaly,
	); err != nil {
		return fmt.Errorf("routing traces: category counts: %w", err)
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanRoutingTrace(scanner rowScanner) (RoutingTraceRow, error) {
	var row RoutingTraceRow
	var createdAt, reasonCodes string
	var profileID sql.NullInt64
	var committed, allActualKnown int
	if err := scanner.Scan(
		&row.ID, &createdAt, &row.CorrelationID, &profileID, &row.ProfileSlug, &row.Protocol,
		&row.Path, &row.Strategy, &row.Route, &row.TaskType, &row.Difficulty, &row.Risk,
		&row.ClassificationSource, &row.ClassificationConfidenceBPS, &reasonCodes,
		&row.EstimatedInputTokens, &row.RequestedOutputTokens, &row.DecisionReason,
		&row.InitialModel, &row.FinalModel, &row.InitialTarget, &row.FinalTarget,
		&row.VisionMode, &row.StatusCode, &committed, &row.AnswerAttempts,
		&row.AuxiliaryCalls, &row.TotalOutboundCalls, &row.ModelSwitches,
		&row.TargetSwitches, &row.SelfEscalations, &row.SelfEscalationReason,
		&row.PlannedWorstCaseCostMicroUSD, &row.ConsumedEstimatedCostMicroUSD,
		&row.HeldCostMicroUSD, &row.KnownActualCostMicroUSD,
		&allActualKnown, &row.ElapsedMilliseconds,
	); err != nil {
		return RoutingTraceRow{}, fmt.Errorf("routing traces: scan: %w", err)
	}
	parsed, err := time.Parse(usageTimeFormat, createdAt)
	if err != nil {
		return RoutingTraceRow{}, fmt.Errorf("routing traces: parse created_at: %w", err)
	}
	row.CreatedAt = parsed
	if profileID.Valid {
		id := profileID.Int64
		row.ProfileID = &id
	}
	row.ClientCommitted = committed == 1
	row.AllActualCostsKnown = allActualKnown == 1
	if err := json.Unmarshal([]byte(reasonCodes), &row.ClassificationReasonCodes); err != nil {
		return RoutingTraceRow{}, fmt.Errorf("routing traces: parse classification reasons: %w", err)
	}
	return row, nil
}

func (s *DB) QueryRoutingTraceDetail(
	ctx context.Context,
	traceID int64,
) (RoutingTraceDetail, error) {
	if traceID <= 0 {
		return RoutingTraceDetail{}, ErrRoutingTraceNotFound
	}
	trace, err := scanRoutingTrace(s.db.QueryRowContext(
		ctx, routingTraceSelect+" WHERE id = ?", traceID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return RoutingTraceDetail{}, ErrRoutingTraceNotFound
	}
	if err != nil {
		return RoutingTraceDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ordinal, model, decision, reason_code, quality_score_bps,
		       severe_error_rate_bps, expected_cost_micro_usd,
		       answer_worst_cost_micro_usd, vision_call_cost_micro_usd,
		       vision_mode, upstream_nodes
		FROM routing_candidate_decisions
		WHERE trace_id = ?
		ORDER BY ordinal`, traceID)
	if err != nil {
		return RoutingTraceDetail{}, fmt.Errorf("routing trace candidates: query: %w", err)
	}
	candidates := []RoutingCandidateDecisionRow{}
	for rows.Next() {
		var candidate RoutingCandidateDecisionRow
		var nodes string
		if err := rows.Scan(
			&candidate.Ordinal, &candidate.Model, &candidate.Decision,
			&candidate.ReasonCode, &candidate.QualityScoreBPS,
			&candidate.SevereErrorRateBPS, &candidate.ExpectedCostMicroUSD,
			&candidate.AnswerWorstCostMicroUSD, &candidate.VisionCallCostMicroUSD,
			&candidate.VisionMode, &nodes,
		); err != nil {
			rows.Close()
			return RoutingTraceDetail{}, fmt.Errorf("routing trace candidates: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(nodes), &candidate.UpstreamNodes); err != nil {
			rows.Close()
			return RoutingTraceDetail{}, fmt.Errorf("routing trace candidates: parse nodes: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RoutingTraceDetail{}, fmt.Errorf("routing trace candidates: read: %w", err)
	}
	if err := rows.Close(); err != nil {
		return RoutingTraceDetail{}, fmt.Errorf("routing trace candidates: close: %w", err)
	}
	calls, err := s.QueryRoutingCalls(ctx, RoutingCallFilter{TraceID: &traceID, Limit: 500})
	if err != nil {
		return RoutingTraceDetail{}, err
	}
	return RoutingTraceDetail{Trace: trace, Candidates: candidates, Calls: calls}, nil
}
