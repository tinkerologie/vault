package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type RestoreApproval struct {
	RequestID    string
	RunID        int64
	ApprovedAt   time.Time
	OperatorName string
	ReviewDigest string
}

func (d *DB) GetRestoreApproval(ctx context.Context, requestID string) (RestoreApproval, error) {
	var approval RestoreApproval
	var timestamp int64
	err := d.QueryRowContext(ctx, `SELECT run_id, approved_at, operator_name, review_digest
		FROM restore_request_approvals WHERE request_id = ?`, requestID).
		Scan(&approval.RunID, &timestamp, &approval.OperatorName, &approval.ReviewDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return RestoreApproval{}, ErrNotFound
	}
	approval.RequestID = requestID
	approval.ApprovedAt = time.Unix(0, timestamp).UTC()
	return approval, err
}

// ClaimRestoreApproval atomically binds a pending request to one tracked run.
// BeginTx uses SQLite's immediate transactions, including across DB handles.
func (d *DB) ClaimRestoreApproval(ctx context.Context, approval RestoreApproval, run JobRun, now func() time.Time) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	approval.ApprovedAt = now().UTC()
	changed, err := restoreRequestsChanged(tx.ExecContext(ctx, `UPDATE restore_requests SET status = 'running'
		WHERE id = ? AND status = 'pending' AND expires_at > ?`,
		approval.RequestID, approval.ApprovedAt.UnixNano()))
	if err != nil || !changed {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO job_runs (job_id, status, backup_type, run_type, items_total)
		VALUES (?, 'running', ?, 'restore', ?)`, run.JobID, run.BackupType, run.ItemsTotal)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO restore_request_approvals
		(request_id, run_id, approved_at, operator_name, review_digest) VALUES (?, ?, ?, ?, ?)`,
		approval.RequestID, id, approval.ApprovedAt.UnixNano(), approval.OperatorName, approval.ReviewDigest); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (d *DB) InvalidateRestoreRequest(ctx context.Context, id string, now time.Time) (bool, error) {
	return restoreRequestsChanged(d.ExecContext(ctx, `UPDATE restore_requests
		SET status = 'invalidated', terminal_at = ?, terminal_reason = 'reviewed_operation_changed'
		WHERE id = ? AND status = 'pending' AND expires_at > ?`, now.UnixNano(), id, now.UnixNano()))
}

var errApprovedRestoreInactive = errors.New("linked Restore Request and Run are no longer active")

// FinishApprovedRestore retries a failed transaction as a unit. Rewriting
// either row alone could leave the run and its request with different outcomes.
func (d *DB) FinishApprovedRestore(ctx context.Context, requestID string, run JobRun, requestStatus, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, delay := range []time.Duration{25 * time.Millisecond, 50 * time.Millisecond} {
		err := d.finishApprovedRestoreOnce(ctx, requestID, run, requestStatus, reason)
		if err == nil || errors.Is(err, errApprovedRestoreInactive) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(delay):
		}
	}
	return d.finishApprovedRestoreOnce(ctx, requestID, run, requestStatus, reason)
}

func (d *DB) finishApprovedRestoreOnce(ctx context.Context, requestID string, run JobRun, requestStatus, reason string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := restoreRequestsChanged(tx.ExecContext(ctx, `UPDATE restore_requests
		SET status = ?, terminal_at = ?, terminal_reason = ?
		WHERE id = ? AND status = 'running'
		AND EXISTS (SELECT 1 FROM restore_request_approvals WHERE request_id = ? AND run_id = ?)`,
		requestStatus, time.Now().UnixNano(), reason, requestID, requestID, run.ID))
	if err != nil {
		return err
	}
	if !changed {
		return errApprovedRestoreInactive
	}
	changed, err = restoreRequestsChanged(tx.ExecContext(ctx, `UPDATE job_runs
		SET status = ?, completed_at = CURRENT_TIMESTAMP, log = ?, items_done = ?, items_failed = ?, size_bytes = ?,
			stop_requested_at = ?, stopped_by = ?
		WHERE id = ? AND status = 'running'`, run.Status, run.Log, run.ItemsDone, run.ItemsFailed, run.SizeBytes,
		run.StopRequestedAt, run.StoppedBy, run.ID))
	if err != nil {
		return err
	}
	if !changed {
		return errApprovedRestoreInactive
	}
	return tx.Commit()
}

func (d *DB) cleanupStaleRuns(ctx context.Context) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	const restoreInterrupted = "Interrupted: daemon restarted while restore was running; destination may be partially restored"
	if _, err := tx.ExecContext(ctx, `UPDATE restore_requests
		SET status = 'interrupted', terminal_at = ?, terminal_reason = 'daemon_restarted'
		WHERE status = 'running' AND id IN (SELECT request_id FROM restore_request_approvals)`, time.Now().UnixNano()); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_log_entries (run_id, level, message)
		SELECT id, 'error', ? FROM job_runs WHERE status = 'running'
		AND id IN (SELECT run_id FROM restore_request_approvals)`, restoreInterrupted); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE job_runs SET status = 'failed', completed_at = CURRENT_TIMESTAMP,
		log = CASE WHEN run_type = 'restore' THEN ? ELSE 'Interrupted: daemon was restarted while backup was running' END
		WHERE status = 'running'`, restoreInterrupted)
	if err != nil {
		return 0, fmt.Errorf("cleaning up stale runs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (d *DB) GetApprovedRestoreRunStatus(ctx context.Context, runID int64) (string, error) {
	var status string
	err := d.QueryRowContext(ctx, `SELECT CASE WHEN q.status = 'interrupted' THEN q.status ELSE COALESCE(r.status, q.status) END
		FROM restore_request_approvals a JOIN restore_requests q ON q.id = a.request_id
		LEFT JOIN job_runs r ON r.id = a.run_id WHERE a.run_id = ?`, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}
