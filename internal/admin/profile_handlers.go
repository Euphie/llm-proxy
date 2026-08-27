package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/stats"
)

type profileResponse struct {
	ID          int64                `json:"id"`
	Slug        string               `json:"slug"`
	DisplayName string               `json:"display_name"`
	Enabled     bool                 `json:"enabled"`
	Config      profile.Config       `json:"config"`
	Usage30D    stats.ProfileSummary `json:"usage_30d"`
}

type saveProfileRequest struct {
	Slug                    string         `json:"slug"`
	DisplayName             string         `json:"display_name"`
	Enabled                 bool           `json:"enabled"`
	Config                  profile.Config `json:"config"`
	MakeDefault             bool           `json:"make_default"`
	ExpectedRuntimeRevision int64          `json:"expected_runtime_revision,omitempty"`
}

type copyProfileRequest struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type defaultProfileRequest struct {
	ProfileID int64 `json:"profile_id"`
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	records, defaultProfileID, err := a.profiles.List(r.Context())
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	summaries, err := a.stats.ProfileSummaries(r.Context(), a.now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	response := struct {
		DefaultProfileID int64             `json:"default_profile_id"`
		Profiles         []profileResponse `json:"profiles"`
	}{
		DefaultProfileID: defaultProfileID,
		Profiles:         make([]profileResponse, 0, len(records)),
	}
	for _, record := range records {
		item := responseFromProfile(record)
		item.Usage30D = summaries[record.ID]
		response.Profiles = append(response.Profiles, item)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) createProfile(w http.ResponseWriter, r *http.Request) {
	var request saveProfileRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	record, err := a.profiles.CreateRuntime(r.Context(), profile.SaveInput{
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Config:      request.Config,
	}, request.MakeDefault)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, responseFromProfile(record))
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	id, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	record, err := a.profiles.Get(r.Context(), id)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, responseFromProfile(record))
}

func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request saveProfileRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	input := profile.SaveInput{
		ID:          id,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Config:      request.Config,
	}
	var record profile.Record
	if request.Config.Version == 2 {
		record, err = a.profiles.SaveRuntime(
			r.Context(), input, request.MakeDefault, request.ExpectedRuntimeRevision,
		)
	} else {
		record, err = a.profiles.Save(r.Context(), input, request.MakeDefault)
	}
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, responseFromProfile(record))
}

func (a *API) copyProfile(w http.ResponseWriter, r *http.Request) {
	id, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request copyProfileRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	record, err := a.profiles.Copy(r.Context(), id, request.Slug, request.DisplayName)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, responseFromProfile(record))
}

func (a *API) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, err := profileIDFromPath(r)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request struct{}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	replacementID, err := optionalPositiveInt64Query(r, "replacement_default_id")
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	if err := rejectUnknownQuery(r, "replacement_default_id"); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	if err := a.profiles.Delete(r.Context(), id, replacementID); err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeOK(w)
}

func (a *API) setDefaultProfile(w http.ResponseWriter, r *http.Request) {
	var request defaultProfileRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	if request.ProfileID <= 0 {
		a.writeRequestError(w, r, invalidRequestField("profile_id", "Must be a positive integer."))
		return
	}
	if err := a.profiles.SetDefault(r.Context(), request.ProfileID); err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DefaultProfileID int64 `json:"default_profile_id"`
	}{DefaultProfileID: request.ProfileID})
}

func responseFromProfile(record profile.Record) profileResponse {
	return profileResponse{
		ID:          record.ID,
		Slug:        record.Slug,
		DisplayName: record.DisplayName,
		Enabled:     record.Enabled,
		Config:      record.Config,
	}
}

func profileIDFromPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidRequestField("id", "Profile ID must be a positive integer.")
	}
	return id, nil
}

func optionalPositiveInt64Query(r *http.Request, name string) (int64, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return 0, nil
	}
	if len(values) != 1 {
		return 0, invalidRequestField(name, "Must be supplied once.")
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value <= 0 {
		return 0, invalidRequestField(name, "Must be a positive integer.")
	}
	return value, nil
}

func invalidRequestField(field, message string) error {
	return &requestError{
		status:  http.StatusBadRequest,
		code:    "invalid_request",
		message: "Request validation failed.",
		fields:  map[string]string{field: message},
	}
}

func rejectUnknownQuery(r *http.Request, allowed ...string) error {
	allow := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allow[name] = struct{}{}
	}
	for name := range r.URL.Query() {
		if _, ok := allow[name]; !ok {
			return invalidRequestField(name, fmt.Sprintf("Unsupported query parameter %q.", name))
		}
	}
	return nil
}
