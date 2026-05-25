package db

import (
	"database/sql"
	"fmt"
	"time"
)

// DedupGCRun is one persisted mark-and-sweep GC result for a destination.
// Rows are written by dedup.RunGC; the most recent row backs the Storage
// card's "reclaimable / last cleanup" fields (which were previously
// process-local in-memory counters that always read 0 from the API).
type DedupGCRun struct {
	ID              int64
	StorageID       int64
	StartedAt       time.Time
	CompletedAt     time.Time
	Reachable       int64
	FreedPacks      int64
	FreedBytes      int64
	RewritableBytes int64
	ErrorCount      int64
	CompactedPacks  int64
	ReclaimedBytes  int64
}

// InsertDedupGCRun records a completed GC run and returns the new row ID.
func (d *DB) InsertDedupGCRun(run DedupGCRun) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO dedup_gc_runs
		    (storage_id, started_at, completed_at, reachable, freed_packs, freed_bytes,
		     rewritable_bytes, error_count, compacted_packs, reclaimed_bytes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.StorageID, run.StartedAt, run.CompletedAt, run.Reachable,
		run.FreedPacks, run.FreedBytes, run.RewritableBytes, run.ErrorCount,
		run.CompactedPacks, run.ReclaimedBytes,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting dedup gc run: %w", err)
	}
	return res.LastInsertId()
}

// LatestDedupGCRun returns the most recent GC run for a destination. found
// is false (with a nil error) when the destination has never been GC'd.
func (d *DB) LatestDedupGCRun(storageID int64) (DedupGCRun, bool, error) {
	var r DedupGCRun
	err := d.QueryRow(
		`SELECT id, storage_id, started_at, completed_at, reachable,
		        freed_packs, freed_bytes, rewritable_bytes, error_count,
		        compacted_packs, reclaimed_bytes
		   FROM dedup_gc_runs
		  WHERE storage_id = ?
		  ORDER BY completed_at DESC, id DESC
		  LIMIT 1`, storageID,
	).Scan(&r.ID, &r.StorageID, &r.StartedAt, &r.CompletedAt, &r.Reachable,
		&r.FreedPacks, &r.FreedBytes, &r.RewritableBytes, &r.ErrorCount,
		&r.CompactedPacks, &r.ReclaimedBytes)
	if err == sql.ErrNoRows {
		return DedupGCRun{}, false, nil
	}
	if err != nil {
		return DedupGCRun{}, false, fmt.Errorf("latest dedup gc run: %w", err)
	}
	return r, true, nil
}
