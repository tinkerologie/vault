package runner

import (
	"context"
	"time"

	"github.com/tinkerologie/vault/internal/engine"
)

// withProgressReporter installs the runner's detail reporter on ctx so
// engines and runner transfers can report structured progress (#456).
func (r *Runner) withProgressReporter(ctx context.Context) context.Context {
	return engine.WithDetailReporter(ctx, r.reportItemDetail)
}

// reportItemDetail folds a detail report into the run status. Every report
// counts as progress for the stall watchdog. A phase change is broadcast at
// once so throttling can never hide it; other updates ride on the regular,
// rate-limited progress broadcasts (see broadcastProgress).
func (r *Runner) reportItemDetail(item string, d engine.ProgressDetail) {
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now()
	r.lastProgressMu.Unlock()

	// Only the named current item: a late report from a finished item's
	// worker must touch neither the tracker nor the status.
	r.statusMu.RLock()
	current := r.currentRun != nil && r.currentRun.CurrentItem == item
	r.statusMu.RUnlock()
	if !current {
		return
	}

	snap, changed, applied := r.progress.update(item, d)
	if !applied {
		return // throttled same-phase report; the next one carries its totals
	}

	r.statusMu.Lock()
	var event map[string]any
	// Re-check: the item may have changed while the tracker was updated.
	if cr := r.currentRun; cr != nil && cr.CurrentItem == item {
		applyProgressSnapshot(cr, snap)
		if changed {
			kind := "backup_progress"
			if cr.RunType == "restore" {
				kind = "restore_progress"
			}
			event = map[string]any{
				"type":      kind,
				"job_id":    cr.JobID,
				"run_id":    cr.RunID,
				"item":      item,
				"item_type": cr.CurrentItemType,
				"percent":   cr.CurrentItemPercent,
				"message":   cr.CurrentItemMessage,
			}
		}
	}
	r.statusMu.Unlock()
	if event != nil {
		r.broadcastProgress(event)
	}
}

// applyProgressSnapshot copies snap into s. When the phase has a known
// total, current_item_percent becomes the phase percentage so clients that
// only read the percentage also see real progress.
func applyProgressSnapshot(s *RunStatus, snap progressSnapshot) {
	s.Phase = string(snap.Phase)
	s.PhaseStartedAt = formatTime(snap.PhaseStartedAt)
	s.BytesDone, s.BytesTotal = snap.BytesDone, snap.BytesTotal
	s.FilesDone, s.FilesTotal = snap.FilesDone, snap.FilesTotal
	s.RateBytesPerSec, s.RateFilesPerSec = 0, 0
	if snap.BytesDone > 0 || snap.BytesTotal > 0 {
		s.RateBytesPerSec = snap.Rate
	} else {
		s.RateFilesPerSec = snap.Rate
	}
	s.ETASeconds = snap.ETASeconds
	s.ProgressDeterminate = snap.Determinate
	s.LastProgressAt = formatTime(snap.LastProgressAt)
	if snap.Determinate {
		s.CurrentItemPercent = snap.Percent
	}
}

// clearProgressDetail empties the detail fields of s.
func clearProgressDetail(s *RunStatus) {
	s.Phase, s.PhaseStartedAt, s.LastProgressAt = "", "", ""
	s.BytesDone, s.BytesTotal, s.FilesDone, s.FilesTotal = 0, 0, 0, 0
	s.RateBytesPerSec, s.RateFilesPerSec, s.ETASeconds = 0, 0, 0
	s.ProgressDeterminate = false
}

// mergeProgressDetail adds the current item's detail fields to a
// backup_progress or restore_progress payload.
func (r *Runner) mergeProgressDetail(data map[string]any) {
	kind, _ := data["type"].(string)
	if kind != "backup_progress" && kind != "restore_progress" {
		return
	}
	item, _ := data["item"].(string)
	snap, ok := r.progress.snapshot(item)
	if !ok {
		return
	}
	var s RunStatus
	applyProgressSnapshot(&s, snap)
	data["phase"] = s.Phase
	data["phase_started_at"] = s.PhaseStartedAt
	data["bytes_done"] = s.BytesDone
	data["bytes_total"] = s.BytesTotal
	data["files_done"] = s.FilesDone
	data["files_total"] = s.FilesTotal
	data["rate_bytes_per_sec"] = s.RateBytesPerSec
	data["rate_files_per_sec"] = s.RateFilesPerSec
	data["eta_seconds"] = s.ETASeconds
	data["progress_determinate"] = s.ProgressDeterminate
	data["last_progress_at"] = s.LastProgressAt
	if s.ProgressDeterminate {
		data["percent"] = s.CurrentItemPercent
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
