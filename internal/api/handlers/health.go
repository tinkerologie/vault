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
		respondInternalError(w, err)
		return
	}

	var totalItems, protectedItems int
	var recentSuccess, recentFailed int
	var lastSuccessTime *time.Time
	protectedKeys := []string{}
	pendingKeys := []string{}

	for _, job := range jobs {
		items, err := h.db.GetJobItems(job.ID)
		if err != nil {
			continue
		}
		totalItems += len(items)

		// An item is "protected" only when at least one of the job's restore
		// points actually contains it (per the membership recorded when the
		// backup ran). Items configured in the job but never captured in a
		// restore point are "pending" — adding an item to a job does not make
		// it backed up. Schedule state is irrelevant: a disabled schedule must
		// not flip already-backed-up items back to unprotected.
		rps, _ := h.db.ListRestorePoints(job.ID)
		backedUp := make(map[string]struct{})
		legacyAllProtected := false
		for _, rp := range rps {
			members, known := rp.BackedUpItems()
			if !known {
				// A legacy restore point (produced before per-item membership
				// was recorded) tells us nothing per item; fall back to the
				// historical behaviour of treating every job item as protected
				// so existing installs don't regress.
				legacyAllProtected = true
				continue
			}
			for name := range members {
				backedUp[name] = struct{}{}
			}
		}
		for _, item := range items {
			key := item.ItemType + ":" + item.ItemName
			_, isBackedUp := backedUp[item.ItemName]
			if legacyAllProtected || isBackedUp {
				protectedItems++
				protectedKeys = append(protectedKeys, key)
			} else {
				pendingKeys = append(pendingKeys, key)
			}
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
	// Round to nearest integer rather than truncating.
	healthScore := (protectionPct*40 + successRate*60 + 50) / 100

	result := map[string]any{
		"health_score":    healthScore,
		"total_items":     totalItems,
		"protected_items": protectedItems,
		"protected_keys":  protectedKeys,
		"pending_keys":    pendingKeys,
		"protection_pct":  protectionPct,
		"success_rate":    successRate,
		"recent_success":  recentSuccess,
		"recent_failed":   recentFailed,
		"last_success_at": lastSuccessTime,
	}
	respondJSON(w, http.StatusOK, result)
}
