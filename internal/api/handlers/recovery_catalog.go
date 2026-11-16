package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/recovery"
	"github.com/tinkerologie/vault/internal/runner"
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

func (h *RecoveryHandler) FindRestorePoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := recovery.FindOptions{ItemSelection: recoverySelection(r)}
	if q.Has("limit") {
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		opts.Limit = limit
	}
	if q.Has("before_id") {
		id, err := strconv.ParseInt(q.Get("before_id"), 10, 64)
		if err != nil || id < 1 {
			respondError(w, http.StatusBadRequest, "before_id must be a positive integer")
			return
		}
		opts.BeforeID = id
	}
	result, err := recovery.FindRestorePoints(r.Context(), h.db, opts)
	writeRecoveryEvidence(w, result, err)
}

func (h *RecoveryHandler) GetRecoveryEvidence(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "rpid")
	if !ok {
		return
	}
	result, err := recovery.GetEvidence(r.Context(), h.db, id, recoverySelection(r))
	writeRecoveryEvidence(w, result, err)
}

func recoverySelection(r *http.Request) runner.ItemSelection {
	q := r.URL.Query()
	return runner.ItemSelection{ItemType: q.Get("item_type"), ItemName: q.Get("item_name")}
}

func writeRecoveryEvidence(w http.ResponseWriter, result any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case errors.Is(err, runner.ErrInvalidSelection), errors.Is(err, recovery.ErrInvalidFindOptions):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrNotFound):
		respondError(w, http.StatusNotFound, "Restore Point not found")
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not read recovery evidence")
	default:
		respondJSON(w, http.StatusOK, result)
	}
}
