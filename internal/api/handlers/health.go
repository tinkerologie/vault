package handlers

import (
	"net/http"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/health"
)

// HealthHandler serves aggregated health metrics for the dashboard.
type HealthHandler struct {
	db *db.DB
}

// NewHealthHandler creates a HealthHandler.
func NewHealthHandler(database *db.DB) *HealthHandler {
	return &HealthHandler{db: database}
}

// Summary returns an aggregated health score and backup statistics.
func (h *HealthHandler) Summary(w http.ResponseWriter, r *http.Request) {
	result, err := health.BuildSummary(r.Context(), h.db, time.Now())
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, result)
}
