package runner

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestScanStorageOrphansHappyPath drives ScanStorageOrphans against a real
// local destination with a mix of known and unknown files. Known files
// (those under a restore point's storage_path for a job targeting this
// destination) are filtered out; the rest are reported as orphans.
func TestScanStorageOrphansHappyPath(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Create a job + restore point pointing at "my-job/1_run".
	jobID, _ := database.CreateJob(db.Job{
		Name: "my-job", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "success", BackupType: "full",
	})
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "my-job/1_run", Metadata: "{}", SizeBytes: 100,
	}); err != nil {
		t.Fatal(err)
	}

	// Lay out files: some under the known prefix, some elsewhere.
	mustMkdir := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(p string, sz int) {
		if err := os.WriteFile(p, make([]byte, sz), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustMkdir(filepath.Join(storageDir, "my-job", "1_run"))
	mustMkdir(filepath.Join(storageDir, "_vault"))
	mustMkdir(filepath.Join(storageDir, "leftover"))

	mustWrite(filepath.Join(storageDir, "my-job", "1_run", "data.tar"), 50)  // known
	mustWrite(filepath.Join(storageDir, "my-job", "1_run", "manifest.json"), 30) // known
	mustWrite(filepath.Join(storageDir, "_vault", "vault.db"), 10)           // internal — excluded
	mustWrite(filepath.Join(storageDir, "leftover", "old.tar"), 25)          // ORPHAN
	mustWrite(filepath.Join(storageDir, "stray.bin"), 7)                     // ORPHAN

	orphans, total, err := r.ScanStorageOrphans(dest)
	if err != nil {
		t.Fatalf("ScanStorageOrphans: %v", err)
	}
	sort.Strings(orphans)
	want := []string{"leftover/old.tar", "stray.bin"}
	if len(orphans) != len(want) {
		t.Fatalf("got %d orphans (%v), want %d (%v)", len(orphans), orphans, len(want), want)
	}
	for i, w := range want {
		if orphans[i] != w {
			t.Errorf("orphan[%d] = %q, want %q", i, orphans[i], w)
		}
	}
	if total != int64(25+7) {
		t.Errorf("total = %d, want 32", total)
	}
}

// TestScanStorageOrphansDedupRefused exercises the early-error branch:
// dedup destinations must redirect callers to chunk-store GC instead.
func TestScanStorageOrphansDedupRefused(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "dedup", Type: "local", Config: `{"path":"` + storageDir + `"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := database.GetStorageDestination(id)

	_, _, err = r.ScanStorageOrphans(dest)
	if err == nil {
		t.Error("expected dedup-refused error")
	}
}

// TestScanStorageOrphansAdapterConstructionError exercises the adapter
// construction failure branch.
func TestScanStorageOrphansAdapterConstructionError(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	id, _ := database.CreateStorageDestination(db.StorageDestination{
		Name: "bad", Type: "not-a-real-type", Config: `{}`,
	})
	dest, _ := database.GetStorageDestination(id)
	if _, _, err := r.ScanStorageOrphans(dest); err == nil {
		t.Error("expected adapter construction error")
	}
}

// TestDeleteStorageOrphansHappyPath drives the delete flow. Files seen by
// the rescan should be removable.
func TestDeleteStorageOrphansHappyPath(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Lay out an orphan with no jobs referencing it.
	if err := os.WriteFile(filepath.Join(storageDir, "leftover.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = dest

	deleted, errs := r.DeleteStorageOrphans(dest, []string{"leftover.bin"})
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (errs=%v)", deleted, errs)
	}
	if len(errs) != 0 {
		t.Errorf("got %d errors, want 0: %v", len(errs), errs)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "leftover.bin")); !os.IsNotExist(err) {
		t.Errorf("leftover.bin should have been deleted (stat err=%v)", err)
	}
}

// TestDeleteStorageOrphansSkipsNonOrphans verifies the rescan safety net:
// paths that no longer appear in the orphan set are skipped with an error
// rather than blindly deleted.
func TestDeleteStorageOrphansSkipsNonOrphans(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Create a job + restore point that "owns" a file — so the file is
	// NOT an orphan even though we'll try to delete it.
	jobID, _ := database.CreateJob(db.Job{
		Name: "owner", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "owner/1_run", Metadata: "{}", SizeBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storageDir, "owner", "1_run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "owner", "1_run", "data.tar"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}

	deleted, errs := r.DeleteStorageOrphans(dest, []string{"owner/1_run/data.tar"})
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (must refuse non-orphan)", deleted)
	}
	if len(errs) != 1 {
		t.Errorf("got %d errors, want 1 (refusal)", len(errs))
	}
	// File should still exist.
	if _, err := os.Stat(filepath.Join(storageDir, "owner", "1_run", "data.tar")); err != nil {
		t.Errorf("file should not have been deleted: %v", err)
	}
}

// TestDeleteStorageOrphansDedupRefused exercises the dedup-refused branch.
func TestDeleteStorageOrphansDedupRefused(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "dedup", Type: "local", Config: `{"path":"` + storageDir + `"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := database.GetStorageDestination(id)

	deleted, errs := r.DeleteStorageOrphans(dest, []string{"anything"})
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 on dedup", deleted)
	}
	if len(errs) != 1 {
		t.Errorf("got %d errors, want 1", len(errs))
	}
}
