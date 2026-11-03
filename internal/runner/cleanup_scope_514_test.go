package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
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
	write514(t, root, "appdata/2026-01-02_000000/manifest.json", `{"job_name":"appdata","run_id":1,"backup_type":"full","items_done":1}`)
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
	write514(t, root, "ret/2026-01-09_000000/manifest.json", `{"job_name":"ret","run_id":1,"backup_type":"full","items_done":1}`) // newest, kept by count

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
	write514(t, root, "ok/run/manifest.json", `{"job_name":"ok","run_id":1,"backup_type":"full","items_done":1}`)
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

// A manifest.json that is not valid JSON, or valid JSON without a job_name,
// is not Vault's.
func TestHasManifestRejectsForeignFiles514(t *testing.T) {
	t.Parallel()
	_, _, root := setupTestRunner(t)
	write514(t, root, "a/run/manifest.json", "not json")
	write514(t, root, "b/run/manifest.json", `{"name":"web app"}`)
	// Another app's file that happens to use job_name is not Vault's either.
	write514(t, root, "e/run/manifest.json", `{"job_name":"x"}`)
	// Vault's keys under a directory that is not the job's name: not Vault's.
	write514(t, root, "f/run/manifest.json", `{"job_name":"other","run_id":1,"backup_type":"full","items_done":1}`)
	// An S3-sanitised job directory still matches its job_name.
	write514(t, root, "My_Job/run/manifest.json", `{"job_name":"My Job","run_id":1,"backup_type":"full","items_done":1}`)
	write514(t, root, "c/run/manifest.json", `{"job_name":"c","run_id":1,"backup_type":"full","items_done":1}`)
	if err := os.MkdirAll(filepath.Join(root, "d", "run", "manifest.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := localAdapter514(t, root)
	for dir, want := range map[string]bool{"a/run": false, "b/run": false, "c/run": true, "d/run": false, "e/run": false, "f/run": false, "My_Job/run": true, "missing/run": false} {
		if got := hasManifest(a, dir, noUnseal); got != want {
			t.Errorf("hasManifest(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestSweepManagedRunDirsErrors514(t *testing.T) {
	t.Parallel()
	r, _, root := setupTestRunner(t)
	write514(t, root, "plain-file", "x")
	a := localAdapter514(t, root)
	if err := r.sweepManagedRunDirs(a, db.StorageDestination{}, 0, ".."); !errors.Is(err, ErrCleanupRefused) {
		t.Errorf("sweep of ..: %v, want ErrCleanupRefused", err)
	}
	if err := r.sweepManagedRunDirs(a, db.StorageDestination{}, 0, "plain-file"); err == nil {
		t.Error("sweep of a file: want a listing error")
	}
	if err := r.sweepManagedRunDirs(a, db.StorageDestination{}, 0, "missing"); err != nil {
		t.Errorf("sweep of a missing dir: %v, want nil", err)
	}
}

func TestCheckImportPathOwnedAppdataBackup514(t *testing.T) {
	t.Parallel()
	_, _, root := setupTestRunner(t)
	write514(t, root, "ab_20260101_000000/plex.tar.gz", "x")
	if err := os.MkdirAll(filepath.Join(root, "ab_20260101_000000", "dir.tar.gz"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := localAdapter514(t, root)
	ab := map[string]any{"source": "appdata.backup"}
	for p, ok := range map[string]bool{
		"ab_20260101_000000/plex.tar.gz":    true,
		"ab_20260101_000000/missing.tar.gz": false,
		"ab_20260101_000000/dir.tar.gz":     false,
		"other/plex.tar.gz":                 false,
		"ab_20260101_000000/notes.txt":      false,
	} {
		if err := checkImportPathOwned(a, ab, p, noUnseal); (err == nil) != ok {
			t.Errorf("checkImportPathOwned(%q) = %v, want ok=%v", p, err, ok)
		}
	}
}

// A job name that is not a relative path cannot be swept; the async cleanup
// reports it instead of claiming success.
func TestCleanupJobStorageAsyncRefusedJobDir514(t *testing.T) {
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	b458RunAsync(t, func() {
		r.CleanupJobStorageAsync(1, "..", dest, nil)
	})
	if !b458ActivityContains(t, d, `Failed to delete backup files for deleted job ".."`) {
		t.Fatal("a refused job directory sweep should be reported")
	}
}

// Database errors during a destination wipe are reported, and the layout
// sweep still runs; restore points without a path are skipped.
func TestCleanupStorageDestinationDatabaseErrors514(t *testing.T) {
	t.Parallel()
	t.Run("restore points unreadable", func(t *testing.T) {
		t.Parallel()
		r, d, root := setupTestRunner(t)
		dest := createLocalDest(t, d, root)
		if _, err := d.CreateJob(db.Job{Name: "j", StorageDestID: dest.ID}); err != nil {
			t.Fatal(err)
		}
		write514(t, root, "j/run/manifest.json", `{"job_name":"j","run_id":1,"backup_type":"full","items_done":1}`)
		if _, err := d.Exec(`ALTER TABLE restore_points RENAME TO rp_hidden_514`); err != nil {
			t.Fatal(err)
		}
		if err := r.CleanupStorageDestination(dest); err == nil {
			t.Fatal("want the restore point listing error")
		}
		if exists514(t, root, "j/run") {
			t.Error("the layout sweep should still remove managed runs")
		}
	})
	t.Run("jobs unreadable", func(t *testing.T) {
		t.Parallel()
		r, d, root := setupTestRunner(t)
		dest := createLocalDest(t, d, root)
		if _, err := d.Exec(`ALTER TABLE jobs RENAME TO jobs_hidden_514`); err != nil {
			t.Fatal(err)
		}
		if err := r.CleanupStorageDestination(dest); err == nil {
			t.Fatal("want the job listing error")
		}
	})
	t.Run("empty and refused recorded paths", func(t *testing.T) {
		t.Parallel()
		r, d, root := setupTestRunner(t)
		dest := createLocalDest(t, d, root)
		jobID, _ := d.CreateJob(db.Job{Name: "j", StorageDestID: dest.ID})
		other := createNamedLocalDest(t, d, "other-514", t.TempDir())
		otherID, _ := d.CreateJob(db.Job{Name: "elsewhere", StorageDestID: other.ID})
		runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
		otherRun, _ := d.CreateJobRun(db.JobRun{JobID: otherID, Status: "success"})
		for _, rp := range []db.RestorePoint{
			{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "", Metadata: "{}"},
			{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "plugins", Metadata: "{}"}, // refused: one component
			{JobRunID: otherRun, JobID: otherID, BackupType: "full", StoragePath: "elsewhere/run", Metadata: "{}"},
		} {
			if _, err := d.CreateRestorePoint(rp); err != nil {
				t.Fatal(err)
			}
		}
		write514(t, root, "elsewhere/run/data.tar", "x") // another destination's row; not touched
		write514(t, root, "plugins/dynamix/page.php", "keep")
		if err := r.CleanupStorageDestination(dest); !errors.Is(err, ErrCleanupRefused) {
			t.Fatalf("CleanupStorageDestination = %v, want ErrCleanupRefused for the recorded %q", err, "plugins")
		}
		if !exists514(t, root, "plugins/dynamix/page.php") {
			t.Error("a refused recorded path was deleted")
		}
		if !exists514(t, root, "elsewhere/run/data.tar") {
			t.Error("a restore point of another destination was deleted")
		}
	})
}

// failingReadAdapter wraps a real adapter but fails manifest reads, either
// when opening the file or while reading it.
type failingReadAdapter struct {
	storage.Adapter
	openErr bool
}

type manifestErrReader struct{}

func (manifestErrReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (manifestErrReader) Close() error             { return nil }

func (f failingReadAdapter) Read(p string) (io.ReadCloser, error) {
	if f.openErr {
		return nil, errors.New("open failed")
	}
	return manifestErrReader{}, nil
}

// A manifest that exists but cannot be read is not proof of ownership.
func TestHasManifestReadFailures514(t *testing.T) {
	t.Parallel()
	_, _, root := setupTestRunner(t)
	write514(t, root, "j/run/manifest.json", `{"job_name":"j","run_id":1,"backup_type":"full","items_done":1}`)
	base := localAdapter514(t, root)
	if !hasManifest(base, "j/run", noUnseal) {
		t.Fatal("precondition: a readable manifest is accepted")
	}
	for name, a := range map[string]storage.Adapter{
		"open fails": failingReadAdapter{Adapter: base, openErr: true},
		"read fails": failingReadAdapter{Adapter: base},
	} {
		if hasManifest(a, "j/run", noUnseal) {
			t.Errorf("%s: hasManifest accepted an unreadable manifest", name)
		}
	}
}

// noDirRemover hides the optional RemoveEmptyDir method, like object stores.
type noDirRemover struct{ storage.Adapter }

// Without directory removal, deleting a run leaves its now-empty parents
// alone (object stores have no directories to remove).
func TestDeleteStorageDirWithoutDirRemoval514(t *testing.T) {
	t.Parallel()
	r, _, root := setupTestRunner(t)
	write514(t, root, "job/run/data.tar", "x")
	a := noDirRemover{localAdapter514(t, root)}
	if _, ok := any(a).(interface{ RemoveEmptyDir(string) error }); ok {
		t.Fatal("precondition: the double must not expose RemoveEmptyDir")
	}
	if err := r.DeleteStorageDir(a, "job/run"); err != nil {
		t.Fatalf("DeleteStorageDir: %v", err)
	}
	if exists514(t, root, "job/run") || !exists514(t, root, "job") {
		t.Fatal("want the run gone and its parent left in place")
	}
}

// A destination whose root does not exist yet has nothing to delete.
func TestCleanupStorageDestinationMissingRoot514(t *testing.T) {
	t.Parallel()
	r, d, _ := setupTestRunner(t)
	dest := createLocalDest(t, d, filepath.Join(t.TempDir(), "not-created-yet"))
	if err := r.CleanupStorageDestination(dest); err != nil {
		t.Fatalf("CleanupStorageDestination on a missing root: %v", err)
	}
}

// AppData Backup's flash backups are .zip archives.
func TestCheckImportPathOwnedZip514(t *testing.T) {
	t.Parallel()
	_, _, root := setupTestRunner(t)
	write514(t, root, "ab_20260101_000000/tower-flash.zip", "x")
	a := localAdapter514(t, root)
	if err := checkImportPathOwned(a, map[string]any{"source": "appdata.backup"}, "ab_20260101_000000/tower-flash.zip", noUnseal); err != nil {
		t.Fatalf("zip archive rejected: %v", err)
	}
}

// An encrypted envelope proves ownership only once it decrypts to a manifest
// that names its parent directory. The marker alone is one forgeable field.
func TestHasManifestAuthenticatesEncryptedEnvelope(t *testing.T) {
	t.Parallel()
	_, _, root := setupTestRunner(t)
	const pass = "correct horse"
	seal := func(job string) string {
		t.Helper()
		plain := fmt.Sprintf(`{"job_name":%q,"run_id":1,"backup_type":"full","items_done":1}`, job)
		rd, err := crypto.EncryptReader(pass, strings.NewReader(plain))
		if err != nil {
			t.Fatal(err)
		}
		ct, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		_ = rd.Close()
		env, err := encodeManifestEnvelope("age", "age-v1", ct)
		if err != nil {
			t.Fatal(err)
		}
		return string(env)
	}
	marker := `{"vault_manifest_enc":1,"key":"age","algo":"age","payload":"AAAA"}`
	write514(t, root, "real/run/manifest.json", seal("real"))
	write514(t, root, "marker/run/manifest.json", marker)
	write514(t, root, "unknown/run/manifest.json", `{"vault_manifest_enc":1,"key":"other","algo":"x","payload":"AAAA"}`)
	write514(t, root, "badb64/run/manifest.json", `{"vault_manifest_enc":1,"key":"age","algo":"age","payload":"!"}`)
	// Decrypts and names its directory, but is not a Vault manifest.
	rd, err := crypto.EncryptReader(pass, strings.NewReader(`{"job_name":"shape"}`))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	_ = rd.Close()
	shape, err := encodeManifestEnvelope("age", "age-v1", ct)
	if err != nil {
		t.Fatal(err)
	}
	write514(t, root, "shape/run/manifest.json", string(shape))
	// A genuine envelope copied into an unrelated directory.
	write514(t, root, "unrelated/run/manifest.json", seal("real"))
	a := localAdapter514(t, root)

	unseal := func(env manifestEnvelope) ([]byte, error) {
		return unsealManifest(env, func() (*dedup.Repo, error) { return nil, errors.New("no repo") }, pass)
	}
	for dir, want := range map[string]bool{"real/run": true, "marker/run": false, "unknown/run": false, "badb64/run": false, "unrelated/run": false, "shape/run": false} {
		if got := hasManifest(a, dir, unseal); got != want {
			t.Errorf("hasManifest(%q) = %v, want %v", dir, got, want)
		}
	}
	// A server that cannot decrypt it keeps it, even if it is genuine.
	noPass := func(env manifestEnvelope) ([]byte, error) {
		return unsealManifest(env, nil, "")
	}
	if hasManifest(a, "real/run", noPass) {
		t.Error("envelope accepted without a passphrase")
	}
	dedupEnv, err := encodeManifestEnvelope("dedup", "aes-256-gcm", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	write514(t, root, "dedupjob/run/manifest.json", string(dedupEnv))
	if hasManifest(a, "dedupjob/run", unseal) {
		t.Error("dedup envelope accepted with no repository to verify it")
	}
}

// The destination sweep must leave a directory holding only a forged marker.
func TestCleanupDestinationKeepsForgedEnvelopeDirectory(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "forged-dest", root)
	write514(t, root, "photos/2024/manifest.json", `{"vault_manifest_enc":1,"key":"age","algo":"age","payload":"AAAA"}`)
	write514(t, root, "photos/2024/family.jpg", "precious")
	if err := r.CleanupStorageDestination(dest); err != nil {
		t.Fatalf("CleanupStorageDestination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "photos/2024/family.jpg")); err != nil {
		t.Fatalf("user file was deleted: %v", err)
	}
	// Import reaches the same check, even when the caller omits "encrypted".
	n, err := r.ImportBackups(dest.ID, []map[string]any{{"job_name": "photos", "storage_path": "photos/2024"}})
	if err != nil || n != 0 {
		t.Fatalf("import of a forged envelope = %d, %v", n, err)
	}
}

// A path another job's restore point covers is not deleted for this job, by
// retention or by the job-directory sweep.
func TestDeleteRefusesPathHeldByOtherJob(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "shared-dest", root)
	owner, err := database.CreateJob(db.Job{Name: "owner", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateJob(db.Job{Name: "other", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRestorePoint(db.RestorePoint{JobRunID: authRun(t, database, owner), JobID: owner, BackupType: "full", StoragePath: "owner/run"}); err != nil {
		t.Fatal(err)
	}
	manifest := `{"job_name":"owner","run_id":1,"backup_type":"full","items_done":1}`
	write514(t, root, "owner/run/manifest.json", manifest)
	write514(t, root, "owner/free/manifest.json", manifest)
	a := localAdapter514(t, root)

	if err := r.deleteJobRestorePointDir(a, dest.ID, other, "owner/run"); !errors.Is(err, ErrCleanupRefused) {
		t.Fatalf("delete of another job's path = %v", err)
	}
	if err := r.deleteJobRestorePointDir(a, dest.ID, other, "owner/run/sub"); !errors.Is(err, ErrCleanupRefused) {
		t.Fatalf("delete below another job's path = %v", err)
	}
	if err := r.sweepManagedRunDirs(a, dest, other, "owner"); !errors.Is(err, ErrCleanupRefused) {
		t.Fatalf("sweep = %v, want the held directory refused", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owner/run/manifest.json")); err != nil {
		t.Fatalf("held restore point deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owner/free")); !os.IsNotExist(err) {
		t.Fatalf("unheld run directory survived: %v", err)
	}
	// The owner itself still deletes it.
	if err := r.deleteJobRestorePointDir(a, dest.ID, owner, "owner/run"); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	// Uncertainty refuses too.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.deleteJobRestorePointDir(a, dest.ID, other, "owner/x"); !errors.Is(err, ErrCleanupRefused) {
		t.Fatalf("delete with an unreadable database = %v", err)
	}
}

// An imported AppData Backup restore point is a single archive file: it is
// deleted directly, while a lookalike file outside that shape is refused.
func TestDeleteRestorePointAppdataArchive514(t *testing.T) {
	t.Parallel()
	r, _, root := setupTestRunner(t)
	write514(t, root, "ab_20260101_000000/plex.tar.gz", "x")
	write514(t, root, "ab_20260101_000000/tower-flash.zip", "x")
	write514(t, root, "ab_20260101_000000/keep.tar.gz", "x")
	write514(t, root, "docs/report.tar.gz", "keep")
	a := localAdapter514(t, root)
	for _, p := range []string{"ab_20260101_000000/plex.tar.gz", "ab_20260101_000000/tower-flash.zip"} {
		if err := r.deleteRestorePointDir(a, p); err != nil {
			t.Fatalf("deleteRestorePointDir(%q): %v", p, err)
		}
		if exists514(t, root, p) {
			t.Errorf("%s should be deleted", p)
		}
	}
	if !exists514(t, root, "ab_20260101_000000/keep.tar.gz") {
		t.Error("another archive in the folder was deleted")
	}
	if err := r.deleteRestorePointDir(a, "docs/report.tar.gz"); err == nil {
		t.Error("a file outside an ab_* folder was treated as an archive restore point")
	}
	if !exists514(t, root, "docs/report.tar.gz") {
		t.Error("a non-appdata file was deleted")
	}
	// An already-removed archive is clean, not an error.
	if err := r.deleteRestorePointDir(a, "ab_20260101_000000/plex.tar.gz"); err != nil {
		t.Errorf("deleting a missing archive: %v", err)
	}
}

// noUnseal stands in for a server with no key that opens an envelope.
func noUnseal(manifestEnvelope) ([]byte, error) { return nil, errors.New("cannot unseal") }
