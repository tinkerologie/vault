package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// nullableID maps the sentinel 0 ("no storage destination") to a SQL NULL so
// writes satisfy the storage_dest_id foreign key. Orphaned jobs (whose
// destination was deleted — issue #113) carry a 0 here and must round-trip
// without re-introducing a FK violation. Reads map NULL back to 0 via COALESCE.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (d *DB) CreateJob(job Job) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO jobs (name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, storage_dest_id, defer_remote_upload,
		keep_latest, keep_daily, keep_weekly, keep_monthly, keep_yearly,
		verify_schedule, verify_mode,
		retry_max_override, retry_delays_override,
		max_parallel_uploads)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Name, job.Description, job.Enabled, job.Schedule, job.BackupTypeChain,
		job.RetentionCount, job.RetentionDays, job.Compression, job.Encryption, job.ContainerMode,
		job.VMMode, job.PreScript, job.PostScript, job.NotifyOn, job.VerifyBackup, nullableID(job.StorageDestID),
		job.DeferRemoteUpload,
		job.KeepLatest, job.KeepDaily, job.KeepWeekly, job.KeepMonthly, job.KeepYearly,
		job.VerifySchedule, job.VerifyMode,
		job.RetryMaxOverride, job.RetryDelaysOverride,
		job.MaxParallelUploads,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetJob(id int64) (Job, error) {
	var job Job
	err := d.QueryRow(
		`SELECT id, name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, COALESCE(storage_dest_id, 0), COALESCE(source_id, 0),
		COALESCE(defer_remote_upload, 0),
		COALESCE(keep_latest, 0), COALESCE(keep_daily, 0), COALESCE(keep_weekly, 0),
		COALESCE(keep_monthly, 0), COALESCE(keep_yearly, 0),
		COALESCE(verify_schedule, ''), COALESCE(verify_mode, 'quick'),
		retry_max_override, retry_delays_override,
		COALESCE(anomaly_sensitivity, ''),
		COALESCE(max_parallel_uploads, 1),
		created_at, updated_at
		FROM jobs WHERE id = ?`, id,
	).Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.Schedule,
		&job.BackupTypeChain, &job.RetentionCount, &job.RetentionDays, &job.Compression,
		&job.Encryption, &job.ContainerMode, &job.VMMode, &job.PreScript, &job.PostScript, &job.NotifyOn,
		&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.DeferRemoteUpload,
		&job.KeepLatest, &job.KeepDaily, &job.KeepWeekly, &job.KeepMonthly, &job.KeepYearly,
		&job.VerifySchedule, &job.VerifyMode,
		&job.RetryMaxOverride, &job.RetryDelaysOverride,
		&job.AnomalySensitivity,
		&job.MaxParallelUploads,
		&job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return job, ErrNotFound
	}
	return job, err
}

func (d *DB) ListJobs() ([]Job, error) {
	rows, err := d.Query(
		`SELECT id, name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, COALESCE(storage_dest_id, 0), COALESCE(source_id, 0),
		COALESCE(defer_remote_upload, 0),
		COALESCE(keep_latest, 0), COALESCE(keep_daily, 0), COALESCE(keep_weekly, 0),
		COALESCE(keep_monthly, 0), COALESCE(keep_yearly, 0),
		COALESCE(verify_schedule, ''), COALESCE(verify_mode, 'quick'),
		retry_max_override, retry_delays_override,
		COALESCE(anomaly_sensitivity, ''),
		COALESCE(max_parallel_uploads, 1),
		created_at, updated_at
		FROM jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.Schedule,
			&job.BackupTypeChain, &job.RetentionCount, &job.RetentionDays, &job.Compression,
			&job.Encryption, &job.ContainerMode, &job.VMMode, &job.PreScript, &job.PostScript, &job.NotifyOn,
			&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.DeferRemoteUpload,
			&job.KeepLatest, &job.KeepDaily, &job.KeepWeekly, &job.KeepMonthly, &job.KeepYearly,
			&job.VerifySchedule, &job.VerifyMode,
			&job.RetryMaxOverride, &job.RetryDelaysOverride,
			&job.AnomalySensitivity,
			&job.MaxParallelUploads,
			&job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (d *DB) UpdateJob(job Job) error {
	_, err := d.Exec(
		`UPDATE jobs SET name=?, description=?, enabled=?, schedule=?, backup_type_chain=?,
		retention_count=?, retention_days=?, compression=?, encryption=?, container_mode=?, vm_mode=?, pre_script=?,
		post_script=?, notify_on=?, verify_backup=?, storage_dest_id=?, defer_remote_upload=?,
		keep_latest=?, keep_daily=?, keep_weekly=?, keep_monthly=?, keep_yearly=?,
		verify_schedule=?, verify_mode=?,
		retry_max_override=?, retry_delays_override=?,
		anomaly_sensitivity=?,
		max_parallel_uploads=?,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		job.Name, job.Description, job.Enabled, job.Schedule, job.BackupTypeChain,
		job.RetentionCount, job.RetentionDays, job.Compression, job.Encryption, job.ContainerMode,
		job.VMMode, job.PreScript, job.PostScript, job.NotifyOn, job.VerifyBackup, nullableID(job.StorageDestID),
		job.DeferRemoteUpload,
		job.KeepLatest, job.KeepDaily, job.KeepWeekly, job.KeepMonthly, job.KeepYearly,
		job.VerifySchedule, job.VerifyMode,
		job.RetryMaxOverride, job.RetryDelaysOverride,
		job.AnomalySensitivity,
		job.MaxParallelUploads,
		job.ID,
	)
	return err
}

func (d *DB) DeleteJob(id int64) error {
	_, err := d.Exec("DELETE FROM jobs WHERE id = ?", id)
	return err
}

// GetJobByName looks up a job by its unique name. Returns ErrNotFound if
// no matching job exists.
func (d *DB) GetJobByName(name string) (Job, error) {
	var job Job
	err := d.QueryRow(
		`SELECT id, name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, COALESCE(storage_dest_id, 0), COALESCE(source_id, 0),
		COALESCE(defer_remote_upload, 0),
		COALESCE(keep_latest, 0), COALESCE(keep_daily, 0), COALESCE(keep_weekly, 0),
		COALESCE(keep_monthly, 0), COALESCE(keep_yearly, 0),
		COALESCE(verify_schedule, ''), COALESCE(verify_mode, 'quick'),
		retry_max_override, retry_delays_override,
		COALESCE(anomaly_sensitivity, ''),
		COALESCE(max_parallel_uploads, 1),
		created_at, updated_at
		FROM jobs WHERE name = ?`, name,
	).Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.Schedule,
		&job.BackupTypeChain, &job.RetentionCount, &job.RetentionDays, &job.Compression,
		&job.Encryption, &job.ContainerMode, &job.VMMode, &job.PreScript, &job.PostScript, &job.NotifyOn,
		&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.DeferRemoteUpload,
		&job.KeepLatest, &job.KeepDaily, &job.KeepWeekly, &job.KeepMonthly, &job.KeepYearly,
		&job.VerifySchedule, &job.VerifyMode,
		&job.RetryMaxOverride, &job.RetryDelaysOverride,
		&job.AnomalySensitivity,
		&job.MaxParallelUploads,
		&job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return job, ErrNotFound
	}
	return job, err
}

func (d *DB) AddJobItem(item JobItem) (int64, error) {
	res, err := d.Exec(
		"INSERT INTO job_items (job_id, item_type, item_name, item_id, settings, sort_order) VALUES (?, ?, ?, ?, ?, ?)",
		item.JobID, item.ItemType, item.ItemName, item.ItemID, item.Settings, item.SortOrder,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetJobItems(jobID int64) ([]JobItem, error) {
	rows, err := d.Query("SELECT id, job_id, item_type, item_name, item_id, settings, sort_order, missing_since FROM job_items WHERE job_id = ? ORDER BY sort_order ASC", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []JobItem
	for rows.Next() {
		var item JobItem
		if err := rows.Scan(&item.ID, &item.JobID, &item.ItemType, &item.ItemName, &item.ItemID, &item.Settings, &item.SortOrder, &item.MissingSince); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d *DB) DeleteJobItems(jobID int64) error {
	_, err := d.Exec("DELETE FROM job_items WHERE job_id = ?", jobID)
	return err
}

// DeleteJobItem removes a single job item by its primary key. Used by the
// stale-item remediation endpoint; does not touch restore points (those
// reference the job, not the item, so existing backups stay restorable).
func (d *DB) DeleteJobItem(id int64) error {
	_, err := d.Exec("DELETE FROM job_items WHERE id = ?", id)
	return err
}

// DeleteJobItemsByIDs removes several job items by primary key in one
// statement. No-op for an empty slice.
func (d *DB) DeleteJobItemsByIDs(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	// aikido-ignore-next-line AIK_go_G202 -- placeholders are constant "?" tokens; the id values are bound via args and are never interpolated into the SQL string.
	q := "DELETE FROM job_items WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := d.Exec(q, args...)
	return err
}

// MarkJobItemsMissing stamps missing_since=ts on the given items (only those
// not already marked, so the original detection time is preserved). No-op for
// an empty slice.
func (d *DB) MarkJobItemsMissing(ids []int64, ts string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, ts)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	// aikido-ignore-next-line AIK_go_G202 -- placeholders are constant "?" tokens; ts and the id values are bound via args and are never interpolated into the SQL string.
	q := "UPDATE job_items SET missing_since = ? WHERE missing_since IS NULL AND id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := d.Exec(q, args...)
	return err
}

// ClearJobItemsMissing resets missing_since to NULL on the given items (used
// when a previously-missing item reappears). No-op for an empty slice.
func (d *DB) ClearJobItemsMissing(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	// aikido-ignore-next-line AIK_go_G202 -- placeholders are constant "?" tokens; the id values are bound via args and are never interpolated into the SQL string.
	q := "UPDATE job_items SET missing_since = NULL WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := d.Exec(q, args...)
	return err
}

// Job Runs

func (d *DB) CreateJobRun(run JobRun) (int64, error) {
	runType := run.RunType
	if runType == "" {
		runType = "backup"
	}
	res, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, run_type, items_total,
			retry_of_run_id, retry_attempt)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.JobID, run.Status, run.BackupType, runType, run.ItemsTotal,
		run.RetryOfRunID, run.RetryAttempt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateImportedJobRun creates a synthetic JobRun for a backup imported
// from storage, preserving the original backup timestamp on both
// started_at and completed_at so the History page reflects when the
// backup actually ran (not when it was imported). All counters
// (items_total/done/failed and size_bytes) are populated from the
// manifest in a single statement so the run never appears in-progress.
func (d *DB) CreateImportedJobRun(run JobRun, ts time.Time) (int64, error) {
	runType := run.RunType
	if runType == "" {
		runType = "backup"
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	res, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, run_type, items_total,
			items_done, items_failed, size_bytes, started_at, completed_at,
			retry_of_run_id, retry_attempt)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.JobID, run.Status, run.BackupType, runType, run.ItemsTotal,
		run.ItemsDone, run.ItemsFailed, run.SizeBytes, ts.UTC(), ts.UTC(),
		run.RetryOfRunID, run.RetryAttempt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateJobRun(run JobRun) error {
	_, err := d.Exec(
		`UPDATE job_runs SET status=?, completed_at=CURRENT_TIMESTAMP, log=?,
		items_done=?, items_failed=?, size_bytes=?, retry_next_at=? WHERE id=?`,
		run.Status, run.Log, run.ItemsDone, run.ItemsFailed, run.SizeBytes,
		run.RetryNextAt, run.ID,
	)
	return err
}

// UpdateJobRunProgress updates in-flight progress counters without
// setting completed_at. This is called after each item finishes so the
// UI can show real-time progress.
func (d *DB) UpdateJobRunProgress(runID int64, done, failed int, sizeBytes int64) error {
	_, err := d.Exec(
		`UPDATE job_runs SET items_done=?, items_failed=?, size_bytes=? WHERE id=?`,
		done, failed, sizeBytes, runID,
	)
	return err
}

// CleanupStaleRuns marks any "running" job runs as "failed". This handles
// runs that were interrupted by a daemon restart or crash.
func (d *DB) CleanupStaleRuns() (int64, error) {
	res, err := d.Exec(
		`UPDATE job_runs SET status='failed', completed_at=CURRENT_TIMESTAMP,
		log='Interrupted: daemon was restarted while backup was running'
		WHERE status='running'`,
	)
	if err != nil {
		return 0, fmt.Errorf("cleaning up stale runs: %w", err)
	}
	return res.RowsAffected()
}

// DeleteOldFailedRuns removes failed/error job runs older than keepDays.
func (d *DB) DeleteOldFailedRuns(keepDays int) (int64, error) {
	res, err := d.Exec(
		`DELETE FROM job_runs WHERE status IN ('failed', 'error')
		AND completed_at IS NOT NULL
		AND completed_at < datetime('now', '-' || ? || ' days')`,
		keepDays,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeEligibleRuns removes finished job runs older than keepDays that no
// longer have any restore point — i.e. run history whose backups were already
// trimmed by the job's own retention. Runs that still back a restore point
// (recoverable backups) are never touched here. keepDays <= 0 is a no-op.
func (d *DB) PurgeEligibleRuns(keepDays int) (int64, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	res, err := d.Exec(
		`DELETE FROM job_runs
		 WHERE status != 'running'
		   AND completed_at IS NOT NULL
		   AND completed_at < datetime('now', '-' || ? || ' days')
		   AND id NOT IN (SELECT DISTINCT job_run_id FROM restore_points)`,
		keepDays,
	)
	if err != nil {
		return 0, fmt.Errorf("purging eligible runs: %w", err)
	}
	return res.RowsAffected()
}

// ListRecentRuns returns the most recent job runs across all jobs.
func (d *DB) ListRecentRuns(limit int) ([]JobRun, error) {
	rows, err := d.Query(
		`SELECT id, job_id, status, backup_type, COALESCE(run_type, 'backup'), started_at, completed_at, log,
		items_total, items_done, items_failed, size_bytes,
		CASE WHEN completed_at IS NOT NULL THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER) ELSE NULL END,
		retry_of_run_id, COALESCE(retry_attempt, 0), retry_next_at
		FROM job_runs ORDER BY started_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []JobRun
	for rows.Next() {
		var run JobRun
		if err := rows.Scan(&run.ID, &run.JobID, &run.Status, &run.BackupType,
			&run.RunType, &run.StartedAt, &run.CompletedAt, &run.Log, &run.ItemsTotal,
			&run.ItemsDone, &run.ItemsFailed, &run.SizeBytes, &run.DurationSeconds,
			&run.RetryOfRunID, &run.RetryAttempt, &run.RetryNextAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// GetJobRun returns a single job run row by primary key.
// Returns ErrNotFound when no row matches.
func (d *DB) GetJobRun(id int64) (JobRun, error) {
	var run JobRun
	err := d.QueryRow(
		`SELECT id, job_id, status, backup_type, COALESCE(run_type, 'backup'), started_at, completed_at, log,
		items_total, items_done, items_failed, size_bytes,
		CASE WHEN completed_at IS NOT NULL THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER) ELSE NULL END,
		retry_of_run_id, COALESCE(retry_attempt, 0), retry_next_at
		FROM job_runs WHERE id = ?`, id,
	).Scan(&run.ID, &run.JobID, &run.Status, &run.BackupType,
		&run.RunType, &run.StartedAt, &run.CompletedAt, &run.Log, &run.ItemsTotal,
		&run.ItemsDone, &run.ItemsFailed, &run.SizeBytes, &run.DurationSeconds,
		&run.RetryOfRunID, &run.RetryAttempt, &run.RetryNextAt)
	if err == sql.ErrNoRows {
		return run, ErrNotFound
	}
	return run, err
}

// PurgeJobRuns deletes all job run records and returns the count of deleted rows.
func (d *DB) PurgeJobRuns() (int64, error) {
	res, err := d.Exec("DELETE FROM job_runs")
	if err != nil {
		return 0, fmt.Errorf("purging job runs: %w", err)
	}
	return res.RowsAffected()
}

func (d *DB) GetJobRuns(jobID int64, limit int) ([]JobRun, error) {
	rows, err := d.Query(
		`SELECT id, job_id, status, backup_type, COALESCE(run_type, 'backup'), started_at, completed_at, log,
		items_total, items_done, items_failed, size_bytes,
		CASE WHEN completed_at IS NOT NULL THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER) ELSE NULL END,
		retry_of_run_id, COALESCE(retry_attempt, 0), retry_next_at
		FROM job_runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?`, jobID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []JobRun
	for rows.Next() {
		var run JobRun
		if err := rows.Scan(&run.ID, &run.JobID, &run.Status, &run.BackupType,
			&run.RunType, &run.StartedAt, &run.CompletedAt, &run.Log, &run.ItemsTotal,
			&run.ItemsDone, &run.ItemsFailed, &run.SizeBytes, &run.DurationSeconds,
			&run.RetryOfRunID, &run.RetryAttempt, &run.RetryNextAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// Restore Points

func (d *DB) CreateRestorePoint(rp RestorePoint) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO restore_points (job_run_id, job_id, backup_type, storage_path, metadata, size_bytes, parent_restore_point_id, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rp.JobRunID, rp.JobID, rp.BackupType, rp.StoragePath, rp.Metadata, rp.SizeBytes, rp.ParentRestorePointID, rp.SourceID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListRestorePoints(jobID int64) ([]RestorePoint, error) {
	rows, err := d.Query(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE job_id = ? ORDER BY created_at DESC`, jobID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rps []RestorePoint
	for rows.Next() {
		var rp RestorePoint
		if err := rows.Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
			&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
			&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt); err != nil {
			return nil, err
		}
		rps = append(rps, rp)
	}
	return rps, rows.Err()
}

func (d *DB) DeleteRestorePoint(id int64) error {
	_, err := d.Exec("DELETE FROM restore_points WHERE id = ?", id)
	return err
}

func (d *DB) DeleteOldRestorePoints(jobID int64, keepCount int) error {
	_, err := d.Exec(
		`DELETE FROM restore_points WHERE job_id = ? AND id NOT IN
		(SELECT id FROM restore_points WHERE job_id = ? ORDER BY created_at DESC LIMIT ?)`,
		jobID, jobID, keepCount,
	)
	return err
}

// GetOldRestorePoints returns restore points that exceed the keep count
// (i.e., the ones that would be deleted by retention). Returns them
// sorted oldest first.
func (d *DB) GetOldRestorePoints(jobID int64, keepCount int) ([]RestorePoint, error) {
	rows, err := d.Query(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE job_id = ? AND id NOT IN
		(SELECT id FROM restore_points WHERE job_id = ? ORDER BY created_at DESC LIMIT ?)
		ORDER BY created_at ASC`,
		jobID, jobID, keepCount,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rps []RestorePoint
	for rows.Next() {
		var rp RestorePoint
		if err := rows.Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
			&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
			&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt); err != nil {
			return nil, err
		}
		rps = append(rps, rp)
	}
	return rps, rows.Err()
}

// GetExpiredRestorePoints returns restore points older than the specified
// number of days, sorted oldest first.
func (d *DB) GetExpiredRestorePoints(jobID int64, olderThanDays int) ([]RestorePoint, error) {
	rows, err := d.Query(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE job_id = ? AND created_at < datetime('now', '-' || ? || ' days')
		ORDER BY created_at ASC`,
		jobID, olderThanDays,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rps []RestorePoint
	for rows.Next() {
		var rp RestorePoint
		if err := rows.Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
			&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
			&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt); err != nil {
			return nil, err
		}
		rps = append(rps, rp)
	}
	return rps, rows.Err()
}

// DeleteExpiredRestorePoints removes restore points older than the specified
// number of days.
func (d *DB) DeleteExpiredRestorePoints(jobID int64, olderThanDays int) error {
	_, err := d.Exec(
		"DELETE FROM restore_points WHERE job_id = ? AND created_at < datetime('now', '-' || ? || ' days')",
		jobID, olderThanDays,
	)
	return err
}

// GetRestorePoint returns a single restore point by ID.
func (d *DB) GetRestorePoint(id int64) (RestorePoint, error) {
	var rp RestorePoint
	err := d.QueryRow(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE id = ?`, id,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt)
	if err == sql.ErrNoRows {
		return rp, ErrNotFound
	}
	return rp, err
}

// GetLastRestorePointByType returns the most recent restore point of the given
// backup type for a job. Returns sql.ErrNoRows if none exist.
func (d *DB) GetLastRestorePointByType(jobID int64, backupType string) (RestorePoint, error) {
	var rp RestorePoint
	err := d.QueryRow(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE job_id = ? AND backup_type = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, jobID, backupType,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt)
	return rp, err
}

// GetLastRestorePoint returns the most recent restore point for a job,
// regardless of type. Returns sql.ErrNoRows if none exist.
func (d *DB) GetLastRestorePoint(jobID int64) (RestorePoint, error) {
	var rp RestorePoint
	err := d.QueryRow(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), manifest_id, created_at
		FROM restore_points WHERE job_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, jobID,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.ManifestID, &rp.CreatedAt)
	return rp, err
}

// SetRestorePointManifestID stores the manifest blob ID for a restore
// point. Used by the dedup engine to associate a restore point with its
// manifest (which lists the chunks comprising the backup).
func (d *DB) SetRestorePointManifestID(restorePointID int64, manifestID []byte) error {
	_, err := d.Exec(`UPDATE restore_points SET manifest_id = ? WHERE id = ?`, manifestID, restorePointID)
	return err
}

// DueRetry describes a job_run row that is eligible to be retried.
type DueRetry struct {
	OriginalRunID int64
	JobID         int64
	AttemptSoFar  int // the retry_attempt value of the original run
}

// ClaimDueRetries atomically grabs all job_runs whose retry_next_at has
// expired, clearing their retry_next_at so they cannot be claimed twice.
// Returns descriptors the scheduler can dispatch.
//
// Implementation: SELECT candidates, then UPDATE each one with a WHERE
// clause that requires retry_next_at to still be non-null. Only the
// caller that wins the race gets RowsAffected = 1.
func (d *DB) ClaimDueRetries() ([]DueRetry, error) {
	rows, err := d.Query(`
		SELECT id, job_id, COALESCE(retry_attempt, 0)
		  FROM job_runs
		 WHERE status = 'failed'
		   AND retry_next_at IS NOT NULL
		   AND retry_next_at <= CURRENT_TIMESTAMP
	`)
	if err != nil {
		return nil, fmt.Errorf("listing due retries: %w", err)
	}
	var candidates []DueRetry
	for rows.Next() {
		var dr DueRetry
		if err := rows.Scan(&dr.OriginalRunID, &dr.JobID, &dr.AttemptSoFar); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, dr)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	claimed := make([]DueRetry, 0, len(candidates))
	for _, c := range candidates {
		res, err := d.Exec(
			`UPDATE job_runs SET retry_next_at = NULL
			  WHERE id = ? AND retry_next_at IS NOT NULL`,
			c.OriginalRunID,
		)
		if err != nil {
			log.Printf("retry watcher: claim run %d: %v", c.OriginalRunID, err)
			continue
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			claimed = append(claimed, c)
		}
	}
	return claimed, nil
}
