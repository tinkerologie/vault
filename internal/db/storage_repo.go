package db

import "database/sql"

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
		created_at, updated_at
		FROM storage_destinations WHERE id = ?`, id,
	).Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.DedupEnabled,
		&dest.LastHealthCheckAt, &dest.LastHealthCheckStatus, &dest.LastHealthCheckError,
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
		"UPDATE storage_destinations SET name=?, type=?, config=?, dedup_enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?",
		dest.Name, dest.Type, dest.Config, dest.DedupEnabled, dest.ID,
	)
	return err
}

func (d *DB) DeleteStorageDestination(id int64) error {
	_, err := d.Exec("DELETE FROM storage_destinations WHERE id = ?", id)
	return err
}

// CountJobsByStorageDestID returns the number of jobs that reference the
// given storage destination.
func (d *DB) CountJobsByStorageDestID(storageDestID int64) (int, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM jobs WHERE storage_dest_id = ?", storageDestID).Scan(&count)
	return count, err
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
