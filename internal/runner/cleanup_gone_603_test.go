package runner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// raceAdapter lists a restore point's files, then fails every delete with err:
// another cleanup removed them in between, or the delete really failed.
type raceAdapter struct {
	storage.Adapter
	err     error
	deletes []string
}

func (a *raceAdapter) List(prefix string) ([]storage.FileInfo, error) {
	if prefix == "job/run" {
		return []storage.FileInfo{{Path: "job/run/data.tar"}, {Path: "job/run/sub", IsDir: true}}, nil
	}
	return nil, nil
}

func (a *raceAdapter) Delete(p string) error {
	a.deletes = append(a.deletes, p)
	return &fs.PathError{Op: "remove", Path: "/mnt/user/backups/" + p, Err: a.err}
}

// Issue #603: a restore point's files that are already gone count as deleted,
// while a delete that really fails is still reported.
func TestDeleteStorageDirAlreadyGone(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	gone := &raceAdapter{err: syscall.ENOENT}
	if err := r.DeleteStorageDir(gone, "job/run"); err != nil {
		t.Errorf("already-gone files = %v, want nil", err)
	}
	if len(gone.deletes) != 3 {
		t.Errorf("deletes = %v", gone.deletes)
	}
	denied := &raceAdapter{err: syscall.EACCES}
	err := r.DeleteStorageDir(denied, "job/run")
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("permission denied = %v, want it reported", err)
	}
}

// Deleting a restore point whose directory the job's own cleanup already
// removed logs no error, end to end on a local destination.
func TestCleanupRestorePointAlreadyGoneLogsNoError(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "gone-job", StorageDestID: dest.ID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storageDir, "gone-job"), 0o755); err != nil {
		t.Fatal(err)
	}
	b458RunAsync(t, func() {
		r.CleanupRestorePointStorageAsync(jobID, 191, dest, "gone-job/2026-11-03_142151")
	})
	if b458ActivityContains(t, d, "Failed to delete backup files for deleted restore point 191") {
		t.Fatal("already-gone restore point reported as a cleanup failure")
	}
}
