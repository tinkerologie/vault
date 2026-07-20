package handlers

import (
	"log"
	"net/http"
	"sort"
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

	var recentSuccess, recentFailed int
	var lastSuccessTime *time.Time

	// Counts are per distinct item (type:name), not per job-item pair. The same
	// container or VM is frequently covered by several jobs; counting it once
	// per job inflated the totals and, when only one of those jobs had produced
	// a restore point, emitted the SAME key in both protected_keys and
	// pending_keys — so the UI showed one item as simultaneously protected and
	// unprotected, and protection_pct under-reported a genuinely protected item.
	// An item is protected if ANY job has backed it up.
	protectedSet := map[string]struct{}{}
	allItems := map[string]struct{}{}

	for _, job := range jobs {
		items, err := h.db.GetJobItems(job.ID)
		if err != nil {
			continue
		}

		// An item is "protected" only when at least one of the job's restore
		// points actually contains it (per the membership recorded when the
		// backup ran). Items configured in the job but never captured in a
		// restore point are "pending" — adding an item to a job does not make
		// it backed up. Schedule state is irrelevant: a disabled schedule must
		// not flip already-backed-up items back to unprotected.
		rps, rpErr := h.db.ListRestorePoints(job.ID)
		if rpErr != nil {
			// Surface DB issues rather than silently treating the job as having
			// no backups (which would mislabel its items as pending).
			log.Printf("health summary: listing restore points for job %d: %v", job.ID, rpErr)
		}
		backedUp := make(map[string]struct{})
		legacyAllProtected := false
		for _, rp := range rps {
			members, known := rp.BackedUpItems()
			if !known {
				// A legacy restore point (produced before per-item membership
				// was recorded) tells us nothing per item; fall back to the
				// historical behaviour of treating every job item as protected
				// so existing installs don't regress. Once one is found the
				// per-item map is irrelevant, so stop scanning.
				legacyAllProtected = true
				break
			}
			for name := range members {
				backedUp[name] = struct{}{}
			}
		}
		for _, item := range items {
			key := item.ItemType + ":" + item.ItemName
			allItems[key] = struct{}{}
			if _, isBackedUp := backedUp[item.ItemName]; legacyAllProtected || isBackedUp {
				protectedSet[key] = struct{}{}
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

	// Sorted so the payload is stable across requests (map iteration is not).
	protectedKeys := make([]string, 0, len(protectedSet))
	for key := range protectedSet {
		protectedKeys = append(protectedKeys, key)
	}
	sort.Strings(protectedKeys)

	pendingKeys := []string{}
	for key := range allItems {
		if _, ok := protectedSet[key]; !ok {
			pendingKeys = append(pendingKeys, key)
		}
	}
	sort.Strings(pendingKeys)

	totalItems := len(allItems)
	protectedItems := len(protectedSet)

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
