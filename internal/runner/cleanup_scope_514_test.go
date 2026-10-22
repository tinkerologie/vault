package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

func write514(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists514(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func localAdapter514(t *testing.T, root string) storage.Adapter {
	t.Helper()
	a, err := storage.NewAdapter("local", `{"path":"`+root+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.CloseAdapter(a) })
	return a
}

// The recursive delete must never start from the destination root or from
// a path that escapes it (#514).
func TestDeleteStorageDirRejectsRootAliases514(t *testing.T) {
	t.Parallel()
	r, _, root := setupTestRunner(t)
	write514(t, root, "keep/file.txt", "x")
	a := localAdapter514(t, root)
	for _, p := range []string{"", " ", ".", "./", "/", "..", "../x", "/abs"} {
		if err := r.DeleteStorageDir(a, p); !errors.Is(err, ErrCleanupRefused) {
			t.Errorf("DeleteStorageDir(%q) = %v, want ErrCleanupRefused", p, err)
		}
	}
	if !exists514(t, root, "keep/file.txt") {
		t.Fatal("content was deleted")
	}
}

func TestDeleteRestorePointDirRequiresManagedLayout514(t *testing.T) {
	t.Parallel()
	r, _, root := setupTestRunner(t)
	write514(t, root, "appdata/plex/config.xml", "x")
	write514(t, root, "job/run/data.tar", "x")
	a := localAdapter514(t, root)

	for _, p := range []string{"appdata", ".", "", "/appdata/plex", "../appdata/plex"} {
		if err := r.deleteRestorePointDir(a, p); !errors.Is(err, ErrCleanupRefused) {
			t.Errorf("deleteRestorePointDir(%q) = %v, want ErrCleanupRefused", p, err)
		}
	}
	if !exists514(t, root, "appdata/plex/config.xml") {
		t.Fatal("a refused path was deleted")
	}
	// A recorded <job>/<run> path is deleted even without a manifest: the
	// manifest write is best-effort, and the database row is the evidence.
	if err := r.deleteRestorePointDir(a, "job/run"); err != nil {
		t.Fatalf("deleteRestorePointDir(job/run): %v", err)
	}
	if exists514(t, root, "job/run") {
		t.Fatal("job/run should be gone")
	}
}

// Job deletion sweeps only managed run folders from a directory that shares
// the job's name; foreign content survives.
func TestCleanupJobStorageKeepsForeignContent514(t *testing.T) {
	t.Parallel()
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	jobID, err := d.CreateJob(db.Job{Name: "appdata", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"appdata/2026-01-01_000000", "appdata"} { // a bad row too
		if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: p, Metadata: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	write514(t, root, "appdata/2026-01-01_000000/data.tar", "x")
	write514(t, root, "appdata/2026-01-02_000000/manifest.json", `{"job_name":"j"}`)
	write514(t, root, "appdata/plex/config.xml", "keep")

	if err := r.CleanupJobStorage(jobID); err != nil {
		t.Fatalf("CleanupJobStorage: %v", err)
	}
	if exists514(t, root, "appdata/2026-01-01_000000") || exists514(t, root, "appdata/2026-01-02_000000") {
		t.Error("managed run folders should be gone")
	}
	if !exists514(t, root, "appdata/plex/config.xml") {
		t.Error("foreign content under the job's directory was deleted")
	}
}

// A refused recorded path is a real cleanup failure and surfaces through the
// failure event and activity log.
func TestCleanupJobStorageAsyncRefusalFails514(t *testing.T) {
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	write514(t, root, "plugins/dynamix/page.php", "keep")
	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(1, "jobX", dest, []string{"plugins"})
	})
	if !exists514(t, root, "plugins/dynamix/page.php") {
		t.Fatal("refused path was deleted")
	}
	if !b458ActivityContains(t, d, `Failed to delete backup files for deleted job "jobX"`) {
		t.Fatal("refusal should be reported as a cleanup failure")
	}
}

func TestCleanupRestorePointAsyncRefusal514(t *testing.T) {
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	write514(t, root, "languages/en.txt", "keep")
	b458RunAsync(t, func() {
		r.CleanupRestorePointStorageAsync(1, 77, dest, "languages")
	})
	if !exists514(t, root, "languages/en.txt") {
		t.Fatal("refused path was deleted")
	}
	if !b458ActivityContains(t, d, "Failed to delete backup files for deleted restore point 77") {
		t.Fatal("refusal should be reported")
	}
}

// Retention trusts a recorded path only when it has the managed shape.
func TestEnforceRetentionRefusesUnmanagedPath514(t *testing.T) {
	t.Parallel()
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	jobID, err := d.CreateJob(db.Job{Name: "ret", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"usr", "ret/2026-01-01_000000"} {
		runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: p, Metadata: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	write514(t, root, "usr/local/emhttp/index.php", "keep")
	write514(t, root, "ret/2026-01-01_000000/data.tar", "x")
	write514(t, root, "ret/2026-01-09_000000/manifest.json", `{"job_name":"j"}`) // newest, kept by count

	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "ret/2026-01-09_000000", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	// Age the two expiring rows so the count-based keep picks the newest.
	if _, err := d.Exec(`UPDATE restore_points SET created_at = '2020-01-01 00:00:00' WHERE storage_path IN ('usr', 'ret/2026-01-01_000000')`); err != nil {
		t.Fatal(err)
	}
	r.enforceRetention(t.Context(), dest, jobID, 1, 0)

	if !exists514(t, root, "usr/local/emhttp/index.php") {
		t.Fatal("retention deleted an unmanaged recorded path")
	}
	if exists514(t, root, "ret/2026-01-01_000000") {
		t.Fatal("managed expired run should be gone")
	}
	if !exists514(t, root, "ret/2026-01-09_000000/manifest.json") {
		t.Fatal("kept run was deleted")
	}
}

// The destination wipe removes the dedup repository but keeps the daemon's
// database backups, and removes recorded restore points at any depth.
func TestCleanupStorageDestinationDedupAndRecorded514(t *testing.T) {
	t.Parallel()
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	jobID, err := d.CreateJob(db.Job{Name: "Daily/Docker", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if _, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "Daily/Docker/2026-01-01_000000", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	write514(t, root, "Daily/Docker/2026-01-01_000000/data.tar", "x")
	write514(t, root, "_vault/repo.json", "{}")
	write514(t, root, "_vault/packs/aa/pack1", "x")
	write514(t, root, "_vault/index/idx1", "x")
	write514(t, root, "_vault/vault.db.latest", "keep")

	if err := r.CleanupStorageDestination(dest); err != nil {
		t.Fatalf("CleanupStorageDestination: %v", err)
	}
	if exists514(t, root, "Daily") {
		t.Error("recorded nested restore point and its empty parents should be gone")
	}
	for _, gone := range []string{"_vault/repo.json", "_vault/packs", "_vault/index"} {
		if exists514(t, root, gone) {
			t.Errorf("%s should be gone", gone)
		}
	}
	if !exists514(t, root, "_vault/vault.db.latest") {
		t.Error("database backup must survive")
	}
}

// A destination whose root cannot be listed is unavailable: nothing is
// attempted, so the caller keeps the record.
func TestCleanupStorageDestinationReportsListError514(t *testing.T) {
	t.Parallel()
	r, d, _ := setupTestRunner(t)
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := createLocalDest(t, d, file)
	if err := r.CleanupStorageDestination(dest); !errors.Is(err, ErrCleanupUnavailable) {
		t.Fatalf("want ErrCleanupUnavailable for an unlistable root, got %v", err)
	}
}

func TestCleanupStorageDestinationUnsafeRoot514(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	err := r.CleanupStorageDestination(db.StorageDestination{Type: "local", Config: `{"path":""}`})
	if !errors.Is(err, ErrCleanupUnavailable) {
		t.Fatalf("want ErrCleanupUnavailable, got %v", err)
	}
}

// ImportBackups refuses client-supplied paths that are not managed backups
// on the destination, so an imported row can never authorise a later
// recursive delete of unrelated data.
func TestImportBackupsRejectsUnmanagedPaths514(t *testing.T) {
	t.Parallel()
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	write514(t, root, "appdata/plex/config.xml", "x") // two components, no manifest
	write514(t, root, "ok/run/manifest.json", `{"job_name":"j"}`)
	write514(t, root, "ab_20260101_000000/notes.txt", "x")
	write514(t, root, "Sites/blog/manifest.json", `{"name":"blog"}`) // not a Vault manifest

	backups := []map[string]any{
		{"job_name": "a", "storage_path": "."},
		{"job_name": "b", "storage_path": "/mnt/user/appdata"},
		{"job_name": "c", "storage_path": "../escape/run"},
		{"job_name": "d", "storage_path": "appdata"},
		{"job_name": "e", "storage_path": "appdata/plex"},
		{"job_name": "f", "source": "appdata.backup", "storage_path": "appdata/plex"},
		{"job_name": "g", "source": "appdata.backup", "storage_path": "ab_20260101_000000/notes.txt"},
		{"job_name": "h", "storage_path": "Sites/blog"},
		{"job_name": "ok", "storage_path": "ok/run"},
	}
	n, err := r.ImportBackups(dest.ID, backups)
	if err != nil {
		t.Fatalf("ImportBackups: %v", err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want only the managed entry", n)
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if _, err := d.GetJobByName(name); err == nil {
			t.Errorf("job %q was created from an unmanaged path", name)
		}
	}
}
