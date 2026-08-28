package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Euphie/llm-proxy/internal/aggregate"
	"github.com/Euphie/llm-proxy/internal/profile"
)

type saveProviderAccountRequest struct {
	Slug        string           `json:"slug"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Protocol    profile.Protocol `json:"protocol"`
	Upstream    string           `json:"upstream"`
	AuthHeader  string           `json:"auth_header"`
	Secret      string           `json:"secret"`
	Models      []struct {
		ModelID     string `json:"model_id"`
		DisplayName string `json:"display_name"`
		Enabled     bool   `json:"enabled"`
	} `json:"models"`
}

type saveAggregateGatewayRequest struct {
	Slug        string           `json:"slug"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Protocol    profile.Protocol `json:"protocol"`
	Routes      []struct {
		PublicModel       string `json:"public_model"`
		ProviderAccountID int64  `json:"provider_account_id"`
		ProviderModel     string `json:"provider_model"`
		Enabled           bool   `json:"enabled"`
	} `json:"routes"`
}

type createAggregateKeyRequest struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	ExpiresAt string `json:"expires_at"`
}

func (a *API) listProviderAccounts(w http.ResponseWriter, r *http.Request) {
	providers, err := a.aggregate.ListProviders(r.Context())
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	response := struct {
		Providers []aggregate.ProviderAccountResponse `json:"providers"`
	}{Providers: make([]aggregate.ProviderAccountResponse, 0, len(providers))}
	for _, provider := range providers {
		response.Providers = append(response.Providers, aggregate.ProviderResponse(provider))
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) createProviderAccount(w http.ResponseWriter, r *http.Request) {
	var request saveProviderAccountRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	record, err := a.aggregate.SaveProvider(r.Context(), aggregate.ProviderAccountInput{
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Protocol:    request.Protocol,
		Upstream:    request.Upstream,
		AuthHeader:  request.AuthHeader,
		Secret:      request.Secret,
		Models:      providerModelInputs(request.Models),
	})
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, aggregate.ProviderResponse(record))
}

func (a *API) updateProviderAccount(w http.ResponseWriter, r *http.Request) {
	id, err := positiveIDFromPath(r, "id")
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request saveProviderAccountRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	record, err := a.aggregate.SaveProvider(r.Context(), aggregate.ProviderAccountInput{
		ID:          id,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Protocol:    request.Protocol,
		Upstream:    request.Upstream,
		AuthHeader:  request.AuthHeader,
		Secret:      request.Secret,
		Models:      providerModelInputs(request.Models),
	})
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, aggregate.ProviderResponse(record))
}

func (a *API) listAggregateGateways(w http.ResponseWriter, r *http.Request) {
	gateways, err := a.aggregate.ListGateways(r.Context())
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Gateways []aggregate.Gateway `json:"gateways"`
	}{Gateways: gateways})
}

func (a *API) createAggregateGateway(w http.ResponseWriter, r *http.Request) {
	input, ok := a.decodeAggregateGatewayInput(w, r, 0)
	if !ok {
		return
	}
	record, err := a.aggregate.SaveGateway(r.Context(), input)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (a *API) updateAggregateGateway(w http.ResponseWriter, r *http.Request) {
	id, err := positiveIDFromPath(r, "id")
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	input, ok := a.decodeAggregateGatewayInput(w, r, id)
	if !ok {
		return
	}
	record, err := a.aggregate.SaveGateway(r.Context(), input)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (a *API) createAggregateGatewayKey(w http.ResponseWriter, r *http.Request) {
	id, err := positiveIDFromPath(r, "id")
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var request createAggregateKeyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	var expiresAt *time.Time
	if request.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, request.ExpiresAt)
		if err != nil {
			a.writeRequestError(w, r, invalidRequestField("expires_at", "Must be an RFC3339 timestamp."))
			return
		}
		expiresAt = &parsed
	}
	key, err := a.aggregate.CreateKey(r.Context(), aggregate.IssuedKeyInput{
		GatewayID: id,
		Name:      request.Name,
		Enabled:   request.Enabled,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (a *API) listAggregateGatewayKeys(w http.ResponseWriter, r *http.Request) {
	id, err := positiveIDFromPath(r, "id")
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	keys, err := a.aggregate.ListKeys(r.Context(), id)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Keys []aggregate.IssuedKey `json:"keys"`
	}{Keys: keys})
}

func (a *API) decodeAggregateGatewayInput(
	w http.ResponseWriter,
	r *http.Request,
	id int64,
) (aggregate.GatewayInput, bool) {
	var request saveAggregateGatewayRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return aggregate.GatewayInput{}, false
	}
	input := aggregate.GatewayInput{
		ID:          id,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Protocol:    request.Protocol,
		Routes:      make([]aggregate.RouteInput, 0, len(request.Routes)),
	}
	for _, route := range request.Routes {
		input.Routes = append(input.Routes, aggregate.RouteInput{
			PublicModel:       route.PublicModel,
			ProviderAccountID: route.ProviderAccountID,
			ProviderModel:     route.ProviderModel,
			Enabled:           route.Enabled,
		})
	}
	return input, true
}

func providerModelInputs(request []struct {
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}) []aggregate.ProviderModelInput {
	models := make([]aggregate.ProviderModelInput, 0, len(request))
	for _, model := range request {
		models = append(models, aggregate.ProviderModelInput{
			ModelID:     model.ModelID,
			DisplayName: model.DisplayName,
			Enabled:     model.Enabled,
		})
	}
	return models
}

func positiveIDFromPath(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidRequestField(name, fmt.Sprintf("%s must be a positive integer.", name))
	}
	return id, nil
}
