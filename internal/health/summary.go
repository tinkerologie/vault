// Package health builds the backup health summary and the protection
// classification that the REST API, the MCP server and the recovery plan share,
// so the Dashboard, assistants, Home Assistant and Disaster Recovery agree on
// what is protected (#708, #717). It depends only on the database, crypto and
// the cron parser, never on a transport.
package health

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// staleJob names a job whose newest backup is older than its cadence allows.
type staleJob struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	LastSuccessAt time.Time `json:"last_success_at"`
}

// isBackupRun reports whether a run is a backup, not a restore. Older rows have
// no run type, and those were all backups.
func isBackupRun(run db.JobRun) bool {
	return run.RunType == "" || run.RunType == "backup"
}

// BuildSummary aggregates the health score, protection classification
// and recent backup outcomes. The REST summary and the MCP get_health_summary
// tool both serve it, so the Dashboard, assistants and Home Assistant agree
// on what is protected (#708).
func BuildSummary(ctx context.Context, database *db.DB, now time.Time) (map[string]any, error) {
	jobs, err := database.ListJobsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}

	var recentSuccess, recentFailed, recentPartial, recentSkipped, recentCancelled int
	var outstandingFailed, outstandingPartial, outstandingSkipped, outstandingCancelled int
	var lastSuccessTime *time.Time
	staleJobs := []staleJob{}

	// Counts are per distinct item (type:name), not per job-item pair: the same
	// container or VM is often covered by several jobs, and one key must never be
	// both protected and pending. An item is protected if ANY job has backed it
	// up, and only once a Restore Point actually contains it (the membership
	// recorded when the backup ran); adding an item to a job does not make it
	// backed up. Schedule state is irrelevant to membership: a disabled schedule
	// must not flip already-backed-up items back to unprotected. The classifier
	// is shared with the recovery plan so both pages count the same items.
	classes := NewProtectionClassifier(now)

	for _, job := range jobs {
		items, err := database.GetJobItemsContext(ctx, job.ID)
		if err != nil {
			continue
		}

		rps, rpErr := database.ListRestorePointsContext(ctx, job.ID)
		if rpErr != nil {
			// Surface DB issues rather than silently treating the job as having
			// no backups (which would mislabel its items as pending).
			log.Printf("health summary: listing restore points for job %d: %v", job.ID, rpErr)
		}
		classes.AddJob(job, items, rps)

		// A run of skips or failures can push the last success out of any recent
		// window, so this isn't read from the runs below. With run history purged,
		// the newest restore point is the only record left of a backup.
		lastSuccess, lsErr := database.GetLastSuccessfulBackupAt(job.ID)
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
		if job.SourceID == 0 && newestBackup != nil && jobIsStale(job, *newestBackup, now) {
			staleJobs = append(staleJobs, staleJob{ID: job.ID, Name: job.Name, Enabled: job.Enabled, LastSuccessAt: *newestBackup})
		}

		runs, err := database.GetJobRuns(job.ID, 10)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if !isBackupRun(run) {
				continue
			}
			switch run.Status {
			case "success", "completed":
				recentSuccess++
			case "failed", "error":
				recentFailed++
				if run.AcknowledgedAt == nil {
					outstandingFailed++
				}
			case "partial":
				recentPartial++
				if run.AcknowledgedAt == nil {
					outstandingPartial++
				}
			case "skipped":
				recentSkipped++
				if run.AcknowledgedAt == nil {
					outstandingSkipped++
				}
			case "cancelled":
				recentCancelled++
				if run.AcknowledgedAt == nil {
					outstandingCancelled++
				}
			}
		}
	}

	// Sorted so the payload is stable across requests (map iteration is not).
	protectedKeys := classes.backedKeys()
	pendingKeys := classes.pendingKeys()
	outOfDateKeys := classes.outOfDateKeys()

	totalItems := len(classes.all)
	protectedItems := len(protectedKeys)
	upToDateItems := protectedItems - len(outOfDateKeys)

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

	// Acknowledgement clears outstanding attention without rewriting historical metrics.
	outstandingTotal := recentSuccess + outstandingFailed + outstandingSkipped + outstandingCancelled
	outstandingSuccessRate := 0
	if outstandingTotal > 0 {
		outstandingSuccessRate = (recentSuccess * 100) / outstandingTotal
	}
	// Health score: weighted average of up-to-date protection and outstanding
	// success rate. An out-of-date item still has a Restore Point, so it counts in
	// protection_pct, but not towards the score: a stale backup isn't healthy.
	// Round to nearest integer rather than truncating.
	upToDatePct := 0
	if totalItems > 0 {
		upToDatePct = (upToDateItems * 100) / totalItems
	}
	healthScore := (upToDatePct*40 + outstandingSuccessRate*60 + 50) / 100

	result := map[string]any{
		"health_score":          healthScore,
		"total_items":           totalItems,
		"protected_items":       protectedItems,
		"protected_keys":        protectedKeys,
		"pending_keys":          pendingKeys,
		"pending_items":         len(pendingKeys),
		"out_of_date_keys":      outOfDateKeys,
		"out_of_date_items":     len(outOfDateKeys),
		"up_to_date_items":      upToDateItems,
		"item_last_backup_at":   classes.newestBackups(),
		"server_key_at_risk":    serverKeyAtRisk(database, jobs),
		"protection_pct":        protectionPct,
		"success_rate":          successRate,
		"recent_success":        recentSuccess,
		"recent_failed":         recentFailed,
		"recent_partial":        recentPartial,
		"recent_skipped":        recentSkipped,
		"recent_cancelled":      recentCancelled,
		"outstanding_failed":    outstandingFailed,
		"outstanding_partial":   outstandingPartial,
		"outstanding_skipped":   outstandingSkipped,
		"outstanding_cancelled": outstandingCancelled,
		"last_success_at":       lastSuccessTime,
		"stale_jobs":            staleJobs,
	}
	return result, nil
}
