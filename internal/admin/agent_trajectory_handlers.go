package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/agenttrajectory"
)

var agentTrajectoryQueryParameters = []string{
	"profile_id", "model", "status", "source", "risk", "from", "to", "page", "page_size",
}

type agentTrajectoryPage struct {
	Items      []agenttrajectory.Record `json:"items"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	Total      int64                    `json:"total"`
	TotalPages int                      `json:"total_pages"`
	Summary    agenttrajectory.Summary  `json:"summary"`
}

type agentTrajectoryDetail struct {
	Record     agenttrajectory.Record    `json:"record"`
	Trajectory agenttrajectory.Plaintext `json:"trajectory"`
}

func (a *API) getAgentTrajectories(w http.ResponseWriter, r *http.Request) {
	if a.agentTrajectories == nil {
		a.writeInternalError(w, r, errors.New("agent trajectory store is unavailable"))
		return
	}
	filter, page, pageSize, err := parseAgentTrajectoryFilter(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	result, err := a.agentTrajectories.List(r.Context(), filter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	summaryFilter := filter
	summaryFilter.Limit = 0
	summaryFilter.Offset = 0
	summary, err := a.agentTrajectories.Summary(r.Context(), summaryFilter)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	totalPages := 0
	if result.Total > 0 {
		totalPages = int((result.Total + int64(pageSize) - 1) / int64(pageSize))
	}
	writeJSON(w, http.StatusOK, agentTrajectoryPage{
		Items: result.Items, Page: page, PageSize: pageSize, Total: result.Total,
		TotalPages: totalPages, Summary: summary,
	})
}

func (a *API) getAgentTrajectory(w http.ResponseWriter, r *http.Request) {
	if a.agentTrajectories == nil || a.agentTrajectoryCipher == nil {
		a.writeInternalError(w, r, errors.New("agent trajectory audit is unavailable"))
		return
	}
	id, ok := parseAgentTrajectoryID(w, r)
	if !ok {
		return
	}
	record, err := a.agentTrajectories.Get(r.Context(), id)
	if errors.Is(err, agenttrajectory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent_trajectory_not_found", "Agent trajectory not found.", nil)
		return
	}
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	plaintext, err := a.agentTrajectoryCipher.Open(record.Payload)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "agent_trajectory_decrypt_failed", "Agent trajectory cannot be decrypted, but it can still be deleted.", nil)
		return
	}
	writeJSON(w, http.StatusOK, agentTrajectoryDetail{Record: record, Trajectory: plaintext})
}

func (a *API) deleteAgentTrajectory(w http.ResponseWriter, r *http.Request) {
	if a.agentTrajectories == nil {
		a.writeInternalError(w, r, errors.New("agent trajectory store is unavailable"))
		return
	}
	id, ok := parseAgentTrajectoryID(w, r)
	if !ok {
		return
	}
	if err := a.agentTrajectories.Delete(r.Context(), id); err != nil {
		if errors.Is(err, agenttrajectory.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_trajectory_not_found", "Agent trajectory not found.", nil)
			return
		}
		a.writeInternalError(w, r, err)
		return
	}
	writeOK(w)
}

func parseAgentTrajectoryFilter(r *http.Request) (agenttrajectory.ListFilter, int, int, error) {
	if err := rejectUnknownQuery(r, agentTrajectoryQueryParameters...); err != nil {
		return agenttrajectory.ListFilter{}, 0, 0, err
	}
	values, err := scalarQueryValues(r, agentTrajectoryQueryParameters)
	if err != nil {
		return agenttrajectory.ListFilter{}, 0, 0, err
	}
	page, pageSize := 1, 25
	filter := agenttrajectory.ListFilter{}
	if raw := values["profile_id"]; raw != "" {
		filter.ProfileID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || filter.ProfileID <= 0 {
			return filter, 0, 0, invalidRequestField("profile_id", "Must be a positive integer.")
		}
	}
	if raw := values["model"]; raw != "" {
		if len(raw) > 128 {
			return filter, 0, 0, invalidRequestField("model", "Must not exceed 128 characters.")
		}
		filter.Model = raw
	}
	if raw := values["status"]; raw != "" {
		filter.Status = agenttrajectory.Status(raw)
		if !validAgentTrajectoryStatus(filter.Status) {
			return filter, 0, 0, invalidRequestField("status", "Unsupported trajectory status.")
		}
	}
	if raw := values["source"]; raw != "" {
		filter.Source = agenttrajectory.Source(raw)
		if filter.Source != agenttrajectory.SourceSession && filter.Source != agenttrajectory.SourceRequestSnapshot {
			return filter, 0, 0, invalidRequestField("source", "Unsupported trajectory source.")
		}
	}
	if raw := values["risk"]; raw != "" {
		if raw != "normal" && raw != "high" && raw != "unknown" {
			return filter, 0, 0, invalidRequestField("risk", "Unsupported risk.")
		}
		filter.Risk = raw
	}
	if raw := values["from"]; raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return filter, 0, 0, invalidRequestField("from", "Must be an RFC 3339 timestamp.")
		}
		filter.From = &value
	}
	if raw := values["to"]; raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return filter, 0, 0, invalidRequestField("to", "Must be an RFC 3339 timestamp.")
		}
		filter.To = &value
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return filter, 0, 0, invalidRequestField("from", "Must not be after to.")
	}
	if raw := values["page"]; raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > maxStatisticsPage {
			return filter, 0, 0, invalidRequestField("page", "Must be between 1 and 1000000.")
		}
	}
	if raw := values["page_size"]; raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || (pageSize != 25 && pageSize != 50 && pageSize != 100) {
			return filter, 0, 0, invalidRequestField("page_size", "Must be 25, 50, or 100.")
		}
	}
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize
	return filter, page, pageSize, nil
}

func parseAgentTrajectoryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "agent_trajectory_not_found", "Agent trajectory not found.", nil)
		return 0, false
	}
	return id, true
}

func validAgentTrajectoryStatus(status agenttrajectory.Status) bool {
	switch status {
	case agenttrajectory.StatusCollecting, agenttrajectory.StatusCompleted,
		agenttrajectory.StatusQueued, agenttrajectory.StatusEvaluating,
		agenttrajectory.StatusEvaluated, agenttrajectory.StatusTimedOut,
		agenttrajectory.StatusInterrupted, agenttrajectory.StatusSkipped,
		agenttrajectory.StatusFailed:
		return true
	default:
		return false
	}
}
