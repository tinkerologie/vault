package handlers

import (
	"net/http"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
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
	jobs, err := h.db.ListJobs()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var totalItems, protectedItems int
	var recentSuccess, recentFailed int
	var lastSuccessTime *time.Time

	for _, job := range jobs {
		items, err := h.db.GetJobItems(job.ID)
		if err != nil {
			continue
		}
		totalItems += len(items)
		if job.Enabled {
			protectedItems += len(items)
		}

		runs, err := h.db.GetJobRuns(job.ID, 10)
		if err != nil {
			continue
		}
		for _, run := range runs {
			switch run.Status {
			case "success", "completed":
				recentSuccess++
				if run.CompletedAt != nil && (lastSuccessTime == nil || run.CompletedAt.After(*lastSuccessTime)) {
					lastSuccessTime = run.CompletedAt
				}
			case "failed", "error":
				recentFailed++
			}
		}
	}

	totalRuns := recentSuccess + recentFailed
	successRate := 0
	if totalRuns > 0 {
		successRate = (recentSuccess * 100) / totalRuns
	}

	protectionPct := 0
	if totalItems > 0 {
		protectionPct = (protectedItems * 100) / totalItems
	}

	// Health score: weighted average of protection % and success rate.
	healthScore := (protectionPct*40 + successRate*60) / 100

	result := map[string]any{
		"health_score":    healthScore,
		"total_items":     totalItems,
		"protected_items": protectedItems,
		"protection_pct":  protectionPct,
		"success_rate":    successRate,
		"recent_success":  recentSuccess,
		"recent_failed":   recentFailed,
		"last_success_at": lastSuccessTime,
	}
	respondJSON(w, http.StatusOK, result)
}
