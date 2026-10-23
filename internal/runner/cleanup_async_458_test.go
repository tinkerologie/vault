package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/tinkerologie/vault/internal/db"
)

// b458RunAsync runs fn inside a synctest bubble and waits until every
// goroutine it started has finished, so the async cleanup goroutines are
// observed deterministically without sleeping.
func b458RunAsync(t *testing.T, fn func()) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		fn()
		synctest.Wait()
	})
}

// b458ActivityContains reports whether any activity row contains sub.
func b458ActivityContains(t *testing.T, d *db.DB, sub string) bool {
	t.Helper()
	entries, err := d.ListActivityLogs(200, "", 0)
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

func b458QueueLen(r *Runner) int {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	return len(r.queue)
}

func b458WriteFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupJobStorageAsyncSuccessB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	b458WriteFile(t, filepath.Join(storageDir, "jobA", "run1", "data.tar"), "x")
	b458WriteFile(t, filepath.Join(storageDir, "jobA", "run2", "manifest.json"), `{"job_name":"jobA","run_id":1,"backup_type":"full","items_done":1}`)
	b458WriteFile(t, filepath.Join(storageDir, "jobA", "stray", "orphan.part"), "y")

	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(1, "jobA", dest, []string{"jobA/run1", ""})
	})

	// The recorded path and the unrecorded run folder with a manifest are
	// Vault's and go. The folder without a manifest is not provably Vault's
	// and survives (#514), so the job directory stays too.
	for _, gone := range []string{"run1", "run2"} {
		if _, err := os.Stat(filepath.Join(storageDir, "jobA", gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed, stat err=%v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(storageDir, "jobA", "stray", "orphan.part")); err != nil {
		t.Fatalf("unmanaged content must survive the job sweep: %v", err)
	}
	if n := b458QueueLen(r); n != 0 {
		t.Fatalf("queue should be empty after cleanup, has %d", n)
	}
}

func TestCleanupJobStorageAsyncAdapterErrorB458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	dest := db.StorageDestination{ID: 9, Type: "bogus-type", Config: "{}"}
	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(1, "jobB", dest, []string{"jobB/run1"})
	})
	if n := b458QueueLen(r); n != 0 {
		t.Fatalf("queue should be empty, has %d", n)
	}
}

func TestCleanupJobStorageAsyncDeleteFailureB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	b458RunAsync(t, func() {
		// A path escaping the destination root is rejected by the local
		// adapter, so the per-restore-point delete fails.
		r.CleanupJobStorageAsync(1, "jobC", dest, []string{"../escape"})
	})
	if !b458ActivityContains(t, d, `Failed to delete backup files for deleted job "jobC"`) {
		t.Fatal("expected cleanup failure activity entry")
	}
}

func TestCleanupJobStorageAsyncDedupLastJobB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	r.serverKey = testServerKey()
	storageDir := t.TempDir()
	dest, jobID := dedupBackup(t, r, d, storageDir)
	rps, _ := d.ListRestorePoints(jobID)
	var paths []string
	for _, rp := range rps {
		paths = append(paths, rp.StoragePath)
	}
	if err := d.DeleteJob(jobID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(jobID, "escrow", dest, paths)
	})
	if _, err := os.Stat(filepath.Join(storageDir, "_vault", "repo.json")); !os.IsNotExist(err) {
		t.Fatalf("repo.json should be removed for the last dedup job, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "_vault", "packs")); !os.IsNotExist(err) {
		t.Fatalf("packs dir should be removed, err=%v", err)
	}
}

func TestCleanupJobStorageAsyncDedupSharedRepoRunsGCB458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	r.serverKey = testServerKey()
	storageDir := t.TempDir()
	dest, jobID := dedupBackup(t, r, d, storageDir)
	// A second job still on the destination keeps the repo alive, so the
	// cleanup must run GC instead of removing the repository.
	if _, err := d.CreateJob(db.Job{Name: "survivor", StorageDestID: dest.ID, BackupTypeChain: "full"}); err != nil {
		t.Fatal(err)
	}
	rps, _ := d.ListRestorePoints(jobID)
	var paths []string
	for _, rp := range rps {
		paths = append(paths, rp.StoragePath)
	}
	if err := d.DeleteJob(jobID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(jobID, "escrow", dest, paths)
	})
	if _, err := os.Stat(filepath.Join(storageDir, "_vault", "repo.json")); err != nil {
		t.Fatalf("repo.json must survive while other jobs share the repo: %v", err)
	}
}

func TestCleanupRestorePointStorageAsyncB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "rp-job", StorageDestID: dest.ID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("success uses job name", func(t *testing.T) {
		b458WriteFile(t, filepath.Join(storageDir, "rp-job", "run1", "data.tar"), "x")
		b458RunAsync(t, func() {
			r.CleanupRestorePointStorageAsync(jobID, 11, dest, "rp-job/run1")
		})
		if _, err := os.Stat(filepath.Join(storageDir, "rp-job", "run1")); !os.IsNotExist(err) {
			t.Fatalf("restore point dir should be gone, err=%v", err)
		}
	})

	t.Run("empty path unknown job", func(t *testing.T) {
		b458RunAsync(t, func() {
			r.CleanupRestorePointStorageAsync(99999, 12, dest, "")
		})
		if n := b458QueueLen(r); n != 0 {
			t.Fatalf("queue should be empty, has %d", n)
		}
	})

	t.Run("adapter error", func(t *testing.T) {
		bad := db.StorageDestination{Type: "bogus-type", Config: "{}"}
		b458RunAsync(t, func() {
			r.CleanupRestorePointStorageAsync(jobID, 13, bad, "rp-job/run2")
		})
		if n := b458QueueLen(r); n != 0 {
			t.Fatalf("queue should be empty, has %d", n)
		}
	})

	t.Run("delete failure", func(t *testing.T) {
		b458RunAsync(t, func() {
			r.CleanupRestorePointStorageAsync(jobID, 14, dest, "../escape")
		})
		if !b458ActivityContains(t, d, "Failed to delete backup files for deleted restore point 14") {
			t.Fatal("expected restore point cleanup failure activity entry")
		}
	})
}

func TestCleanupJobStorageErrorsB458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	if err := r.CleanupJobStorage(424242); err == nil {
		t.Fatal("missing job should error")
	}
	// Job whose destination row does not exist.
	jobID, err := d.CreateJob(db.Job{Name: "nodest", StorageDestID: 0, BackupTypeChain: "full"})
	if err == nil {
		if err := r.CleanupJobStorage(jobID); err == nil {
			t.Fatal("missing destination should error")
		}
	}
	// Adapter construction failure: a destination of an unknown type.
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "weird", Type: "bogus-type", Config: "{}"})
	if err != nil {
		t.Skipf("cannot create bogus destination: %v", err)
	}
	j2, err := d.CreateJob(db.Job{Name: "weird-job", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CleanupJobStorage(j2); err == nil {
		t.Fatal("adapter failure should error")
	}
	_ = storageDir
}
