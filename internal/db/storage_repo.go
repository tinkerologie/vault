package db

import (
	"database/sql"
	"fmt"
)

func (d *DB) CreateStorageDestination(dest StorageDestination) (int64, error) {
	res, err := d.Exec(
		"INSERT INTO storage_destinations (name, type, config, dedup_enabled) VALUES (?, ?, ?, ?)",
		dest.Name, dest.Type, dest.Config, dest.DedupEnabled,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetStorageDestination(id int64) (StorageDestination, error) {
	var dest StorageDestination
	err := d.QueryRow(
		`SELECT id, name, type, config, COALESCE(dedup_enabled, 0),
		last_health_check_at, COALESCE(last_health_check_status, ''), COALESCE(last_health_check_error, ''),
		COALESCE(consecutive_failures, 0),
		COALESCE(breaker_state, 'closed'),
		breaker_opened_at,
		COALESCE(backup_database_enabled, 0),
		created_at, updated_at
		FROM storage_destinations WHERE id = ?`, id,
	).Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.DedupEnabled,
		&dest.LastHealthCheckAt, &dest.LastHealthCheckStatus, &dest.LastHealthCheckError,
		&dest.ConsecutiveFailures, &dest.BreakerState, &dest.BreakerOpenedAt,
		&dest.BackupDatabaseEnabled,
		&dest.CreatedAt, &dest.UpdatedAt)
	if err == sql.ErrNoRows {
		return dest, ErrNotFound
	}
	return dest, err
}

func (d *DB) ListStorageDestinations() ([]StorageDestination, error) {
	rows, err := d.Query(
		`SELECT id, name, type, config, COALESCE(dedup_enabled, 0),
		last_health_check_at, COALESCE(last_health_check_status, ''), COALESCE(last_health_check_error, ''),
		COALESCE(consecutive_failures, 0),
		COALESCE(breaker_state, 'closed'),
		breaker_opened_at,
		COALESCE(backup_database_enabled, 0),
		created_at, updated_at
		FROM storage_destinations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dests []StorageDestination
	for rows.Next() {
		var dest StorageDestination
		if err := rows.Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.DedupEnabled,
			&dest.LastHealthCheckAt, &dest.LastHealthCheckStatus, &dest.LastHealthCheckError,
			&dest.ConsecutiveFailures, &dest.BreakerState, &dest.BreakerOpenedAt,
			&dest.BackupDatabaseEnabled,
			&dest.CreatedAt, &dest.UpdatedAt); err != nil {
			return nil, err
		}
		dests = append(dests, dest)
	}
	return dests, rows.Err()
}

// UpdateStorageDestinationHealth records the outcome of a TestConnection
// against a storage destination. status is "ok" or "failed"; errMsg holds
// the error string when status == "failed". The timestamp is set to the
// current UTC time.
func (d *DB) UpdateStorageDestinationHealth(id int64, status, errMsg string) error {
	_, err := d.Exec(
		`UPDATE storage_destinations SET
		last_health_check_at = CURRENT_TIMESTAMP,
		last_health_check_status = ?,
		last_health_check_error = ?
		WHERE id = ?`,
		status, errMsg, id,
	)
	return err
}

func (d *DB) UpdateStorageDestination(dest StorageDestination) error {
	_, err := d.Exec(
		`UPDATE storage_destinations
		 SET name=?, type=?, config=?, dedup_enabled=?, backup_database_enabled=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		dest.Name, dest.Type, dest.Config, dest.DedupEnabled, dest.BackupDatabaseEnabled, dest.ID,
	)
	return err
}

// RecordDestinationFailure increments consecutive_failures and clears
// success-related fields. Used by the circuit breaker on each failed
// run, health check, or preflight.
func (d *DB) RecordDestinationFailure(destID int64, newCount int) error {
	_, err := d.Exec(
		`UPDATE storage_destinations SET consecutive_failures=? WHERE id=?`,
		newCount, destID,
	)
	return err
}

// RecordDestinationSuccess resets consecutive_failures to 0. Does NOT
// touch breaker_state — that's CloseBreaker's job.
func (d *DB) RecordDestinationSuccess(destID int64) error {
	_, err := d.Exec(
		`UPDATE storage_destinations SET consecutive_failures=0 WHERE id=?`,
		destID,
	)
	return err
}

// OpenBreaker transitions the breaker to "open" with the given final
// failure count. Stamps breaker_opened_at.
func (d *DB) OpenBreaker(destID int64, finalFailureCount int) error {
	_, err := d.Exec(
		`UPDATE storage_destinations
		 SET breaker_state='open',
		     consecutive_failures=?,
		     breaker_opened_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		finalFailureCount, destID,
	)
	return err
}

// CloseBreaker transitions the breaker to "closed" and resets counters.
func (d *DB) CloseBreaker(destID int64) error {
	_, err := d.Exec(
		`UPDATE storage_destinations
		 SET breaker_state='closed',
		     consecutive_failures=0,
		     breaker_opened_at=NULL
		 WHERE id=?`,
		destID,
	)
	return err
}

func (d *DB) DeleteStorageDestination(id int64) error {
	_, err := d.Exec("DELETE FROM storage_destinations WHERE id = ?", id)
	return err
}

// ListDBBackupDestinations returns all destinations with
// backup_database_enabled=1. Used by the runner to fan out the DB
// backup after each successful job.
func (d *DB) ListDBBackupDestinations() ([]StorageDestination, error) {
	all, err := d.ListStorageDestinations()
	if err != nil {
		return nil, err
	}
	out := make([]StorageDestination, 0)
	for _, dest := range all {
		if dest.BackupDatabaseEnabled {
			out = append(out, dest)
		}
	}
	return out, nil
}

// CountJobsByStorageDestID returns the number of jobs that reference the
// given storage destination.
func (d *DB) CountJobsByStorageDestID(storageDestID int64) (int, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM jobs WHERE storage_dest_id = ?", storageDestID).Scan(&count)
	return count, err
}

// ListJobsByStorageDestID returns id/name pairs of every job that references
// the given storage destination. Used by the dependent-jobs API surface so the
// UI can render which jobs would be orphaned by a delete.
func (d *DB) ListJobsByStorageDestID(storageDestID int64) ([]JobRef, error) {
	rows, err := d.Query("SELECT id, name FROM jobs WHERE storage_dest_id = ? ORDER BY name", storageDestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	jobs := make([]JobRef, 0)
	for rows.Next() {
		var j JobRef
		if err := rows.Scan(&j.ID, &j.Name); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// JobRef is a slim id/name view of a job for endpoints that don't need the
// full Job model.
type JobRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DedupPack is one row in the dedup_packs table: a single pack blob written
// to a storage destination plus its metadata.
type DedupPack struct {
	ID         string
	StorageID  int64
	Path       string
	SizeBytes  int64
	ChunkCount int
}

// DedupChunk is one row in the dedup_chunks table: a chunk's location within
// a pack. (storage_id, chunk_id) is the primary key.
type DedupChunk struct {
	ChunkID   []byte
	StorageID int64
	PackID    string
	Offset    int64
	Length    int64
}

// UpsertDedupPack inserts a pack row; idempotent — duplicate IDs are ignored.
func (d *DB) UpsertDedupPack(p DedupPack) error {
	_, err := d.Exec(`
        INSERT OR IGNORE INTO dedup_packs (id, storage_id, path, size_bytes, chunk_count, created_at)
        VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		p.ID, p.StorageID, p.Path, p.SizeBytes, p.ChunkCount)
	return err
}

// UpsertDedupChunk inserts a chunk-to-pack mapping; idempotent.
func (d *DB) UpsertDedupChunk(c DedupChunk) error {
	_, err := d.Exec(`
        INSERT OR IGNORE INTO dedup_chunks (chunk_id, storage_id, pack_id, offset, length)
        VALUES (?, ?, ?, ?, ?)`,
		c.ChunkID, c.StorageID, c.PackID, c.Offset, c.Length)
	return err
}

// HasDedupChunk returns true if the destination already stores this chunk.
func (d *DB) HasDedupChunk(storageID int64, chunkID []byte) (bool, error) {
	var one int
	err := d.QueryRow(`SELECT 1 FROM dedup_chunks WHERE storage_id=? AND chunk_id=?`,
		storageID, chunkID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// LocateDedupChunk returns the pack path + chunk offset + length to range-read.
func (d *DB) LocateDedupChunk(storageID int64, chunkID []byte) (packPath string, offset, length int64, err error) {
	err = d.QueryRow(`
        SELECT p.path, c.offset, c.length
          FROM dedup_chunks c JOIN dedup_packs p ON c.pack_id = p.id
         WHERE c.storage_id=? AND c.chunk_id=?`,
		storageID, chunkID).Scan(&packPath, &offset, &length)
	return
}

// DedupAggregates is the source-of-truth stats snapshot for one destination,
// computed from SQL aggregates rather than from in-memory counters. Used by
// the Storage page's dedup stats card (polled every 30s) so a fresh daemon
// process or a different goroutine still sees the correct totals.
type DedupAggregates struct {
	TotalChunks   int64 // COUNT(*) dedup_chunks
	TotalPacks    int64 // COUNT(*) dedup_packs
	PhysicalBytes int64 // SUM(size_bytes) dedup_packs
	LogicalBytes  int64 // SUM(size_bytes) of live dedup restore_points for jobs on this destination
}

// DedupAggregates returns SQL-derived totals for the destination. Caller must
// already have verified the destination is dedup-enabled.
func (d *DB) DedupAggregates(storageID int64) (DedupAggregates, error) {
	var agg DedupAggregates
	var phys sql.NullInt64
	if err := d.QueryRow(`
        SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
          FROM dedup_packs WHERE storage_id=?`, storageID).Scan(&agg.TotalPacks, &phys); err != nil {
		return agg, fmt.Errorf("aggregate packs: %w", err)
	}
	agg.PhysicalBytes = phys.Int64
	if err := d.QueryRow(`
        SELECT COUNT(*) FROM dedup_chunks WHERE storage_id=?`, storageID).Scan(&agg.TotalChunks); err != nil {
		return agg, fmt.Errorf("aggregate chunks: %w", err)
	}
	// Logical = sum of per-restore-point byte counts for dedup runs. The
	// runner persists this on restore_points.size_bytes at backup time, so
	// it reflects the user's "would-have-cost-without-dedup" total across
	// every snapshot still present on this destination.
	//
	// We must NOT filter on `manifest_id IS NOT NULL`: that column is only set
	// for single-item dedup jobs; multi-item jobs (e.g. a containers backup)
	// leave it NULL and record per-item IDs in metadata.item_manifests, so the
	// old filter zeroed out logical bytes — and the dedup ratio — for every
	// multi-item dedup destination. The caller has already verified this
	// destination is dedup-enabled, and dedup mode is immutable per
	// destination, so every restore point for its jobs is a dedup restore
	// point. Restore points are hard-deleted, so no soft-delete filter needed.
	var logical sql.NullInt64
	if err := d.QueryRow(`
        SELECT COALESCE(SUM(size_bytes), 0)
          FROM restore_points
         WHERE job_id IN (SELECT id FROM jobs WHERE storage_dest_id=?)`, storageID).Scan(&logical); err != nil {
		return agg, fmt.Errorf("aggregate logical bytes: %w", err)
	}
	agg.LogicalBytes = logical.Int64
	return agg, nil
}

// DropDedupState wipes all SQLite dedup rows for one destination.
// Used by RebuildFromStorage (Task 5) and `vault dedup repair` (Task 14).
// Does NOT touch the on-storage pack/index blobs.
func (d *DB) DropDedupState(storageID int64) error {
	if _, err := d.Exec(`DELETE FROM dedup_chunks WHERE storage_id=?`, storageID); err != nil {
		return err
	}
	if _, err := d.Exec(`DELETE FROM dedup_packs  WHERE storage_id=?`, storageID); err != nil {
		return err
	}
	if _, err := d.Exec(`DELETE FROM dedup_gc_runs WHERE storage_id=?`, storageID); err != nil {
		return err
	}
	return nil
}

// ListDedupPacks returns every pack registered for a destination.
func (d *DB) ListDedupPacks(storageID int64) ([]DedupPack, error) {
	rows, err := d.Query(`SELECT id, storage_id, path, size_bytes, chunk_count FROM dedup_packs WHERE storage_id=?`, storageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DedupPack{}
	for rows.Next() {
		var p DedupPack
		if err := rows.Scan(&p.ID, &p.StorageID, &p.Path, &p.SizeBytes, &p.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteDedupPack removes a pack row and (via FK ON DELETE CASCADE) all its
// chunk rows. Caller is responsible for deleting the on-storage blob first.
func (d *DB) DeleteDedupPack(storageID int64, packID string) error {
	_, err := d.Exec(`DELETE FROM dedup_packs WHERE storage_id=? AND id=?`, storageID, packID)
	return err
}

// ListDedupChunksByPack returns every chunk row inside one pack.
func (d *DB) ListDedupChunksByPack(storageID int64, packID string) ([]DedupChunk, error) {
	rows, err := d.Query(`SELECT chunk_id, storage_id, pack_id, offset, length FROM dedup_chunks WHERE storage_id=? AND pack_id=?`, storageID, packID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DedupChunk{}
	for rows.Next() {
		var c DedupChunk
		if err := rows.Scan(&c.ChunkID, &c.StorageID, &c.PackID, &c.Offset, &c.Length); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
