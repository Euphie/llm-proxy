package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategy"
)

type strategyConfigRequest struct {
	Config profile.RoutingStrategyConfig `json:"config"`
}

type strategyRevisionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (a *API) listStrategies(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.strategies.Overview(r.Context(), profileID)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (a *API) createStrategy(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request strategyConfigRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	version, err := a.strategies.CreateDraft(r.Context(), profileID, request.Config)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, version)
}

func (a *API) generateStrategyCandidate(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	version, err := a.strategies.GenerateCandidate(r.Context(), profileID)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, version)
}

func (a *API) updateStrategy(w http.ResponseWriter, r *http.Request) {
	profileID, strategyID, err := strategyPathIDs(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request strategyConfigRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	version, err := a.strategies.UpdateDraft(r.Context(), profileID, strategyID, request.Config)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, version)
}

func (a *API) advanceStrategy(w http.ResponseWriter, r *http.Request) {
	profileID, strategyID, err := strategyPathIDs(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request struct {
		From strategy.State `json:"from"`
		To   strategy.State `json:"to"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	version, err := a.strategies.Advance(r.Context(), profileID, strategyID, request.From, request.To)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, version)
}

func (a *API) startStrategyCanary(w http.ResponseWriter, r *http.Request) {
	profileID, strategyID, err := strategyPathIDs(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request struct {
		ExpectedRevision int64 `json:"expected_revision"`
		CanaryBPS        int   `json:"canary_bps"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	snapshot, err := a.strategies.StartCanary(
		r.Context(), profileID, strategyID, request.CanaryBPS, request.ExpectedRevision,
	)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *API) cancelStrategyCanary(w http.ResponseWriter, r *http.Request) {
	a.mutateStrategyPointer(w, r, a.strategies.CancelCanary)
}

func (a *API) promoteStrategy(w http.ResponseWriter, r *http.Request) {
	a.mutateStrategyPointer(w, r, a.strategies.Promote)
}

func (a *API) rollbackStrategy(w http.ResponseWriter, r *http.Request) {
	a.mutateStrategyPointer(w, r, a.strategies.Rollback)
}

func (a *API) mutateStrategyPointer(
	w http.ResponseWriter,
	r *http.Request,
	mutate func(context.Context, int64, int64) (strategy.Snapshot, error),
) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request strategyRevisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	snapshot, err := mutate(r.Context(), profileID, request.ExpectedRevision)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func strategyPathIDs(r *http.Request) (int64, int64, error) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		return 0, 0, err
	}
	strategyID, err := strconv.ParseInt(r.PathValue("strategy_id"), 10, 64)
	if err != nil || strategyID <= 0 {
		return 0, 0, invalidRequestField("strategy_id", "Strategy ID must be a positive integer.")
	}
	return profileID, strategyID, nil
}
