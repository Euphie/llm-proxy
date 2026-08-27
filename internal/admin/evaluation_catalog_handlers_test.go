package admin

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
)

func TestEvaluationCatalogRoutesExposeCurrentAndCSRFProtectedUpdate(t *testing.T) {
	old := adminEvaluationCatalogFixture("a")
	service := &evaluationCatalogStub{current: old}
	job := evalcatalog.UpdateJob{
		ID: "job-1", State: evalcatalog.UpdateStateRunning, PreviousRevision: old.Revision, Revision: old.Revision,
	}
	updater := &evaluationCatalogUpdaterStub{started: job, status: job}
	fixture := newTestAPIWithEvaluationCatalogUpdater(t, service, updater)
	cookies, csrf := fixture.changePassword(t)

	current := fixture.request(t, http.MethodGet, "/_admin/api/evaluation-catalog", "", cookies, "")
	if current.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", current.Code, current.Body.String())
	}
	var currentBody evalcatalog.Catalog
	decodeTestJSON(t, current, &currentBody)
	if currentBody.Revision != old.Revision {
		t.Fatalf("current=%+v", currentBody)
	}

	withoutCSRF := fixture.request(t, http.MethodPost, "/_admin/api/evaluation-catalog/update", `{}`, cookies, "")
	assertAPIError(t, withoutCSRF, http.StatusForbidden, "csrf_failed")
	response := fixture.request(t, http.MethodPost, "/_admin/api/evaluation-catalog/update", `{}`, cookies, csrf)
	if response.Code != http.StatusAccepted {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
	var started evalcatalog.UpdateJob
	decodeTestJSON(t, response, &started)
	if started.ID != job.ID || updater.startCalls != 1 {
		t.Fatalf("started=%+v start_calls=%d", started, updater.startCalls)
	}

	statusResponse := fixture.request(t, http.MethodGet, "/_admin/api/evaluation-catalog/update-status", "", cookies, "")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var status evalcatalog.UpdateJob
	decodeTestJSON(t, statusResponse, &status)
	if status.ID != job.ID || updater.statusCalls != 1 || service.current.Revision != old.Revision {
		t.Fatalf("status=%+v status_calls=%d catalog=%+v", status, updater.statusCalls, service.current)
	}
}

func TestEvaluationCatalogUpdateConflictKeepsDetailsPrivate(t *testing.T) {
	service := &evaluationCatalogStub{current: adminEvaluationCatalogFixture("a")}
	updater := &evaluationCatalogUpdaterStub{startErr: fmt.Errorf("private detail: %w", evalcatalog.ErrUpdateInProgress)}
	fixture := newTestAPIWithEvaluationCatalogUpdater(t, service, updater)
	cookies, csrf := fixture.changePassword(t)
	response := fixture.request(t, http.MethodPost, "/_admin/api/evaluation-catalog/update", `{}`, cookies, csrf)
	assertAPIError(t, response, http.StatusConflict, "evaluation_catalog_update_in_progress")
	assertResponseDoesNotContain(t, response.Body.String(), "private detail")
}

type evaluationCatalogStub struct {
	current evalcatalog.Catalog
}

func (stub *evaluationCatalogStub) Current() evalcatalog.Catalog { return stub.current }

type evaluationCatalogUpdaterStub struct {
	started     evalcatalog.UpdateJob
	startErr    error
	status      evalcatalog.UpdateJob
	startCalls  int
	statusCalls int
}

func (stub *evaluationCatalogUpdaterStub) Start() (evalcatalog.UpdateJob, error) {
	stub.startCalls++
	return stub.started, stub.startErr
}

func (stub *evaluationCatalogUpdaterStub) Status() evalcatalog.UpdateJob {
	stub.statusCalls++
	return stub.status
}

func adminEvaluationCatalogFixture(seed string) evalcatalog.Catalog {
	digest := strings.Repeat(seed, 64)
	return evalcatalog.Catalog{
		SchemaVersion: 1, Revision: digest,
		RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		Sources: []evalcatalog.Source{{
			ID: "fixture", Name: "Fixture", URL: "https://example.test/results",
			License: "CC-BY-4.0", Version: seed,
			RetrievedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), SHA256: digest,
		}},
		Results: []evalcatalog.Result{},
	}
}
