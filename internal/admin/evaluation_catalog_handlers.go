package admin

import (
	"errors"
	"net/http"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
)

type EvaluationCatalogService interface {
	Current() evalcatalog.Catalog
}

type EvaluationCatalogUpdater interface {
	Start() (evalcatalog.UpdateJob, error)
	Status() evalcatalog.UpdateJob
}

func (a *API) getEvaluationCatalog(w http.ResponseWriter, r *http.Request) {
	if a.evaluationCatalog == nil {
		a.writeInternalError(w, r, errEvaluationCatalogUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.evaluationCatalog.Current())
}

func (a *API) updateEvaluationCatalog(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	if a.evaluationCatalogUpdater == nil {
		a.writeInternalError(w, r, errEvaluationCatalogUnavailable)
		return
	}
	job, err := a.evaluationCatalogUpdater.Start()
	if err != nil {
		if errors.Is(err, evalcatalog.ErrUpdateInProgress) {
			writeError(
				w, http.StatusConflict, "evaluation_catalog_update_in_progress",
				"An evaluation catalog update is already running.", nil,
			)
			return
		}
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) getEvaluationCatalogUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if a.evaluationCatalogUpdater == nil {
		a.writeInternalError(w, r, errEvaluationCatalogUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.evaluationCatalogUpdater.Status())
}
