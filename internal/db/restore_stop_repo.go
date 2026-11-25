package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RecordRestoreRunStop saves the first stop audit for an active restore run
// that no Restore Request approved, such as one started from the restore
// wizard (#701). It changes neither the run's status nor its outcome.
func (d *DB) RecordRestoreRunStop(ctx context.Context, runID int64, requestedAt time.Time, operator string) (bool, error) {
	result, err := d.ExecContext(ctx, `UPDATE job_runs
		SET stop_requested_at = ?, stopped_by = ?
		WHERE id = ? AND run_type = 'restore' AND status = 'running' AND stop_requested_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM restore_request_approvals WHERE run_id = job_runs.id)`,
		requestedAt, operator, runID)
	if err != nil {
		return false, fmt.Errorf("recording stop for Restore Run %d: %w", runID, err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// GetRestoreRunStatus returns the recorded status of a restore run, or
// ErrNotFound when the run does not exist or is not a restore.
func (d *DB) GetRestoreRunStatus(ctx context.Context, runID int64) (string, error) {
	var status string
	err := d.QueryRowContext(ctx, `SELECT status FROM job_runs WHERE id = ? AND run_type = 'restore'`, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("loading Restore Run %d status: %w", runID, err)
	}
	return status, nil
}
