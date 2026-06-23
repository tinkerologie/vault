package handlers

import (
	"net/http"
	"strconv"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ActivityHandler serves activity log endpoints.
type ActivityHandler struct {
	db *db.DB
}

// NewActivityHandler creates a new ActivityHandler.
func NewActivityHandler(database *db.DB) *ActivityHandler {
	return &ActivityHandler{db: database}
}

// List returns recent activity log entries.
//
//	GET /api/v1/activity?limit=100&category=backup
//
// `limit` is clamped to [1, maxActivityLimit] to prevent
// memory-exhaustion DoS from authenticated callers passing absurd values.
func (h *ActivityHandler) List(w http.ResponseWriter, r *http.Request) {
	const maxActivityLimit = 1000
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsed > maxActivityLimit {
			parsed = maxActivityLimit
		}
		limit = parsed
	}
	category := r.URL.Query().Get("category")

	entries, err := h.db.ListActivityLogs(limit, category)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if entries == nil {
		entries = []db.ActivityLogEntry{}
	}
	respondJSON(w, http.StatusOK, entries)
}

// Purge deletes all activity log entries.
//
//	DELETE /api/v1/activity
func (h *ActivityHandler) Purge(w http.ResponseWriter, _ *http.Request) {
	if err := h.db.DeleteOldActivityLogs(0); err != nil {
		respondInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
