package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// a458Adapter is a configurable in-memory storage.Adapter for driving error
// branches that a real local adapter cannot produce.
type a458Adapter struct {
	listFn   func(string) ([]storage.FileInfo, error)
	statFn   func(string) (storage.FileInfo, error)
	deleteFn func(string) error
}

func (a *a458Adapter) Write(string, io.Reader) error { return nil }
func (a *a458Adapter) WriteFrom(string, func() (io.ReadCloser, error)) error {
	return nil
}
func (a *a458Adapter) Read(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (a *a458Adapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (a *a458Adapter) Delete(p string) error {
	if a.deleteFn != nil {
		return a.deleteFn(p)
	}
	return nil
}
func (a *a458Adapter) List(p string) ([]storage.FileInfo, error) {
	if a.listFn != nil {
		return a.listFn(p)
	}
	return nil, nil
}
func (a *a458Adapter) Stat(p string) (storage.FileInfo, error) {
	if a.statFn != nil {
		return a.statFn(p)
	}
	return storage.FileInfo{}, nil
}
func (a *a458Adapter) TestConnection() error { return nil }
func (a *a458Adapter) GetCapacity(context.Context) (storage.Capacity, error) {
	return storage.Capacity{}, nil
}

// a458Runner opens a fresh on-disk DB and a hub-less Runner.
func a458Runner(t *testing.T) (*Runner, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(database, nil, nil), database
}

// a458LocalDest creates a local destination rooted at dir.
func a458LocalDest(t *testing.T, d *db.DB, name, dir string) db.StorageDestination {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{
		Name: name, Type: "local", Config: `{"path":"` + filepath.ToSlash(dir) + `"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	dest, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	return dest
}

func a458SetSetting(t *testing.T, d *db.DB, k, v string) {
	t.Helper()
	if err := d.SetSetting(k, v); err != nil {
		t.Fatalf("set %s: %v", k, err)
	}
}

// TestClosedDatabasePathsA458 drives the "database error is logged and the
// operation degrades" branches that only a failing DB can reach.
func TestClosedDatabasePathsA458(t *testing.T) {
	r, d := a458Runner(t)
	storageDir := t.TempDir()
	dest := a458LocalDest(t, d, "closed-db", storageDir)
	_ = d.Close()

	r.RunHealthChecks() // list error → early return

	if status, _ := r.checkOneStorage(dest); status != "ok" {
		t.Fatalf("checkOneStorage status %q, want ok (only persistence fails)", status)
	}
	if _, err := r.probeCapacity(context.Background(), dest, storage.NewLocalAdapter(storageDir)); err == nil {
		t.Fatal("probeCapacity: want persist error on closed DB")
	}

	_ = r.globalExcludePaths()
	if got := r.appdataPath(); got == "" {
		t.Fatal("appdataPath must fall back to the default")
	}
	if c := r.checkDecryptable(db.Job{Encryption: "age"}, "pw"); c.Status != "warn" {
		t.Fatalf("checkDecryptable status %q, want warn", c.Status)
	}
	if _, _, err := r.ScanStorageOrphans(dest); err == nil {
		t.Fatal("ScanStorageOrphans: want list-jobs error")
	}

	// DB backup still writes the file to the job destination but the
	// opt-in listing fails.
	r.backupDatabase(context.Background(), dest)

	b := NewBreaker(2, 2)
	b.RecordFailure(d, db.StorageDestination{ID: dest.ID, BreakerState: "closed"})                         // RecordDestinationFailure error
	b.RecordFailure(d, db.StorageDestination{ID: dest.ID, BreakerState: "closed", ConsecutiveFailures: 5}) // OpenBreaker error
	b.RecordSuccess(d, db.StorageDestination{ID: dest.ID, BreakerState: "closed"})                         // CloseBreaker error
	b.RecordSuccess(d, db.StorageDestination{ID: dest.ID, BreakerState: "open"})                           // RecordDestinationSuccess error
}

func TestBreakerNextStateSuccessA458(t *testing.T) {
	b := NewBreaker(0, 0)
	if s, n, opened := b.NextState("open", 7, true); s != "closed" || n != 0 || opened {
		t.Fatalf("success → (%q,%d,%v), want closed/0/false", s, n, opened)
	}
}

func TestBackupDatabaseFanOutA458(t *testing.T) {
	r, d := a458Runner(t)
	jobDest := a458LocalDest(t, d, "job-dest", t.TempDir())
	extraDir := t.TempDir()
	extra := a458LocalDest(t, d, "extra-dest", extraDir)
	for _, dst := range []db.StorageDestination{jobDest, extra} {
		dst.BackupDatabaseEnabled = true
		if err := d.UpdateStorageDestination(dst); err != nil {
			t.Fatalf("update dest: %v", err)
		}
	}

	// Job destination is also opted in → deduplicated; extra gets a copy.
	r.backupDatabase(context.Background(), jobDest)
	if entries, err := os.ReadDir(filepath.Join(extraDir, dbBackupBaseDir)); err != nil || len(entries) == 0 {
		t.Fatalf("extra destination got no DB backup: %v", err)
	}

	// A cancelled context stops the fan-out before the extra destination
	// and closes the job adapter immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.backupDatabase(ctx, extra)

	// A destination whose root is a regular file makes both writes fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.backupDatabaseToDest(context.Background(), db.StorageDestination{
		Name: "broken", Type: "local", Config: `{"path":"` + filepath.ToSlash(blocker) + `"}`,
	}, d.Path(), "")

	// An in-memory / pathless DB is never backed up.
	(&Runner{db: &db.DB{}}).backupDatabase(context.Background(), jobDest)
}

func TestPruneDBBackupsErrorsA458(t *testing.T) {
	pruneDBBackups(&a458Adapter{listFn: func(string) ([]storage.FileInfo, error) {
		return nil, errors.New("permission denied")
	}}, "x")
	pruneDBBackups(&a458Adapter{listFn: func(string) ([]storage.FileInfo, error) {
		return nil, fs.ErrNotExist
	}}, "x")

	var deleted []string
	var entries []storage.FileInfo
	for i := range dbBackupsKept + 2 {
		entries = append(entries, storage.FileInfo{Path: fmt.Sprintf("%s/vault.db.2026-01-%02dT00-00-00.db", dbBackupBaseDir, i+1)})
	}
	entries = append(entries, storage.FileInfo{Path: dbBackupBaseDir + "/sub", IsDir: true},
		storage.FileInfo{Path: dbBackupBaseDir + "/vault.db.latest.db"})
	pruneDBBackups(&a458Adapter{
		listFn: func(string) ([]storage.FileInfo, error) { return entries, nil },
		deleteFn: func(p string) error {
			deleted = append(deleted, p)
			return errors.New("delete refused")
		},
	}, "x")
	if len(deleted) != 2 {
		t.Fatalf("attempted %d deletes, want 2", len(deleted))
	}
}

type a458ErrReader struct{}

func (a458ErrReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

type a458PanicReader struct{}

func (a458PanicReader) Read([]byte) (int, error) { panic("reader exploded") }

func TestCompressErrorPathsA458(t *testing.T) {
	badGzip := []byte{0x1f, 0x8b, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, _, _, err := OpenStoredStream(bytes.NewReader(badGzip), "x.tar.gz", ""); err == nil {
		t.Fatal("OpenStoredStream: want gzip header error")
	}
	if _, _, _, err := decompressStoredReader(a458ErrReader{}, "x", ""); err == nil {
		t.Fatal("decompressStoredReader: want peek error")
	}
	rc, err := transportCompressReader("gzip", a458PanicReader{})
	if err != nil {
		t.Fatalf("transportCompressReader: %v", err)
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("want compressor panic surfaced as read error, got %v", err)
	}
}

func TestGlobalExcludePathsVariantsA458(t *testing.T) {
	r, d := a458Runner(t)
	a458SetSetting(t, d, "global_exclude_paths", "")
	if got := r.globalExcludePaths(); got != nil {
		t.Fatalf("empty setting → %v, want nil", got)
	}
	a458SetSetting(t, d, "global_exclude_paths", "{not json")
	if got := r.globalExcludePaths(); got != nil {
		t.Fatalf("invalid setting → %v, want nil", got)
	}
	if got := toStringSlice(42); got != nil {
		t.Fatalf("toStringSlice(int) = %v, want nil", got)
	}
}

func TestUniqueRunPathBranchesA458(t *testing.T) {
	root := t.TempDir()
	dest := db.StorageDestination{Name: "local", Type: "local", Config: `{"path":"` + filepath.ToSlash(root) + `"}`}
	mk := func(rel string, withFile bool) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if withFile {
			if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Existing non-empty base, empty "-2" → "-2" is reused.
	mk("job/a", true)
	mk("job/a-2", false)
	if got := uniqueRunPath(dest, "job/a", 7); got != "job/a-2" {
		t.Fatalf("got %q, want job/a-2", got)
	}

	// Base is a regular file → list error (not NotExist) → fallback.
	if err := os.WriteFile(filepath.Join(root, "job", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := uniqueRunPath(dest, "job/file", 8); got != "job/file-r8" {
		t.Fatalf("got %q, want job/file-r8", got)
	}

	// Every candidate taken → fallback.
	mk("job/b", true)
	for i := 2; i < maxRunPathAttempts; i++ {
		mk(fmt.Sprintf("job/b-%d", i), true)
	}
	if got := uniqueRunPath(dest, "job/b", 9); got != "job/b-r9" {
		t.Fatalf("got %q, want job/b-r9", got)
	}
}

func TestLTRProtectedSharedAndMissingParentsA458(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	points := []db.RestorePoint{
		{ID: 4, CreatedAt: now, ParentRestorePointID: 99}, // parent not in set
		{ID: 3, CreatedAt: now.Add(-time.Hour), ParentRestorePointID: 1},
		{ID: 2, CreatedAt: now.Add(-2 * time.Hour), ParentRestorePointID: 1},
		{ID: 1, CreatedAt: now.Add(-3 * time.Hour)},
	}
	got := ltrProtectedRestorePointIDs(points, LTRPolicy{KeepLatest: 10}, time.UTC)
	if len(got) != 4 {
		t.Fatalf("protected %v, want all 4", got)
	}
}

func TestRetainedDependencyCountsCycleA458(t *testing.T) {
	points := []db.RestorePoint{
		{ID: 1, ParentRestorePointID: 2},
		{ID: 2, ParentRestorePointID: 1},
	}
	counts := retainedDependencyCounts(points, map[int64]struct{}{1: {}})
	if counts[2] != 1 {
		t.Fatalf("counts %v, want parent 2 counted once", counts)
	}
}

func TestRunScriptTimeoutsA458(t *testing.T) {
	dir := t.TempDir()
	quick := filepath.Join(dir, "quick.sh")
	if err := writeExecutableScript(quick, "#!/bin/sh\necho hi\n"); err != nil {
		t.Fatal(err)
	}
	if out, err := runScript(quick, 0, nil); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("zero timeout should use default: %q %v", out, err)
	}
	slow := filepath.Join(dir, "slow.sh")
	if err := writeExecutableScript(slow, "#!/bin/sh\nexec sleep 30\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := runScript(slow, 20*time.Millisecond, nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestPreflightUnknownTypeA458(t *testing.T) {
	if err := Preflight(context.Background(), db.StorageDestination{Type: "bogus"}); !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("got %v, want ErrPreflightFailed", err)
	}
}

func TestScheduleRetryDefaultDelaysA458(t *testing.T) {
	r, d := a458Runner(t)
	a458SetSetting(t, d, "retry_delays_default", "")
	run := db.JobRun{}
	r.scheduleRetryIfDue(&run, db.Job{ID: 1}, db.StorageDestination{ID: 1}, runOptions{})
	if run.RetryNextAt == nil {
		t.Fatal("expected a retry to be scheduled with built-in delays")
	}
}

func TestRunLogUnmarshalableDataA458(t *testing.T) {
	r, _ := a458Runner(t)
	// A channel cannot be JSON-encoded; the entry is still appended without data.
	r.runLog(1, runLogLevelInfo, "msg", map[string]any{"c": make(chan int)})
}

func TestOrphansBranchesA458(t *testing.T) {
	r, d := a458Runner(t)
	root := t.TempDir()
	dest := a458LocalDest(t, d, "orph", root)
	other := a458LocalDest(t, d, "other", t.TempDir())
	if _, err := d.CreateJob(db.Job{Name: "other-job", StorageDestID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphans, _, err := r.ScanStorageOrphans(dest)
	if err != nil || len(orphans) != 1 {
		t.Fatalf("orphans %v err %v, want 1", orphans, err)
	}

	// Rescan failure aborts deletion.
	if n, errs := r.DeleteStorageOrphans(db.StorageDestination{Type: "bogus"}, []string{"x"}); n != 0 || len(errs) != 1 {
		t.Fatalf("got %d %v, want rescan failure", n, errs)
	}

	// A destination root that is a file cannot be walked.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileDest := db.StorageDestination{ID: 999, Type: "local", Config: `{"path":"` + filepath.ToSlash(blocker) + `"}`}
	if _, _, err := r.ScanStorageOrphans(fileDest); err == nil {
		t.Fatal("want walk error for a file root")
	}
}
