package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// Adaptive backups (issue #240): jobs with AdaptiveEnabled defer their runs
// while any of their workloads are actively in use, re-checking on a short
// interval until idle or until the max-postpone window elapses (then the
// backup runs anyway — a busy workload still deserves protection).

func (r *Runner) adaptiveSettingInt(key string) int {
	v, _ := r.db.GetSetting(key, docsmeta.DefaultFor(key))
	n, err := strconv.Atoi(v)
	if err != nil {
		n, _ = strconv.Atoi(docsmeta.DefaultFor(key))
	}
	return n
}

// adaptiveBusyReason probes every item of the job and returns a
// human-readable reason when one is actively in use, or "" when all are
// idle. Probe failures are fail-open (unknown = idle) so a stats hiccup
// never blocks backups.
func (r *Runner) adaptiveBusyReason(ctx context.Context, items []db.JobItem) string {
	th := engine.IdleThresholds{
		CPUPercent: float64(r.adaptiveSettingInt("adaptive_idle_cpu_percent")),
		NetKbps:    float64(r.adaptiveSettingInt("adaptive_idle_net_kbps")),
	}
	folderWindow := time.Duration(r.adaptiveSettingInt("adaptive_folder_idle_minutes")) * time.Minute

	for _, item := range items {
		var sample engine.ActivitySample
		switch item.ItemType {
		case "container":
			ch, err := engine.NewContainerHandler()
			if err != nil {
				continue
			}
			sample = ch.ProbeActivity(ctx, item.ItemName)
		case "vm":
			vh, err := engine.NewVMHandler()
			if err != nil {
				continue
			}
			sample = vh.ProbeActivity(ctx, item.ItemName)
			_ = vh.Close()
		case "folder":
			var s map[string]any
			if json.Unmarshal([]byte(item.Settings), &s) != nil {
				continue
			}
			path, _ := s["path"].(string)
			if path == "" {
				continue
			}
			sample = engine.ProbeFolderActivity(ctx, path, extractSettingsStrings(s, "exclude_paths"), folderWindow)
		default:
			continue
		}
		if sample.Active(th) {
			if item.ItemType == "folder" {
				return fmt.Sprintf("%s %q has files changed within the last %d minutes",
					item.ItemType, item.ItemName, r.adaptiveSettingInt("adaptive_folder_idle_minutes"))
			}
			return fmt.Sprintf("%s %q is in use (cpu %.0f%%, network %.0f KB/s)",
				item.ItemType, item.ItemName, sample.CPUPercent, sample.NetBytesPerSec/1000)
		}
	}
	return ""
}

// extractSettingsStrings reads a []string-ish key from an item settings map.
func extractSettingsStrings(s map[string]any, key string) []string {
	raw, ok := s[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if str, ok := e.(string); ok && str != "" {
			out = append(out, str)
		}
	}
	return out
}

// postponeState tracks one job's open adaptive postpone window.
type postponeState struct {
	first          time.Time
	recorded       bool // one postponed run row per window, not per re-check
	recheckPending bool // at most one armed re-check timer per job
}

// adaptivePostpone records the deferral (once per window) and arms a single
// re-check timer. Returns true when the run should be deferred; false once
// the max-postpone window has elapsed (run anyway).
func (r *Runner) adaptivePostpone(jobID int64, jobName, reason string) bool {
	maxPostpone := time.Duration(r.adaptiveSettingInt("adaptive_max_postpone_minutes")) * time.Minute
	recheck := time.Duration(r.adaptiveSettingInt("adaptive_recheck_minutes")) * time.Minute
	if recheck < 30*time.Second {
		recheck = 30 * time.Second
	}

	r.postponeMu.Lock()
	st := r.postponedSince[jobID]
	if st == nil {
		st = &postponeState{first: time.Now()}
		if r.postponedSince == nil {
			r.postponedSince = make(map[int64]*postponeState)
		}
		r.postponedSince[jobID] = st
	}
	elapsed := time.Since(st.first)
	if elapsed >= maxPostpone {
		delete(r.postponedSince, jobID)
		r.postponeMu.Unlock()
		log.Printf("runner: job %d (%s) still busy after %s of postponing — running anyway", jobID, jobName, elapsed.Round(time.Minute))
		return false
	}
	needRow := !st.recorded
	st.recorded = true
	needTimer := !st.recheckPending
	st.recheckPending = true
	r.postponeMu.Unlock()

	// One postponed row per window — repeated re-checks must not flood run
	// history or skew health/anomaly windows with zero-byte rows.
	if needRow {
		run := db.JobRun{JobID: jobID, Status: "postponed"}
		runID, err := r.db.CreateJobRun(run)
		if err != nil {
			log.Printf("runner: recording postponed run for job %d: %v", jobID, err)
		} else {
			run.ID = runID
			run.Log = reason
			if updErr := r.db.UpdateJobRun(run); updErr != nil {
				log.Printf("runner: updating postponed run %d: %v", runID, updErr)
			}
			r.broadcast(map[string]any{
				"type": "job_run_completed", "job_id": jobID, "run_id": runID, "status": "postponed",
			})
		}
		r.logActivity("info", "backup", fmt.Sprintf("Backup of %q postponed: %s", jobName, reason),
			structuredDetails(map[string]any{"job_id": jobID, "reason": reason}))
	}
	log.Printf("runner: job %d (%s) postponed — %s (re-check in %s)", jobID, jobName, reason, recheck)
	r.broadcast(map[string]any{
		"type": "job_postponed", "job_id": jobID, "job_name": jobName, "reason": reason,
	})

	if needTimer {
		time.AfterFunc(recheck, func() {
			defer guardPanic("adaptive re-check")
			r.postponeMu.Lock()
			if st := r.postponedSince[jobID]; st != nil {
				st.recheckPending = false
			}
			r.postponeMu.Unlock()
			// The job may have been disabled or had adaptive turned off
			// while postponed — never resurrect a run the operator stopped.
			job, err := r.db.GetJob(jobID)
			if err != nil || !job.Enabled || !job.AdaptiveEnabled {
				r.adaptiveClearPostpone(jobID)
				return
			}
			r.RunJob(jobID)
		})
	}
	return true
}

// adaptiveClearPostpone forgets a job's postpone window once it actually runs.
func (r *Runner) adaptiveClearPostpone(jobID int64) {
	r.postponeMu.Lock()
	delete(r.postponedSince, jobID)
	r.postponeMu.Unlock()
}
