package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/mount"
)

// MountHandler handles HTTP endpoints for FUSE mount lifecycle.
type MountHandler struct {
	mgr *mount.Manager
	db  *db.DB
}

// NewMountHandler constructs a MountHandler.
func NewMountHandler(mgr *mount.Manager, d *db.DB) *MountHandler {
	return &MountHandler{
		mgr: mgr,
		db:  d,
	}
}

// List handles GET /api/v1/mounts.
func (h *MountHandler) List(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active") != "false"
	sessions, err := h.db.ListMountSessions(activeOnly)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if sessions == nil {
		sessions = []db.MountSession{}
	}
	respondJSON(w, http.StatusOK, sessions)
}

// Get handles GET /api/v1/mounts/{id}.
func (h *MountHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid mount session id")
		return
	}

	session, err := h.mgr.Get(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "mount session not found")
			return
		}
		respondInternalError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, session)
}

// MountRestorePoint handles POST /api/v1/jobs/{id}/restore-points/{rpid}/mount.
func (h *MountHandler) MountRestorePoint(w http.ResponseWriter, r *http.Request) {
	jobIDStr := chi.URLParam(r, "id")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	rpIDStr := chi.URLParam(r, "rpid")
	rpID, err := strconv.ParseInt(rpIDStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid restore point id")
		return
	}

	session, err := h.mgr.MountRestorePoint(r.Context(), jobID, rpID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "job or restore point not found")
			return
		}
		respondInternalError(w, err)
		return
	}

	respondJSON(w, http.StatusCreated, session)
}

// Unmount handles POST /api/v1/mounts/{id}/unmount.
func (h *MountHandler) Unmount(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid mount session id")
		return
	}

	if err := h.mgr.Unmount(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(w, http.StatusNotFound, "mount session not found")
			return
		}
		respondInternalError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "unmounted"})
}
