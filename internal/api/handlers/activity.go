package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/tinkerologie/vault/internal/db"
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
	h.markRunsGone(entries)
	respondJSON(w, http.StatusOK, entries)
}

// markRunsGone flags entries pointing at a run log that no longer exists. Activity
// outlives its runs (retention, a purge, a deleted job), and without this the
// Logs page asks for each gone run's log and gets a 404. A failed lookup leaves
// the entries unmarked: the page then asks, and copes with the 404.
func (h *ActivityHandler) markRunsGone(entries []db.ActivityLogEntry) {
	runIDs := make([]int64, len(entries))
	var ids []int64
	for i, entry := range entries {
		var details struct {
			RunID  int64 `json:"run_id"`
			RunLog bool  `json:"run_log"`
		}
		if json.Unmarshal([]byte(entry.Details), &details) != nil || !details.RunLog || details.RunID == 0 {
			continue
		}
		runIDs[i] = details.RunID
		ids = append(ids, details.RunID)
	}
	existing, err := h.db.ExistingJobRunIDs(ids)
	if err != nil {
		log.Printf("activity: checking run logs: %v", err)
		return
	}
	for i, id := range runIDs {
		if id == 0 {
			continue
		}
		if _, ok := existing[id]; !ok {
			entries[i].RunGone = true
		}
	}
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
