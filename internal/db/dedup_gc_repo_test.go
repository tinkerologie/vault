package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestDedupGCRunInsertAndLatest(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// No runs yet → found=false, no error.
	if _, found, err := d.LatestDedupGCRun(destID); err != nil || found {
		t.Fatalf("expected (found=false, nil), got found=%v err=%v", found, err)
	}

	older := DedupGCRun{
		StorageID: destID, StartedAt: time.Now().Add(-2 * time.Hour).UTC(),
		CompletedAt: time.Now().Add(-2 * time.Hour).UTC(),
		FreedPacks:  1, FreedBytes: 100, RewritableBytes: 10, ErrorCount: 0,
	}
	newer := DedupGCRun{
		StorageID: destID, StartedAt: time.Now().Add(-1 * time.Minute).UTC(),
		CompletedAt: time.Now().UTC(),
		Reachable:   42, FreedPacks: 2, FreedBytes: 200, RewritableBytes: 8406395, ErrorCount: 1,
		CompactedPacks: 3, ReclaimedBytes: 12345,
	}
	if _, err := d.InsertDedupGCRun(older); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertDedupGCRun(newer); err != nil {
		t.Fatal(err)
	}

	got, found, err := d.LatestDedupGCRun(destID)
	if err != nil || !found {
		t.Fatalf("expected latest run, got found=%v err=%v", found, err)
	}
	if got.RewritableBytes != 8406395 || got.FreedBytes != 200 || got.ErrorCount != 1 || got.Reachable != 42 {
		t.Fatalf("latest run mismatch: %+v", got)
	}
	if got.CompactedPacks != 3 || got.ReclaimedBytes != 12345 {
		t.Fatalf("compaction fields not persisted: CompactedPacks=%d ReclaimedBytes=%d", got.CompactedPacks, got.ReclaimedBytes)
	}

	// WHERE storage_id=? must filter: a destination with no runs of its
	// own returns found=false even though other destinations have runs.
	if _, found, err := d.LatestDedupGCRun(destID + 1); err != nil || found {
		t.Fatalf("expected (found=false, nil) for unknown storage id, got found=%v err=%v", found, err)
	}
}

func TestDropDedupStateClearsGCRuns(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertDedupGCRun(DedupGCRun{StorageID: destID, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), FreedBytes: 1, RewritableBytes: 2}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.LatestDedupGCRun(destID); !found {
		t.Fatal("expected a gc run before drop")
	}
	if err := d.DropDedupState(destID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.LatestDedupGCRun(destID); found {
		t.Fatal("dedup_gc_runs not cleared by DropDedupState")
	}
}

// TestAlterMigrationsAddNewGCColumns proves that an old-schema database
// (created BEFORE the compacted_packs/reclaimed_bytes columns existed)
// acquires the two new columns automatically when re-opened via db.Open.
// This is the upgrade path real users hit when their on-disk DB pre-dates
// this change.
func TestAlterMigrationsAddNewGCColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")

	// 1) Create an old-shape DB with everything EXCEPT the two new columns.
	//    Use a raw sql.Open against the same driver so we don't trigger
	//    the migration path yet.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE storage_destinations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config TEXT NOT NULL,
		dedup_enabled INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE dedup_gc_runs (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		storage_id       INTEGER NOT NULL,
		started_at       DATETIME NOT NULL,
		completed_at     DATETIME NOT NULL,
		reachable        INTEGER NOT NULL DEFAULT 0,
		freed_packs      INTEGER NOT NULL DEFAULT 0,
		freed_bytes      INTEGER NOT NULL DEFAULT 0,
		rewritable_bytes INTEGER NOT NULL DEFAULT 0,
		error_count      INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// 2) Reopen via db.Open — schema string is a no-op (tables exist) but
	//    alterMigrations should add the new columns.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 3) Verify the two new columns exist by inserting a row that requires them.
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertDedupGCRun(DedupGCRun{
		StorageID:      destID,
		StartedAt:      time.Now().UTC(),
		CompletedAt:    time.Now().UTC(),
		CompactedPacks: 5,
		ReclaimedBytes: 9999,
	}); err != nil {
		t.Fatalf("InsertDedupGCRun on migrated DB failed: %v", err)
	}
	got, found, err := d.LatestDedupGCRun(destID)
	if err != nil || !found {
		t.Fatalf("LatestDedupGCRun on migrated DB: found=%v err=%v", found, err)
	}
	if got.CompactedPacks != 5 || got.ReclaimedBytes != 9999 {
		t.Fatalf("new columns not persisted on migrated DB: %+v", got)
	}
}
