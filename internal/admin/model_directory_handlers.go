package admin

import (
	"encoding/json"
	"net/http"

	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
)

type modelMutationRequest struct {
	ExpectedRuntimeRevision int64           `json:"expected_runtime_revision"`
	ModelID                 string          `json:"model_id"`
	Capability              json.RawMessage `json:"capability"`
	Reason                  string          `json:"reason"`
}

func (a *API) listProfileModels(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.policies.Models(r.Context(), profileID)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (a *API) mutateProfileModel(kind runtimeconfig.ModelMutationKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profileID, err := profileIDFromPath(r)
		if err != nil {
			a.writeRequestError(w, r, err)
			return
		}
		var request modelMutationRequest
		if err := decodeJSON(w, r, &request); err != nil {
			a.writeRequestError(w, r, err)
			return
		}
		overview, err := a.policies.MutateModel(r.Context(), runtimeconfig.ModelMutationInput{
			ProfileID: profileID, ExpectedRevision: request.ExpectedRuntimeRevision,
			Kind: kind, ModelID: request.ModelID, CapabilityJSON: request.Capability,
			Reason: request.Reason,
		})
		if err != nil {
			a.writeDomainError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, overview)
	}
}

type offlineModelRequest struct {
	ExpectedRuntimeRevision int64             `json:"expected_runtime_revision"`
	ModelID                 string            `json:"model_id"`
	Replacements            map[string]string `json:"replacements"`
	VisionAction            string            `json:"vision_action"`
	Reason                  string            `json:"reason"`
}

func (a *API) offlineProfileModel(w http.ResponseWriter, r *http.Request) {
	profileID, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request offlineModelRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	overview, err := a.policies.EmergencyOffline(
		r.Context(), profileID, request.ExpectedRuntimeRevision,
		request.ModelID, request.Replacements, request.VisionAction, request.Reason,
	)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
