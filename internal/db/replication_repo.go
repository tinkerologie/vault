package db

import "database/sql"

// CreateReplicationSource inserts a new replication source and returns its ID.
func (d *DB) CreateReplicationSource(src ReplicationSource) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO replication_sources (name, url, storage_dest_id, schedule, enabled)
		VALUES (?, ?, ?, ?, ?)`,
		src.Name, src.URL, src.StorageDestID, src.Schedule, src.Enabled,
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
		`SELECT id, name, url, storage_dest_id, schedule, enabled,
			last_sync_at, last_sync_status, last_sync_error, created_at, updated_at
		FROM replication_sources WHERE id = ?`, id,
	).Scan(&src.ID, &src.Name, &src.URL, &src.StorageDestID,
		&src.Schedule, &src.Enabled, &src.LastSyncAt, &src.LastSyncStatus,
		&src.LastSyncError, &src.CreatedAt, &src.UpdatedAt)
	if err == sql.ErrNoRows {
		return src, ErrNotFound
	}
	return src, err
}

// ListReplicationSources returns all replication sources ordered by name.
func (d *DB) ListReplicationSources() ([]ReplicationSource, error) {
	rows, err := d.Query(
		`SELECT id, name, url, storage_dest_id, schedule, enabled,
			last_sync_at, last_sync_status, last_sync_error, created_at, updated_at
		FROM replication_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []ReplicationSource
	for rows.Next() {
		var src ReplicationSource
		if err := rows.Scan(&src.ID, &src.Name, &src.URL, &src.StorageDestID,
			&src.Schedule, &src.Enabled, &src.LastSyncAt, &src.LastSyncStatus,
			&src.LastSyncError, &src.CreatedAt, &src.UpdatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// UpdateReplicationSource updates an existing replication source.
func (d *DB) UpdateReplicationSource(src ReplicationSource) error {
	_, err := d.Exec(
		`UPDATE replication_sources SET name=?, url=?, storage_dest_id=?,
		schedule=?, enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		src.Name, src.URL, src.StorageDestID,
		src.Schedule, src.Enabled, src.ID,
	)
	return err
}

// UpdateReplicationSyncStatus stores the result of a sync attempt.
func (d *DB) UpdateReplicationSyncStatus(id int64, status, syncError string) error {
	_, err := d.Exec(
		`UPDATE replication_sources SET last_sync_at=CURRENT_TIMESTAMP,
		last_sync_status=?, last_sync_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, syncError, id,
	)
	return err
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
		post_script, notify_on, verify_backup, storage_dest_id, COALESCE(source_id, 0), created_at, updated_at
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
			&job.VerifyBackup, &job.StorageDestID, &job.SourceID, &job.CreatedAt, &job.UpdatedAt); err != nil {
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
		job.VerifyBackup, job.StorageDestID, job.SourceID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteReplicatedJobs removes all jobs and their associated data that were
// replicated from a given source.
func (d *DB) DeleteReplicatedJobs(sourceID int64) error {
	_, err := d.Exec("DELETE FROM jobs WHERE source_id = ?", sourceID)
	return err
}
