package handlers

import (
	"fmt"
	"log"
	"net/http"

	"github.com/ruaan-deysel/vault/internal/db"
)

// HistoryHandler serves job run history endpoints.
type HistoryHandler struct {
	db *db.DB
}

// NewHistoryHandler creates a new HistoryHandler.
func NewHistoryHandler(database *db.DB) *HistoryHandler {
	return &HistoryHandler{db: database}
}

// Purge deletes all job run history records.
//
//	DELETE /api/v1/history
func (h *HistoryHandler) Purge(w http.ResponseWriter, _ *http.Request) {
	count, err := h.db.PurgeJobRuns()
	if err != nil {
		log.Printf("ERROR purging job runs: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	h.db.LogActivity("info", "system", "Job run history purged", fmt.Sprintf(`{"deleted_count":%d}`, count))
	w.WriteHeader(http.StatusNoContent)
}
