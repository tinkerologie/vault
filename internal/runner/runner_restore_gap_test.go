package runner

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRestoreSinglePointDedupRouteToChunkedMissingJob exercises the early
// branch of restoreSinglePoint that detects a dedup restore point and
// delegates to restoreSinglePointChunked, where the first DB lookup
// (GetJob by rp.JobID) fails because no such job exists.
func TestRestoreSinglePointDedupRouteToChunkedMissingJob(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)

	// Construct an RP that resolves as dedup (via metadata) but references
	// a job ID that doesn't exist in the DB so the chunked path immediately
	// errors on r.db.GetJob.
	const hexID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	rp := db.RestorePoint{
		ID:    1,
		JobID: 9999, // unknown
		Metadata: `{"item_manifests":{"plex":"` + hexID + `"}}`,
	}

	err := r.restoreSinglePoint(rp, "plex", "container", "", "", nil, restoreProgressReporter{})
	if err == nil {
		t.Fatal("restoreSinglePoint to chunked-path with missing job should error")
	}
	if !strings.Contains(err.Error(), "getting job") {
		t.Errorf("error %q should mention 'getting job'", err.Error())
	}
}

// TestRestoreSinglePointDedupRouteToChunkedMissingDest covers the next
// failure point: job exists but its storage destination has been deleted.
func TestRestoreSinglePointDedupRouteToChunkedMissingDest(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)

	// Create a destination, create the job with FK to it, then delete the
	// destination so the runtime lookup fails. (Schema declares the FK as
	// nullable / set-null, but ListJobs reports the orphaned StorageDestID
	// as-is — perfect for exercising the missing-dest branch.)
	dest := createLocalDest(t, database, storageDir)
	jobID, err := database.CreateJob(db.Job{
		Name: "restore-job", BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// Swap the job's storage_dest_id to a nonexistent value via raw SQL —
	// the FK is RESTRICT on delete, but UPDATE checks point-in-time only
	// when foreign_keys=ON. SQLite allows transiently invalid refs across
	// updates so this works against the schema declared in migrations.go.
	if _, err := database.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	if _, err := database.Exec(`UPDATE jobs SET storage_dest_id = 99999 WHERE id = ?`, jobID); err != nil {
		t.Fatalf("orphan job: %v", err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	_ = dest // silence linter; dest only used to satisfy FK during CreateJob

	const hexID = "1111111111111111111111111111111111111111111111111111111111111111"
	rp := db.RestorePoint{ID: 1, JobID: jobID, Metadata: `{"item_manifests":{"plex":"` + hexID + `"}}`}

	err = r.restoreSinglePoint(rp, "plex", "container", "", "", nil, restoreProgressReporter{})
	if err == nil {
		t.Fatal("restoreSinglePoint with missing storage destination should error")
	}
	if !strings.Contains(err.Error(), "storage destination") {
		t.Errorf("error %q should mention 'storage destination'", err.Error())
	}
}

// TestRestoreSinglePointClassicEmpty exercises the non-dedup branch of
// restoreSinglePoint. With no files in storage and a bogus itemType, the
// stage step completes quickly (no files to download) and restoreStagedItem
// then fails on the unknown item-type switch — which is exactly the path
// we need to cover the non-dedup branch's tempdir + stage + handler-dispatch
// sequence.
func TestRestoreSinglePointClassicEmpty(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "classic-job", BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
	})
	rpID, _ := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "classic-job/empty-run", Metadata: "{}",
	})
	rp, _ := database.GetRestorePoint(rpID)

	// Create the empty item directory so adapter.List() succeeds and
	// stageRestorePointItem short-circuits cleanly (no files to download).
	itemDir := filepath.Join(storageDir, "classic-job", "empty-run", "no-such-item")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err := r.restoreSinglePoint(rp, "no-such-item", "totally-unknown-type",
		"", "", nil, restoreProgressReporter{})
	if err == nil {
		t.Fatal("restoreSinglePoint with unknown item type should error from restoreStagedItem")
	}
	if !strings.Contains(err.Error(), "unknown item type") {
		t.Errorf("error %q should mention 'unknown item type'", err.Error())
	}
}

// TestRestoreSinglePointDedupChunkedBadHandlerType: a dedup RP whose
// itemType is unknown to newHandler() should error inside the chunked path.
func TestRestoreSinglePointDedupChunkedBadHandlerType(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	r.serverKey = testServerKey()
	dest := makeDedupDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "restore-bad-type-job", BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	const hexID = "2222222222222222222222222222222222222222222222222222222222222222"
	raw, _ := hex.DecodeString(hexID)
	rp := db.RestorePoint{ID: 1, JobID: jobID, ManifestID: raw}

	err := r.restoreSinglePoint(rp, "x", "bogus-type", "", "", nil, restoreProgressReporter{})
	if err == nil {
		t.Fatal("restoreSinglePoint with unknown item type should error")
	}
	if !strings.Contains(err.Error(), "unknown item type") {
		t.Errorf("error %q should mention 'unknown item type'", err.Error())
	}
}
