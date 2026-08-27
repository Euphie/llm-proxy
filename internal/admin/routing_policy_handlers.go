package admin

import (
	"net/http"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

func (a *API) getRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.policies.Overview(r.Context(), profileID)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (a *API) generateRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var intent strategycompiler.Intent
	if err := decodeJSON(w, r, &intent); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	preview, err := a.policies.Generate(r.Context(), profileID, intent)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

type applyRoutingPolicyRequest struct {
	ExpectedRuntimeRevision int64                       `json:"expected_runtime_revision"`
	Policy                  profile.RoutingPolicyConfig `json:"policy"`
	ChangeReason            string                      `json:"change_reason"`
}

func (a *API) applyRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request applyRoutingPolicyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.policies.Apply(
		r.Context(), profileID, request.ExpectedRuntimeRevision, request.Policy, request.ChangeReason,
	)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

type rollbackRoutingPolicyRequest struct {
	ExpectedRuntimeRevision int64  `json:"expected_runtime_revision"`
	VersionID               int64  `json:"version_id"`
	ChangeReason            string `json:"change_reason"`
}

func (a *API) rollbackRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request rollbackRoutingPolicyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.policies.Rollback(
		r.Context(), profileID, request.ExpectedRuntimeRevision,
		request.VersionID, request.ChangeReason,
	)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
