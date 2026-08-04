package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

func TestModelCatalogRoutesExposeCurrentAndCSRFProtectedRefresh(t *testing.T) {
	catalog := adminCatalogFixture("old")
	updated := adminCatalogFixture("new")
	updated.Source.Revision = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	stub := &modelCatalogStub{
		current: catalog,
		refresh: modelcatalog.RefreshResult{
			Catalog:        updated,
			PreviousSource: catalog.Source,
			Changed:        true,
		},
	}
	fixture := newTestAPIWithCatalog(t, stub)
	cookies, csrf := fixture.changePassword(t)

	current := fixture.request(t, http.MethodGet, "/_admin/api/model-catalog", "", cookies, "")
	if current.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", current.Code, current.Body.String())
	}
	var currentBody modelcatalog.Catalog
	decodeTestJSON(t, current, &currentBody)
	if currentBody.Source.Revision != catalog.Source.Revision || len(currentBody.Models) != 1 {
		t.Fatalf("current=%+v", currentBody)
	}

	withoutCSRF := fixture.request(t, http.MethodPost, "/_admin/api/model-catalog/refresh", `{}`, cookies, "")
	assertAPIError(t, withoutCSRF, http.StatusForbidden, "csrf_failed")
	refreshed := fixture.request(t, http.MethodPost, "/_admin/api/model-catalog/refresh", `{}`, cookies, csrf)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshed.Code, refreshed.Body.String())
	}
	var refreshBody modelcatalog.RefreshResult
	decodeTestJSON(t, refreshed, &refreshBody)
	if !refreshBody.Changed || refreshBody.Catalog.Source.Revision != updated.Source.Revision || stub.refreshCalls != 1 {
		t.Fatalf("refresh=%+v calls=%d", refreshBody, stub.refreshCalls)
	}
}

func TestModelCatalogRefreshFailureDoesNotExposeUpstreamDetails(t *testing.T) {
	stub := &modelCatalogStub{
		current:    adminCatalogFixture("old"),
		refreshErr: errors.New("remote body contains private diagnostic"),
	}
	fixture := newTestAPIWithCatalog(t, stub)
	cookies, csrf := fixture.changePassword(t)

	response := fixture.request(t, http.MethodPost, "/_admin/api/model-catalog/refresh", `{}`, cookies, csrf)
	assertAPIError(t, response, http.StatusBadGateway, "model_catalog_refresh_failed")
	assertResponseDoesNotContain(t, response.Body.String(), "private diagnostic")
}

type modelCatalogStub struct {
	current      modelcatalog.Catalog
	refresh      modelcatalog.RefreshResult
	refreshErr   error
	refreshCalls int
}

func (stub *modelCatalogStub) Current() modelcatalog.Catalog { return stub.current }

func (stub *modelCatalogStub) Refresh(context.Context) (modelcatalog.RefreshResult, error) {
	stub.refreshCalls++
	return stub.refresh, stub.refreshErr
}

func adminCatalogFixture(name string) modelcatalog.Catalog {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	vision := false
	tools := true
	return modelcatalog.Catalog{
		Source: modelcatalog.Source{
			Name:            "Models.dev",
			Revision:        digest,
			ModelsSHA256:    digest,
			ProvidersSHA256: digest,
			Retrieved:       "2026-08-02",
		},
		Models: []modelcatalog.Model{{
			ID:             "alpha",
			CanonicalID:    "acme/alpha",
			Aliases:        []string{"acme/alpha"},
			Provider:       "Acme",
			Name:           name,
			Family:         "alpha",
			SupportsVision: &vision,
			SupportsTools:  &tools,
			Lifecycle:      "stable",
		}},
	}
}
