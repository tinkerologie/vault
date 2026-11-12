package runner

import (
	"context"
	"errors"
	"time"
)

var (
	errOperatorRestoreStop = errors.New("Restore Run stopped by the operator")
	errRestoreWatchdog     = errors.New("Restore Run stalled")
)

func (r *Runner) publishRestoreCancellation(status *RunStatus, cancel context.CancelCauseFunc) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	r.cancellingRunID = status.RunID
	r.cancellingJobID = status.JobID
	r.cancellingRestoreRequest = status.RestoreRequestID
	r.cancelFn = func() { cancel(nil) }
	if status.RestoreRequestID != "" {
		r.cancelFn = func() { cancel(errOperatorRestoreStop) }
	}
	r.setRunStatus(status)
}

// StopRestoreRun signals only the active approved restore with this run ID.
// The worker records the actual outcome when the engine returns.
func (r *Runner) StopRestoreRun(runID int64, operator string) bool {
	r.cancelMu.Lock()
	if r.cancelFn == nil || r.cancellingRunID != runID || r.cancellingRestoreRequest == "" {
		r.cancelMu.Unlock()
		return false
	}
	if r.restoreStopRequestedAt != nil {
		r.cancelMu.Unlock()
		return true
	}
	now := time.Now().UTC()
	r.restoreStopRequestedAt = &now
	r.restoreStoppedBy = operator
	jobID := r.cancellingJobID
	r.statusMu.Lock()
	r.currentRun.Cancelling = true
	r.statusMu.Unlock()
	r.cancelFn()
	r.cancelMu.Unlock()

	r.broadcast(map[string]any{
		"type": "job_cancelling", "job_id": jobID, "run_id": runID, "run_type": "restore",
	})
	return true
}
