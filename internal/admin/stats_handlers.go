package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/stats"
)

var statsScalarQueryParameters = []string{
	"profile_id",
	"protocol",
	"model",
	"kind",
	"from",
	"to",
}

func (a *API) getStats(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, statsScalarQueryParameters...); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	parameters, err := statsScalarQueryValues(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}

	filter := stats.Filter{
		Protocol: parameters["protocol"],
		Model:    parameters["model"],
		Kind:     parameters["kind"],
	}
	if raw := parameters["profile_id"]; raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			a.writeRequestError(w, r, invalidRequestField("profile_id", "Must be a positive integer."))
			return
		}
		filter.ProfileID = &id
	}
	if raw := parameters["from"]; raw != "" {
		filter.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("from", "Must be an RFC 3339 timestamp."))
			return
		}
	}
	if raw := parameters["to"]; raw != "" {
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

	response, err := a.stats.Query(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	if response.ByDay == nil {
		response.ByDay = []stats.UsageRow{}
	}
	if response.ByModel == nil {
		response.ByModel = []stats.UsageRow{}
	}
	writeJSON(w, http.StatusOK, response)
}

func statsScalarQueryValues(r *http.Request) (map[string]string, error) {
	return scalarQueryValues(r, statsScalarQueryParameters)
}
