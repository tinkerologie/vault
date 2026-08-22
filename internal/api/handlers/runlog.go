package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ruaan-deysel/vault/internal/db"
)

// RunLogHandler serves per-run streaming log entries (issue #328).
type RunLogHandler struct {
	db *db.DB
}

// NewRunLogHandler creates a new RunLogHandler.
func NewRunLogHandler(database *db.DB) *RunLogHandler {
	return &RunLogHandler{db: database}
}

// List returns a run's log entries oldest-first.
//
//	GET /api/v1/runs/{runId}/logs?after=0&limit=500&tail=true
//
// `after` supports tailing: a client that already rendered entries up to
// id N passes after=N and receives only newer lines, so live views poll
// cheaply and WebSocket events reconcile after a reconnect. `limit` is
// clamped the same way the activity handler clamps (positive integers
// only; the repo clamps the ceiling to 1000). With `tail=true` the fetch
// flips to the run's NEWEST lines: the response is the last `limit`
// entries (oldest-first within that window), so the end-of-run summary —
// the status/size/duration line the console must show — is always
// included, even when the run's log exceeds the limit. The unified
// console fetches tail-first for exactly this reason (#328). Unknown runs
// return 404 so a deleted run reads differently from an empty one.
func (h *RunLogHandler) List(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(chi.URLParam(r, "runId"), 10, 64)
	if err != nil || runID < 1 {
		respondError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	afterID := int64(0)
	if a := r.URL.Query().Get("after"); a != "" {
		parsed, perr := strconv.ParseInt(a, 10, 64)
		if perr != nil || parsed < 0 {
			respondError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
		afterID = parsed
	}
	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, perr := strconv.Atoi(l)
		if perr != nil || parsed < 1 {
			respondError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	tail := r.URL.Query().Get("tail") == "true"

	if _, gerr := h.db.GetJobRun(runID); gerr != nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}

	var entries []db.RunLogEntry
	var lerr error
	if tail {
		entries, lerr = h.db.TailRunLogEntries(r.Context(), runID, limit)
	} else {
		entries, lerr = h.db.ListRunLogEntries(r.Context(), runID, afterID, limit)
	}
	if lerr != nil {
		respondInternalError(w, lerr)
		return
	}
	if entries == nil {
		entries = []db.RunLogEntry{}
	}
	var lastID int64
	if n := len(entries); n > 0 {
		lastID = entries[n-1].ID
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"run_id":  runID,
		"entries": entries,
		"last_id": lastID,
	})
}
