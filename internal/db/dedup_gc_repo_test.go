package db

import (
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

	// WHERE storage_id=? must filter: a destination with no runs of its
	// own returns found=false even though other destinations have runs.
	if _, found, err := d.LatestDedupGCRun(destID + 1); err != nil || found {
		t.Fatalf("expected (found=false, nil) for unknown storage id, got found=%v err=%v", found, err)
	}
}
