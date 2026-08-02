package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/stats"
)

var routingTraceQueryParameters = []string{
	"profile_id",
	"correlation_id",
	"from",
	"to",
	"limit",
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
	filter := stats.RoutingTraceFilter{Limit: 100}
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
	if raw := values["limit"]; raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil || filter.Limit < 1 || filter.Limit > 500 {
			a.writeRequestError(w, r, invalidRequestField("limit", "Must be between 1 and 500."))
			return
		}
	}

	rows, err := a.stats.QueryRoutingTraces(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
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
