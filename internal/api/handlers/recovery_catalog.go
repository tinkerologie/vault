package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tinkerologie/vault/internal/recovery"
)

func (h *RecoveryHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var opts recovery.CatalogOptions
	if q.Has("job_id") {
		id, err := strconv.ParseInt(q.Get("job_id"), 10, 64)
		if err != nil || id < 1 {
			respondError(w, http.StatusBadRequest, "job_id must be a positive integer")
			return
		}
		opts.JobID = id
	}
	if q.Has("limit_per_job") {
		limit, err := strconv.Atoi(q.Get("limit_per_job"))
		if err != nil || limit < 1 {
			respondError(w, http.StatusBadRequest, "limit_per_job must be a positive integer")
			return
		}
		opts.LimitPerJob = limit
	}

	catalog, err := recovery.BuildCatalog(r.Context(), h.db, opts)
	switch {
	case errors.Is(err, recovery.ErrInvalidCatalogOptions):
		respondError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not read recovery catalog")
	default:
		w.Header().Set("Cache-Control", "no-store")
		respondJSON(w, http.StatusOK, catalog)
	}
}
