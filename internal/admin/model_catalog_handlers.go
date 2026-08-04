package admin

import (
	"context"
	"net/http"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

type ModelCatalogService interface {
	Current() modelcatalog.Catalog
	Refresh(context.Context) (modelcatalog.RefreshResult, error)
}

func (a *API) getModelCatalog(w http.ResponseWriter, r *http.Request) {
	if a.modelCatalog == nil {
		a.writeInternalError(w, r, errModelCatalogUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.modelCatalog.Current())
}

func (a *API) refreshModelCatalog(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	if a.modelCatalog == nil {
		a.writeInternalError(w, r, errModelCatalogUnavailable)
		return
	}
	result, err := a.modelCatalog.Refresh(r.Context())
	if err != nil {
		a.logger.Warn("model catalog refresh failed", "error", err)
		writeError(
			w,
			http.StatusBadGateway,
			"model_catalog_refresh_failed",
			"Unable to refresh the remote model catalog; the previous catalog remains active.",
			nil,
		)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
