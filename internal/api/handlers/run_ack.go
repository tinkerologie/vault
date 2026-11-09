package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/tinkerologie/vault/internal/db"
)

// RunAckHandler records operator review of terminal run outcomes.
type RunAckHandler struct {
	db          *db.DB
	broadcaster Broadcaster
}

// NewRunAckHandler creates a handler that publishes run acknowledgement changes.
func NewRunAckHandler(database *db.DB, broadcaster Broadcaster) *RunAckHandler {
	return &RunAckHandler{db: database, broadcaster: broadcaster}
}

// Acknowledge handles POST /api/v1/runs/ack.
func (h *RunAckHandler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Reason string  `json:"reason"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		respondError(w, http.StatusUnprocessableEntity, "provide 1 to 1000 run IDs")
		return
	}
	for _, id := range req.IDs {
		if id < 1 {
			respondError(w, http.StatusUnprocessableEntity, "run IDs must be positive")
			return
		}
	}
	if utf8.RuneCountInString(req.Reason) > 500 {
		respondError(w, http.StatusUnprocessableEntity, "reason must be at most 500 characters")
		return
	}

	acknowledged, skipped, err := h.db.AcknowledgeJobRuns(r.Context(), req.IDs, "api", req.Reason)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	if acknowledged > 0 {
		if h.broadcaster != nil {
			h.broadcaster.Broadcast(map[string]any{
				"type": "job_runs_acknowledged", "acknowledged": acknowledged, "ids": req.IDs,
			})
		}
		h.db.LogActivity("info", "system", fmt.Sprintf("Acknowledged %d job run(s)", acknowledged), "")
	}
	respondJSON(w, http.StatusOK, map[string]int{"acknowledged": acknowledged, "skipped": skipped})
}
