package db

import (
	"database/sql"
	"fmt"
)

// CreateVerifyRun inserts a new verify run in "running" state and returns
// its ID. Callers update it via UpdateVerifyRunProgress while the run is
// in flight and FinishVerifyRun on the terminal transition.
func (d *DB) CreateVerifyRun(restorePointID int64, mode string) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO verify_runs (restore_point_id, mode, status) VALUES (?, ?, 'running')`,
		restorePointID, mode,
	)
	if err != nil {
		return 0, fmt.Errorf("creating verify run: %w", err)
	}
	return res.LastInsertId()
}

// UpdateVerifyRunProgress updates the in-flight counters without setting
// completed_at. Called periodically by the verifier so the API + WS UI can
// stream progress.
func (d *DB) UpdateVerifyRunProgress(id int64, filesChecked, filesFailed int, bytesRead int64) error {
	_, err := d.Exec(
		`UPDATE verify_runs SET files_checked=?, files_failed=?, bytes_read=? WHERE id=?`,
		filesChecked, filesFailed, bytesRead, id,
	)
	return err
}

// FinishVerifyRun records the terminal status and completion timestamp.
// Any error_summary is truncated to a sane size (4 KiB) to keep the DB
// row small.
func (d *DB) FinishVerifyRun(id int64, status, errorSummary string) error {
	const maxSummary = 4096
	if len(errorSummary) > maxSummary {
		errorSummary = errorSummary[:maxSummary]
	}
	_, err := d.Exec(
		`UPDATE verify_runs SET status=?, completed_at=CURRENT_TIMESTAMP, error_summary=? WHERE id=?`,
		status, errorSummary, id,
	)
	return err
}

// GetVerifyRun returns one verify run by ID.
func (d *DB) GetVerifyRun(id int64) (VerifyRun, error) {
	var v VerifyRun
	err := d.QueryRow(
		`SELECT id, restore_point_id, mode, status, files_checked, files_failed, bytes_read,
		started_at, completed_at, COALESCE(error_summary, '')
		FROM verify_runs WHERE id = ?`, id,
	).Scan(&v.ID, &v.RestorePointID, &v.Mode, &v.Status, &v.FilesChecked, &v.FilesFailed,
		&v.BytesRead, &v.StartedAt, &v.CompletedAt, &v.ErrorSummary)
	if err == sql.ErrNoRows {
		return v, ErrNotFound
	}
	return v, err
}

// ListRecentVerifyRuns returns the most recent verify runs across all
// restore points, newest first. Used by the diagnostics collector to
// embed verification history in the support bundle so reports show
// whether scheduled verify runs have been failing.
func (d *DB) ListRecentVerifyRuns(limit int) ([]VerifyRun, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := d.Query(
		`SELECT id, restore_point_id, mode, status, files_checked, files_failed, bytes_read,
		started_at, completed_at, COALESCE(error_summary, '')
		FROM verify_runs
		ORDER BY started_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := make([]VerifyRun, 0, limit)
	for rows.Next() {
		var v VerifyRun
		if err := rows.Scan(&v.ID, &v.RestorePointID, &v.Mode, &v.Status, &v.FilesChecked,
			&v.FilesFailed, &v.BytesRead, &v.StartedAt, &v.CompletedAt, &v.ErrorSummary); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListVerifyRunsForJob returns the most recent verify runs across all restore
// points that belong to the given job, newest first. This is used by the
// ReliabilityDetector to detect a pass→fail regression in scheduled
// verification runs.
//
// Verify runs are linked to jobs indirectly:
//
//	verify_runs.restore_point_id → restore_points.id → restore_points.job_id
//
// Status values observed in practice: "running", "passed", "failed",
// "cancelled". The detector compares the newest two completed (non-running)
// runs to detect a regression.
func (d *DB) ListVerifyRunsForJob(jobID int64, limit int) ([]VerifyRun, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.Query(
		`SELECT vr.id, vr.restore_point_id, vr.mode, vr.status,
		        vr.files_checked, vr.files_failed, vr.bytes_read,
		        vr.started_at, vr.completed_at, COALESCE(vr.error_summary, '')
		FROM verify_runs vr
		JOIN restore_points rp ON rp.id = vr.restore_point_id
		WHERE rp.job_id = ?
		ORDER BY vr.started_at DESC LIMIT ?`,
		jobID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := make([]VerifyRun, 0, limit)
	for rows.Next() {
		var v VerifyRun
		if err := rows.Scan(&v.ID, &v.RestorePointID, &v.Mode, &v.Status,
			&v.FilesChecked, &v.FilesFailed, &v.BytesRead,
			&v.StartedAt, &v.CompletedAt, &v.ErrorSummary); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListVerifyRunsForRestorePoint returns the most recent verify runs for a
// given restore point, newest first.
func (d *DB) ListVerifyRunsForRestorePoint(restorePointID int64, limit int) ([]VerifyRun, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.Query(
		`SELECT id, restore_point_id, mode, status, files_checked, files_failed, bytes_read,
		started_at, completed_at, COALESCE(error_summary, '')
		FROM verify_runs WHERE restore_point_id = ?
		ORDER BY started_at DESC LIMIT ?`,
		restorePointID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifyRun
	for rows.Next() {
		var v VerifyRun
		if err := rows.Scan(&v.ID, &v.RestorePointID, &v.Mode, &v.Status, &v.FilesChecked,
			&v.FilesFailed, &v.BytesRead, &v.StartedAt, &v.CompletedAt, &v.ErrorSummary); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
