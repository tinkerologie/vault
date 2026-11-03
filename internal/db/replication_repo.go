package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateReplicationSource inserts a new replication source and returns its ID.
func (d *DB) CreateReplicationSource(src ReplicationSource) (int64, error) {
	// Use NULL for storage_dest_id when 0 to satisfy the FK constraint.
	var destID any
	if src.StorageDestID != 0 {
		destID = src.StorageDestID
	}
	typ := src.Type
	if typ == "" {
		typ = "remote_vault"
	}
	cfg := src.Config
	if cfg == "" {
		cfg = "{}"
	}
	cfg, err := d.sealConfig(cfg)
	if err != nil {
		return 0, err
	}
	res, err := d.Exec(
		`INSERT INTO replication_sources (name, type, url, config, storage_dest_id, schedule, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		src.Name, typ, src.URL, cfg, destID, src.Schedule, src.Enabled,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetReplicationSource returns a single replication source by ID.
func (d *DB) GetReplicationSource(id int64) (ReplicationSource, error) {
	var src ReplicationSource
	err := d.QueryRow(
		`SELECT id, name, COALESCE(type, 'remote_vault'), url, COALESCE(config, '{}'),
			COALESCE(storage_dest_id, 0), schedule, enabled,
			last_sync_at, last_sync_status, last_sync_error,
			COALESCE(last_sync_jobs_synced, 0), COALESCE(last_sync_jobs_failed, 0),
			COALESCE(last_sync_restore_points, 0), COALESCE(last_sync_bytes, 0),
			last_sync_success_at, created_at, updated_at
		FROM replication_sources WHERE id = ?`, id,
	).Scan(&src.ID, &src.Name, &src.Type, &src.URL, &src.Config, &src.StorageDestID,
		&src.Schedule, &src.Enabled, &src.LastSyncAt, &src.LastSyncStatus,
		&src.LastSyncError, &src.LastSyncJobsSynced, &src.LastSyncJobsFailed,
		&src.LastSyncRestorePoints, &src.LastSyncBytes, &src.LastSyncSuccessAt,
		&src.CreatedAt, &src.UpdatedAt)
	if err == sql.ErrNoRows {
		return src, ErrNotFound
	}
	src.Config = d.openConfig(src.Config)
	return src, err
}

// ListReplicationSources returns all replication sources ordered by name.
func (d *DB) ListReplicationSources() ([]ReplicationSource, error) {
	rows, err := d.Query(
		`SELECT id, name, COALESCE(type, 'remote_vault'), url, COALESCE(config, '{}'),
			COALESCE(storage_dest_id, 0), schedule, enabled,
			last_sync_at, last_sync_status, last_sync_error,
			COALESCE(last_sync_jobs_synced, 0), COALESCE(last_sync_jobs_failed, 0),
			COALESCE(last_sync_restore_points, 0), COALESCE(last_sync_bytes, 0),
			last_sync_success_at, created_at, updated_at
		FROM replication_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []ReplicationSource
	for rows.Next() {
		var src ReplicationSource
		if err := rows.Scan(&src.ID, &src.Name, &src.Type, &src.URL, &src.Config, &src.StorageDestID,
			&src.Schedule, &src.Enabled, &src.LastSyncAt, &src.LastSyncStatus,
			&src.LastSyncError, &src.LastSyncJobsSynced, &src.LastSyncJobsFailed,
			&src.LastSyncRestorePoints, &src.LastSyncBytes, &src.LastSyncSuccessAt,
			&src.CreatedAt, &src.UpdatedAt); err != nil {
			return nil, err
		}
		src.Config = d.openConfig(src.Config)
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// ErrReplicationDestinationInUse refuses moving a replication source to
// another destination while its replicated jobs have restore points on the
// old one: their files stay there, and the jobs' destination follows the
// source, so restore would look in the wrong place and every sync would fail.
var ErrReplicationDestinationInUse = errors.New("this source's replicated jobs have restore points on its current storage destination; delete those jobs or create a new source before changing it")

// UpdateReplicationSource updates an existing replication source. Its
// replicated jobs follow its destination: those without restore points are
// rebound in the same transaction, and the change is refused with
// ErrReplicationDestinationInUse if any job has restore points elsewhere.
func (d *DB) UpdateReplicationSource(src ReplicationSource) error {
	var destID any
	if src.StorageDestID != 0 {
		destID = src.StorageDestID
	}
	cfg, err := d.sealForUpdate(src.Config, selectReplicationConfig, src.ID)
	if err != nil {
		return err
	}
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("update replication source %d: begin tx: %w", src.ID, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a successful Commit is a no-op
	var stranded int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM jobs j WHERE j.source_id = ?
		AND COALESCE(j.storage_dest_id, 0) != ?
		AND EXISTS (SELECT 1 FROM restore_points rp WHERE rp.job_id = j.id)`,
		src.ID, src.StorageDestID).Scan(&stranded); err != nil {
		return fmt.Errorf("update replication source %d: check replicated jobs: %w", src.ID, err)
	}
	if stranded > 0 {
		return ErrReplicationDestinationInUse
	}
	if _, err := tx.Exec(
		`UPDATE replication_sources SET name=?, type=?, url=?, config=?, storage_dest_id=?,
		schedule=?, enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		src.Name, src.Type, src.URL, cfg, destID,
		src.Schedule, src.Enabled, src.ID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE jobs SET storage_dest_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ? AND COALESCE(storage_dest_id, 0) != ?`,
		destID, src.ID, src.StorageDestID); err != nil {
		return fmt.Errorf("update replication source %d: rebind replicated jobs: %w", src.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update replication source %d: commit: %w", src.ID, err)
	}
	return nil
}

// UpdateReplicationSyncStatus stores the status of a sync attempt without
// per-item counters. Used when a sync starts ("running") or fails before it
// produces a summary; the counters are reset so a stale summary from an
// earlier successful sync is never shown against a running or failed state.
func (d *DB) UpdateReplicationSyncStatus(id int64, status, syncError string) error {
	_, err := d.Exec(
		`UPDATE replication_sources SET last_sync_at=CURRENT_TIMESTAMP,
		last_sync_status=?, last_sync_error=?,
		last_sync_jobs_synced=0, last_sync_jobs_failed=0,
		last_sync_restore_points=0, last_sync_bytes=0,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, syncError, id,
	)
	if err != nil {
		return fmt.Errorf("update replication sync status for source %d: %w", id, err)
	}
	return nil
}

// UpdateReplicationSyncResult stores the outcome of a completed sync along
// with its per-item counters. last_sync_success_at only advances on a fully
// successful sync, so the UI can show when replication was last known good
// even after a later partial or failed run.
func (d *DB) UpdateReplicationSyncResult(id int64, status, syncError string, jobsSynced, jobsFailed, restorePoints int, bytes int64) error {
	_, err := d.Exec(
		`UPDATE replication_sources SET last_sync_at=CURRENT_TIMESTAMP,
		last_sync_status=?, last_sync_error=?,
		last_sync_jobs_synced=?, last_sync_jobs_failed=?,
		last_sync_restore_points=?, last_sync_bytes=?,
		last_sync_success_at=CASE WHEN ?='success' THEN CURRENT_TIMESTAMP ELSE last_sync_success_at END,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, syncError, jobsSynced, jobsFailed, restorePoints, bytes, status, id,
	)
	if err != nil {
		return fmt.Errorf("update replication sync result for source %d: %w", id, err)
	}
	return nil
}

// DeleteReplicationSource removes a replication source by ID.
func (d *DB) DeleteReplicationSource(id int64) error {
	_, err := d.Exec("DELETE FROM replication_sources WHERE id = ?", id)
	return err
}

// ListReplicatedJobs returns all jobs that were replicated from a given source.
func (d *DB) ListReplicatedJobs(sourceID int64) ([]Job, error) {
	rows, err := d.Query(
		`SELECT id, name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode, pre_script,
		post_script, notify_on, verify_backup, COALESCE(storage_dest_id, 0), COALESCE(source_id, 0),
		retry_max_override, retry_delays_override,
		COALESCE(max_parallel_uploads, 1),
		created_at, updated_at
		FROM jobs WHERE source_id = ? ORDER BY name`, sourceID,
	)
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
			&job.VerifyBackup, &job.StorageDestID, &job.SourceID,
			&job.RetryMaxOverride, &job.RetryDelaysOverride,
			&job.MaxParallelUploads,
			&job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// CreateReplicatedJob creates a job record for a replicated backup, linking it
// to the replication source. It uses the target source's storage destination.
func (d *DB) CreateReplicatedJob(job Job) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO jobs (name, description, enabled, schedule, backup_type_chain,
		retention_count, retention_days, compression, encryption, container_mode, vm_mode,
		pre_script, post_script, notify_on, verify_backup, storage_dest_id, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Name, job.Description, job.Enabled, job.Schedule, job.BackupTypeChain,
		job.RetentionCount, job.RetentionDays, job.Compression, job.Encryption,
		job.ContainerMode, job.VMMode, job.PreScript, job.PostScript, job.NotifyOn,
		job.VerifyBackup, nullableID(job.StorageDestID), job.SourceID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AdoptReplicationDestination records destID as the storage destination of a
// replication source that has none, and of every job replicated from it that
// has none, so the files a sync wrote there can be found again by restore and
// preflight. A source or job that already names a destination is unchanged.
// It reports whether any row changed, so the caller knows to flush the
// configuration.
func (d *DB) AdoptReplicationDestination(sourceID, destID int64) (bool, error) {
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		return false, fmt.Errorf("adopt destination for replication source %d: begin tx: %w", sourceID, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a successful Commit is a no-op
	src, err := tx.Exec(`UPDATE replication_sources SET storage_dest_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND COALESCE(storage_dest_id, 0) = 0`, destID, sourceID)
	if err != nil {
		return false, fmt.Errorf("adopt destination for replication source %d: %w", sourceID, err)
	}
	jobs, err := tx.Exec(`UPDATE jobs SET storage_dest_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ? AND COALESCE(storage_dest_id, 0) = 0`, destID, sourceID)
	if err != nil {
		return false, fmt.Errorf("adopt destination for jobs of replication source %d: %w", sourceID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("adopt destination for replication source %d: commit: %w", sourceID, err)
	}
	// SQLite always reports affected rows, so these cannot fail here.
	srcRows, _ := src.RowsAffected()
	jobRows, _ := jobs.RowsAffected()
	return srcRows+jobRows > 0, nil
}

// DeleteReplicatedJobs removes all jobs and their associated data that were
// replicated from a given source.
func (d *DB) DeleteReplicatedJobs(sourceID int64) error {
	_, err := d.Exec("DELETE FROM jobs WHERE source_id = ?", sourceID)
	return err
}
