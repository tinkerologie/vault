package runner

import (
	"context"
	"errors"
	"time"
)

var (
	errOperatorRestoreStop = errors.New("restore run stopped by the operator")
	errRestoreWatchdog     = errors.New("restore run stalled")
)

func (r *Runner) publishRestoreCancellation(status *RunStatus, cancel context.CancelCauseFunc) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	r.cancellingRunID = status.RunID
	r.cancellingJobID = status.JobID
	r.cancellingRestoreRequest = status.RestoreRequestID
	r.cancelFn = func() { cancel(nil) }
	r.restoreStopFn = func() { cancel(errOperatorRestoreStop) }
	if status.RestoreRequestID != "" {
		r.cancelFn = func() { cancel(errOperatorRestoreStop) }
	}
	r.setRunStatus(status)
}

// StopRestoreRun records and signals a stop for the exact active restore run,
// approved or started from the restore wizard.
// The worker records the actual outcome when the engine returns.
func (r *Runner) StopRestoreRun(ctx context.Context, runID int64, operator string) (bool, error) {
	r.cancelMu.Lock()
	if r.cancelFn == nil || r.cancellingRunID != runID {
		r.cancelMu.Unlock()
		return false, nil
	}
	if r.cancellingRestoreRequest == "" {
		return r.stopWizardRestoreLocked(ctx, runID, operator)
	}
	if r.restoreStopRequested {
		r.cancelMu.Unlock()
		return true, nil
	}
	recorded, err := r.db.RecordApprovedRestoreStop(ctx, r.cancellingRestoreRequest, runID, time.Now().UTC(), operator)
	if err != nil || !recorded {
		r.cancelMu.Unlock()
		return false, err
	}
	r.restoreStopRequested = true
	jobID := r.cancellingJobID
	r.statusMu.Lock()
	r.currentRun.Cancelling = true
	r.statusMu.Unlock()
	r.cancelFn()
	r.cancelMu.Unlock()

	r.broadcast(map[string]any{
		"type": "job_cancelling", "job_id": jobID, "run_id": runID, "run_type": "restore",
	})
	return true, nil
}

// stopWizardRestoreLocked stops the exact active restore run that no Restore
// Request approved, such as one the restore wizard started (#701). The caller
// holds cancelMu; it is released here. A backup run that happens to own the
// cancel function is never stopped, because only a restore's run ID matches.
func (r *Runner) stopWizardRestoreLocked(ctx context.Context, runID int64, operator string) (bool, error) {
	r.statusMu.RLock()
	isRestore := r.currentRun != nil && r.currentRun.RunID == runID && r.currentRun.RunType == "restore"
	cancelling := isRestore && r.currentRun.Cancelling
	r.statusMu.RUnlock()
	if !isRestore || r.restoreStopFn == nil {
		r.cancelMu.Unlock()
		return false, nil
	}
	// A stop already requested, or a plain Cancel already under way, ends the
	// run; recording an operator stop now would claim a stop that didn't decide
	// the run's outcome.
	if r.restoreStopRequested || cancelling {
		r.cancelMu.Unlock()
		return true, nil
	}
	recorded, err := r.db.RecordRestoreRunStop(ctx, runID, time.Now().UTC(), operator)
	if err != nil || !recorded {
		r.cancelMu.Unlock()
		return false, err
	}
	r.restoreStopRequested = true
	jobID := r.cancellingJobID
	// Mark the operator cancel before waking the worker, as CancelJob does, so
	// it is not retried as a stall. The operator-stop cause records the run as
	// cancelled and skips the items not yet started.
	r.statusMu.Lock()
	r.currentRun.Cancelling = true
	r.statusMu.Unlock()
	r.restoreStopFn()
	r.cancelMu.Unlock()

	r.broadcast(map[string]any{
		"type": "job_cancelling", "job_id": jobID, "run_id": runID, "run_type": "restore",
	})
	return true, nil
}
