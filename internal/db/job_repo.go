package db

import (
	"database/sql"
	"fmt"
)

func (d *DB) CreateJob(job Job) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO jobs (name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, storage_dest_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Name, job.Description, job.Enabled, job.Schedule, job.BackupTypeChain,
		job.RetentionCount, job.RetentionDays, job.Compression, job.Encryption, job.ContainerMode,
		job.VMMode, job.PreScript, job.PostScript, job.NotifyOn, job.VerifyBackup, job.StorageDestID,
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
		post_script, notify_on, verify_backup, storage_dest_id, COALESCE(source_id, 0), created_at, updated_at
		FROM jobs WHERE id = ?`, id,
	).Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.Schedule,
		&job.BackupTypeChain, &job.RetentionCount, &job.RetentionDays, &job.Compression,
		&job.Encryption, &job.ContainerMode, &job.VMMode, &job.PreScript, &job.PostScript, &job.NotifyOn,
		&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return job, ErrNotFound
	}
	return job, err
}

func (d *DB) ListJobs() ([]Job, error) {
	rows, err := d.Query(
		`SELECT id, name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, storage_dest_id, COALESCE(source_id, 0), created_at, updated_at
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
			&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.CreatedAt, &job.UpdatedAt); err != nil {
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
		post_script=?, notify_on=?, verify_backup=?, storage_dest_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		job.Name, job.Description, job.Enabled, job.Schedule, job.BackupTypeChain,
		job.RetentionCount, job.RetentionDays, job.Compression, job.Encryption, job.ContainerMode,
		job.VMMode, job.PreScript, job.PostScript, job.NotifyOn, job.VerifyBackup, job.StorageDestID, job.ID,
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
		post_script, notify_on, verify_backup, storage_dest_id, COALESCE(source_id, 0), created_at, updated_at
		FROM jobs WHERE name = ?`, name,
	).Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.Schedule,
		&job.BackupTypeChain, &job.RetentionCount, &job.RetentionDays, &job.Compression,
		&job.Encryption, &job.ContainerMode, &job.VMMode, &job.PreScript, &job.PostScript, &job.NotifyOn,
		&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.CreatedAt, &job.UpdatedAt)
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
	rows, err := d.Query("SELECT id, job_id, item_type, item_name, item_id, settings, sort_order FROM job_items WHERE job_id = ? ORDER BY sort_order ASC", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []JobItem
	for rows.Next() {
		var item JobItem
		if err := rows.Scan(&item.ID, &item.JobID, &item.ItemType, &item.ItemName, &item.ItemID, &item.Settings, &item.SortOrder); err != nil {
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

// Job Runs

func (d *DB) CreateJobRun(run JobRun) (int64, error) {
	runType := run.RunType
	if runType == "" {
		runType = "backup"
	}
	res, err := d.Exec(
		"INSERT INTO job_runs (job_id, status, backup_type, run_type, items_total) VALUES (?, ?, ?, ?, ?)",
		run.JobID, run.Status, run.BackupType, runType, run.ItemsTotal,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateJobRun(run JobRun) error {
	_, err := d.Exec(
		`UPDATE job_runs SET status=?, completed_at=CURRENT_TIMESTAMP, log=?,
		items_done=?, items_failed=?, size_bytes=? WHERE id=?`,
		run.Status, run.Log, run.ItemsDone, run.ItemsFailed, run.SizeBytes, run.ID,
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
		CASE WHEN completed_at IS NOT NULL THEN CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER) ELSE NULL END
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
			&run.ItemsDone, &run.ItemsFailed, &run.SizeBytes, &run.DurationSeconds); err != nil {
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
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
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
			&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt); err != nil {
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
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
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
			&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt); err != nil {
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
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
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
			&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt); err != nil {
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
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
		FROM restore_points WHERE id = ?`, id,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt)
	return rp, err
}

// GetLastRestorePointByType returns the most recent restore point of the given
// backup type for a job. Returns sql.ErrNoRows if none exist.
func (d *DB) GetLastRestorePointByType(jobID int64, backupType string) (RestorePoint, error) {
	var rp RestorePoint
	err := d.QueryRow(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
		FROM restore_points WHERE job_id = ? AND backup_type = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, jobID, backupType,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt)
	return rp, err
}

// GetLastRestorePoint returns the most recent restore point for a job,
// regardless of type. Returns sql.ErrNoRows if none exist.
func (d *DB) GetLastRestorePoint(jobID int64) (RestorePoint, error) {
	var rp RestorePoint
	err := d.QueryRow(
		`SELECT id, job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
			COALESCE(parent_restore_point_id, 0), COALESCE(source_id, 0), created_at
		FROM restore_points WHERE job_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, jobID,
	).Scan(&rp.ID, &rp.JobRunID, &rp.JobID, &rp.BackupType,
		&rp.StoragePath, &rp.Metadata, &rp.SizeBytes,
		&rp.ParentRestorePointID, &rp.SourceID, &rp.CreatedAt)
	return rp, err
}
