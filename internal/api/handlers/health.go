package handlers

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tinkerologie/vault/internal/db"
)

// defaultStaleAfter is how long a job with no cadence to judge it by (disabled,
// manual-only, or a schedule cron can't parse such as "L") may go without a
// successful backup before the dashboard flags it.
const defaultStaleAfter = 35 * 24 * time.Hour

// staleJob names a job whose newest backup is older than its cadence allows.
type staleJob struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	LastSuccessAt time.Time `json:"last_success_at"`
}

// jobStaleAfter allows two missed runs plus a day, so one late or retried run
// doesn't raise an alarm. A disabled schedule never fires, so its cadence says
// nothing about how old the job's backups may get.
func jobStaleAfter(job db.Job, now time.Time) time.Duration {
	if !job.Enabled || job.Schedule == "" {
		return defaultStaleAfter
	}
	schedule, err := cron.ParseStandard(job.Schedule)
	if err != nil {
		return defaultStaleAfter
	}
	next := schedule.Next(now)
	return 2*schedule.Next(next).Sub(next) + 24*time.Hour
}

// isBackupRun reports whether a run is a backup, not a restore. Older rows have
// no run type, and those were all backups.
func isBackupRun(run db.JobRun) bool {
	return run.RunType == "" || run.RunType == "backup"
}

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

	var recentSuccess, recentFailed, recentPartial, recentSkipped, recentCancelled int
	var lastSuccessTime *time.Time
	staleJobs := []staleJob{}
	now := time.Now()

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

		// A run of skips or failures can push the last success out of any recent
		// window, so this isn't read from the runs below. With run history purged,
		// the newest restore point is the only record left of a backup.
		lastSuccess, lsErr := h.db.GetLastSuccessfulBackupAt(job.ID)
		if lsErr != nil {
			log.Printf("health summary: finding last success for job %d: %v", job.ID, lsErr)
		}
		if lastSuccess != nil && (lastSuccessTime == nil || lastSuccess.After(*lastSuccessTime)) {
			lastSuccessTime = lastSuccess
		}
		newestBackup := lastSuccess
		for i := range rps {
			if newestBackup == nil || rps[i].CreatedAt.After(*newestBackup) {
				newestBackup = &rps[i].CreatedAt
			}
		}
		// A replicated job runs on its source, so its age here says nothing.
		if job.SourceID == 0 && newestBackup != nil && now.Sub(*newestBackup) > jobStaleAfter(job, now) {
			staleJobs = append(staleJobs, staleJob{ID: job.ID, Name: job.Name, Enabled: job.Enabled, LastSuccessAt: *newestBackup})
		}

		runs, err := h.db.GetJobRuns(job.ID, 10)
		if err != nil {
			continue
		}
		for _, run := range runs {
			switch run.Status {
			case "success", "completed":
				recentSuccess++
			case "failed", "error":
				recentFailed++
			}
			if !isBackupRun(run) {
				continue
			}
			switch run.Status {
			case "partial":
				recentPartial++
			case "skipped":
				recentSkipped++
			case "cancelled":
				recentCancelled++
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

	// A skipped or cancelled run is a backup that didn't happen, so it lowers the
	// success rate. A partial run isn't scored; it's reported as a warning.
	totalRuns := recentSuccess + recentFailed + recentSkipped + recentCancelled
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
		"health_score":     healthScore,
		"total_items":      totalItems,
		"protected_items":  protectedItems,
		"protected_keys":   protectedKeys,
		"pending_keys":     pendingKeys,
		"protection_pct":   protectionPct,
		"success_rate":     successRate,
		"recent_success":   recentSuccess,
		"recent_failed":    recentFailed,
		"recent_partial":   recentPartial,
		"recent_skipped":   recentSkipped,
		"recent_cancelled": recentCancelled,
		"last_success_at":  lastSuccessTime,
		"stale_jobs":       staleJobs,
	}
	respondJSON(w, http.StatusOK, result)
}
