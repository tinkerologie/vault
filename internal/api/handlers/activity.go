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
//	GET /api/v1/activity?limit=100&category=backup&before_id=1234
//
// `limit` is clamped to [1, maxActivityLimit] to prevent
// memory-exhaustion DoS from authenticated callers passing absurd values.
// `before_id` pages backwards: only entries with a smaller id are returned
// (0/absent = start from the newest).
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

	beforeID := int64(0)
	if b := r.URL.Query().Get("before_id"); b != "" {
		parsed, err := strconv.ParseInt(b, 10, 64)
		if err != nil || parsed < 1 {
			respondError(w, http.StatusBadRequest, "before_id must be a positive integer")
			return
		}
		beforeID = parsed
	}

	entries, err := h.db.ListActivityLogs(limit, category, beforeID)
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
