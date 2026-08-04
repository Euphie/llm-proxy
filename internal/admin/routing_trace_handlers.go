package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/evaluation"
	"github.com/Euphie/llm-proxy/internal/stats"
)

var modelPerformanceQueryParameters = []string{
	"profile_id", "model", "from", "to", "task_type", "difficulty",
	"risk", "vision", "page", "page_size",
}

const maxStatisticsPage = 1_000_000

func (a *API) getModelPerformance(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, modelPerformanceQueryParameters...); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	values, err := scalarQueryValues(r, modelPerformanceQueryParameters)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	filter := evaluation.PerformanceFilter{Page: 1, PageSize: 25}
	if raw := values["profile_id"]; raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			a.writeRequestError(w, r, invalidRequestField("profile_id", "Must be a positive integer."))
			return
		}
		filter.ProfileID = &id
	}
	for _, field := range []string{"model", "task_type"} {
		raw := values[field]
		if raw == "" {
			continue
		}
		if len(raw) > 128 {
			a.writeRequestError(w, r, invalidRequestField(field, "Must not exceed 128 characters."))
			return
		}
		if field == "model" {
			filter.Model = raw
		} else {
			filter.TaskType = raw
		}
	}
	if raw := values["from"]; raw != "" {
		filter.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("from", "Must be an RFC 3339 timestamp."))
			return
		}
	}
	if raw := values["to"]; raw != "" {
		filter.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("to", "Must be an RFC 3339 timestamp."))
			return
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		a.writeRequestError(w, r, invalidRequestField("from", "Must not be after to."))
		return
	}
	if raw := values["page"]; raw != "" {
		filter.Page, err = strconv.Atoi(raw)
		if err != nil || filter.Page < 1 || filter.Page > maxStatisticsPage {
			a.writeRequestError(w, r, invalidRequestField("page", "Must be between 1 and 1000000."))
			return
		}
	}
	if raw := values["page_size"]; raw != "" {
		filter.PageSize, err = strconv.Atoi(raw)
		if err != nil || (filter.PageSize != 25 && filter.PageSize != 50 && filter.PageSize != 100) {
			a.writeRequestError(w, r, invalidRequestField("page_size", "Must be 25, 50, or 100."))
			return
		}
	}
	if !setRoutingTraceEnum(values["difficulty"], []string{"easy", "medium", "hard", "unknown"}, &filter.Difficulty) {
		a.writeRequestError(w, r, invalidRequestField("difficulty", "Unsupported difficulty."))
		return
	}
	if !setRoutingTraceEnum(values["risk"], []string{"normal", "high"}, &filter.Risk) {
		a.writeRequestError(w, r, invalidRequestField("risk", "Unsupported risk."))
		return
	}
	if !setRoutingTraceEnum(values["vision"], []string{"none", "native", "composite"}, &filter.VisionMode) {
		a.writeRequestError(w, r, invalidRequestField("vision", "Unsupported vision mode."))
		return
	}
	page, err := a.strategies.ModelPerformance(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

var routingTraceQueryParameters = []string{
	"profile_id",
	"correlation_id",
	"from",
	"to",
	"page",
	"page_size",
	"category",
	"task_type",
	"difficulty",
	"risk",
	"model",
	"strategy",
	"route",
	"source",
	"status",
	"vision",
}

func (a *API) getRoutingTraces(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, routingTraceQueryParameters...); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	values, err := scalarQueryValues(r, routingTraceQueryParameters)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	filter := stats.RoutingTraceFilter{Page: 1, PageSize: 25}
	if raw := values["correlation_id"]; raw != "" {
		if len(raw) > 128 {
			a.writeRequestError(w, r, invalidRequestField("correlation_id", "Must not exceed 128 characters."))
			return
		}
		filter.CorrelationID = raw
	}
	if raw := values["profile_id"]; raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			a.writeRequestError(w, r, invalidRequestField("profile_id", "Must be a positive integer."))
			return
		}
		filter.ProfileID = &id
	}
	if raw := values["from"]; raw != "" {
		filter.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("from", "Must be an RFC 3339 timestamp."))
			return
		}
	}
	if raw := values["to"]; raw != "" {
		filter.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("to", "Must be an RFC 3339 timestamp."))
			return
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		a.writeRequestError(w, r, invalidRequestField("from", "Must not be after to."))
		return
	}
	if raw := values["page"]; raw != "" {
		filter.Page, err = strconv.Atoi(raw)
		if err != nil || filter.Page < 1 || filter.Page > maxStatisticsPage {
			a.writeRequestError(w, r, invalidRequestField("page", "Must be between 1 and 1000000."))
			return
		}
	}
	if raw := values["page_size"]; raw != "" {
		filter.PageSize, err = strconv.Atoi(raw)
		if err != nil || (filter.PageSize != 25 && filter.PageSize != 50 && filter.PageSize != 100) {
			a.writeRequestError(w, r, invalidRequestField("page_size", "Must be 25, 50, or 100."))
			return
		}
	}
	for _, field := range []string{"task_type", "model", "strategy", "route"} {
		if raw := values[field]; raw != "" {
			if len(raw) > 128 {
				a.writeRequestError(w, r, invalidRequestField(field, "Must not exceed 128 characters."))
				return
			}
			switch field {
			case "task_type":
				filter.TaskType = raw
			case "model":
				filter.Model = raw
			case "strategy":
				filter.Strategy = raw
			case "route":
				filter.Route = raw
			}
		}
	}
	if !setRoutingTraceEnum(values["category"], []string{"all", "normal", "high_risk", "fallback", "changed", "failed", "cost_anomaly"}, &filter.Category) {
		a.writeRequestError(w, r, invalidRequestField("category", "Unsupported category."))
		return
	}
	if filter.Category == "all" {
		filter.Category = ""
	}
	if !setRoutingTraceEnum(values["difficulty"], []string{"easy", "medium", "hard", "unknown"}, &filter.Difficulty) {
		a.writeRequestError(w, r, invalidRequestField("difficulty", "Unsupported difficulty."))
		return
	}
	if !setRoutingTraceEnum(values["risk"], []string{"normal", "high", "unknown"}, &filter.Risk) {
		a.writeRequestError(w, r, invalidRequestField("risk", "Unsupported risk."))
		return
	}
	if !setRoutingTraceEnum(values["source"], []string{"rule", "analyzer", "fallback", "session"}, &filter.Source) {
		a.writeRequestError(w, r, invalidRequestField("source", "Unsupported source."))
		return
	}
	if !setRoutingTraceEnum(values["status"], []string{"success", "failed"}, &filter.Status) {
		a.writeRequestError(w, r, invalidRequestField("status", "Unsupported status."))
		return
	}
	if !setRoutingTraceEnum(values["vision"], []string{"none", "used", "native", "composite"}, &filter.Vision) {
		a.writeRequestError(w, r, invalidRequestField("vision", "Unsupported vision mode."))
		return
	}

	page, err := a.stats.QueryRoutingTracePage(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func setRoutingTraceEnum(value string, allowed []string, target *string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			*target = value
			return true
		}
	}
	return false
}

func (a *API) getRoutingTraceDetail(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.writeRequestError(w, r, invalidRequestField("id", "Must be a positive integer."))
		return
	}
	detail, err := a.stats.QueryRoutingTraceDetail(r.Context(), id)
	if errors.Is(err, stats.ErrRoutingTraceNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Routing trace not found.", nil)
		return
	}
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

var routingCallQueryParameters = []string{"trace_id", "correlation_id", "limit"}

func (a *API) getRoutingCalls(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, routingCallQueryParameters...); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	values, err := scalarQueryValues(r, routingCallQueryParameters)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	filter := stats.RoutingCallFilter{Limit: 100}
	if raw := values["trace_id"]; raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			a.writeRequestError(w, r, invalidRequestField("trace_id", "Must be a positive integer."))
			return
		}
		filter.TraceID = &id
	}
	if raw := values["correlation_id"]; raw != "" {
		if len(raw) > 128 {
			a.writeRequestError(w, r, invalidRequestField("correlation_id", "Must not exceed 128 characters."))
			return
		}
		filter.CorrelationID = raw
	}
	if filter.TraceID == nil && filter.CorrelationID == "" {
		a.writeRequestError(w, r, invalidRequestField("trace_id", "Provide trace_id or correlation_id."))
		return
	}
	if raw := values["limit"]; raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil || filter.Limit < 1 || filter.Limit > 500 {
			a.writeRequestError(w, r, invalidRequestField("limit", "Must be between 1 and 500."))
			return
		}
	}
	rows, err := a.stats.QueryRoutingCalls(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func scalarQueryValues(
	r *http.Request,
	names []string,
) (map[string]string, error) {
	query := r.URL.Query()
	values := make(map[string]string, len(names))
	for _, name := range names {
		entries, present := query[name]
		if !present {
			continue
		}
		if len(entries) != 1 {
			return nil, invalidRequestField(name, "Must be supplied once.")
		}
		if entries[0] == "" {
			return nil, invalidRequestField(name, "Must not be empty.")
		}
		values[name] = entries[0]
	}
	return values, nil
}
