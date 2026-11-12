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
	if status.RestoreRequestID != "" {
		r.cancelFn = func() { cancel(errOperatorRestoreStop) }
	}
	r.setRunStatus(status)
}

// StopRestoreRun records and signals a stop for the exact active approved run.
// The worker records the actual outcome when the engine returns.
func (r *Runner) StopRestoreRun(ctx context.Context, runID int64, operator string) (bool, error) {
	r.cancelMu.Lock()
	if r.cancelFn == nil || r.cancellingRunID != runID || r.cancellingRestoreRequest == "" {
		r.cancelMu.Unlock()
		return false, nil
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
