package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

// c458WriteFromAdapter drives the reopen closure like the real retrying
// adapters do, returning its error (or an injected one) to the caller.
type c458WriteFromAdapter struct {
	storage.Adapter
	fail error
}

func (a c458WriteFromAdapter) WriteFrom(_ string, open func() (io.ReadCloser, error)) error {
	rc, err := open()
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
	return a.fail
}

// c458DeleteFailAdapter records nothing and fails every delete.
type c458DeleteFailAdapter struct{ storage.Adapter }

func (c458DeleteFailAdapter) Delete(string) error { return errors.New("delete refused") }

// c458BlockedLocalDest returns a local destination whose storage root holds a
// regular file named "blocker", so any storage path under it cannot be created.
func c458BlockedLocalDest(t *testing.T, d *db.DB) db.StorageDestination {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return createNamedLocalDest(t, d, "c458-blocked", root)
}

func c458StagedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUploadOneStagedErrorsC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	noop := func(int, string) {}

	// Staged file vanished: reopen fails.
	if _, _, _, err := r.uploadOneStaged(context.Background(), c458WriteFromAdapter{}, t.TempDir(), "missing", "rp", "", "none", "folder", "i", noop, nil); err == nil || !strings.Contains(err.Error(), "writing") {
		t.Fatalf("missing staged file: %v", err)
	}

	// Write fails after cancellation: reported as a cancelled upload.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := c458StagedDir(t)
	if _, _, _, err := r.uploadOneStaged(ctx, c458WriteFromAdapter{fail: errors.New("closed")}, dir, "data.tar", "rp", "pw", "gzip", "vm", "i", noop, nil); err == nil || !strings.Contains(err.Error(), "upload cancelled") {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestUploadStagedFilesNBranchesC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, d, storageDir)

	// concurrency < 1 is clamped; directories are skipped; verify passes.
	sums, err := r.uploadStagedFilesN(context.Background(), 0, c458StagedDir(t), dest, "c458/rp", true, "", "none", "folder", "item", 0)
	if err != nil || len(sums) != 1 {
		t.Fatalf("happy upload: %v %v", sums, err)
	}

	// Pre-cancelled context: nothing is uploaded and the cancellation is
	// reported.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.uploadStagedFilesN(ctx, 0, c458StagedDir(t), dest, "c458/rp2", false, "", "none", "folder", "item", 1); err == nil || !strings.Contains(err.Error(), "upload cancelled") {
		t.Fatalf("cancelled: %v", err)
	}

	// Write failure: the storage path sits beneath a regular file.
	blocked := c458BlockedLocalDest(t, d)
	if _, err := r.uploadStagedFilesN(context.Background(), 0, c458StagedDir(t), blocked, "blocker/rp", false, "", "none", "folder", "item", 1); err == nil {
		t.Fatal("write under a file should fail")
	}
}

func TestCleanupPartialUploadsDeleteFailureC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	r.cleanupPartialUploads(c458DeleteFailAdapter{}, "rp", nil)
	r.cleanupPartialUploads(c458DeleteFailAdapter{}, "rp", map[string]string{"a": "", "b": "x"})
}

func TestStageItemLocallyErrorsC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	if err := d.SetSetting("staging_dir_override", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dest := createLocalDest(t, d, storageDir)

	// Folder backup with no path fails inside the engine.
	_, _, cleanup, err := r.stageItemLocally(context.Background(), 0, engine.BackupItem{Name: "f", Type: "folder", Settings: map[string]any{}}, dest)
	cleanup()
	if err == nil || !strings.Contains(err.Error(), "backup f") {
		t.Fatalf("folder without path: %v", err)
	}

	// Plugin and ZFS items of things that do not exist fail either at
	// handler construction (non-Linux stubs) or during backup.
	for _, typ := range []string{"plugin", "zfs"} {
		_, _, cleanup, err := r.stageItemLocally(context.Background(), 0, engine.BackupItem{
			Name: "c458-nonexistent", Type: typ,
			Settings: map[string]any{"path": filepath.Join(t.TempDir(), "absent"), "dataset": "c458/none"},
		}, dest)
		cleanup()
		if err == nil {
			t.Fatalf("%s backup of a nonexistent target should fail", typ)
		}
	}
}

func TestBackupItemErrorsC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	if err := d.SetSetting("staging_dir_override", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	// Dedup destination with an unknown item type.
	dd := makeDedupDest(t, d, storageDir)
	if _, _, err := r.backupItem(context.Background(), 0, engine.BackupItem{Name: "x", Type: "bogus"}, dd, "p", false, "", "none", 1, nil); err == nil {
		t.Fatal("unknown type on dedup dest should fail")
	}

	// Classic path: staging succeeds but the upload fails.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := c458BlockedLocalDest(t, d)
	item := engine.BackupItem{Name: "src", Type: "folder", Settings: map[string]any{"path": src}}
	if _, _, err := r.backupItem(context.Background(), 0, item, blocked, "blocker/run/src", false, "", "none", 1, nil); err == nil {
		t.Fatal("upload under a file should fail")
	}
}

func TestBackupItemChunkedBranchesC458(t *testing.T) {
	_, d, storageDir := setupTestRunner(t)
	r := runnerWithKey(d, testServerKey())
	dest := makeDedupDest(t, d, storageDir)
	h, err := engine.NewFolderHandler()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha beta gamma"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := engine.BackupItem{Name: "src", Type: "folder", Settings: map[string]any{"path": src}}

	// Bogus destination type.
	if _, _, err := r.backupItemChunked(ctx, 0, item, db.StorageDestination{Type: "bogus"}, nil, h, false); err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("bogus adapter: %v", err)
	}

	// First backup.
	res, _, err := r.backupItemChunked(ctx, 0, item, dest, nil, h, true)
	if err != nil {
		t.Fatalf("first chunked backup: %v", err)
	}
	mid, _ := res.Meta["manifest_id"].([]byte)
	if len(mid) != 32 {
		t.Fatalf("manifest id = %v", res.Meta["manifest_id"])
	}

	// Second backup with the first as parent: nothing changed.
	parent := &db.RestorePoint{ManifestID: mid}
	res2, _, err := r.backupItemChunked(ctx, 0, item, dest, parent, h, false)
	if err != nil {
		t.Fatalf("second chunked backup: %v", err)
	}
	if v, _ := res2.Meta[engine.MetaUnchanged].(bool); !v {
		t.Errorf("unchanged item not flagged: %+v", res2.Meta)
	}

	// A parent pointing at a missing manifest degrades to a full capture,
	// and a folder without a path then fails inside the handler.
	ghost := &db.RestorePoint{ManifestID: make([]byte, 32)}
	bad := engine.BackupItem{Name: "src", Type: "folder", Settings: map[string]any{}}
	if _, _, err := r.backupItemChunked(ctx, 0, bad, dest, ghost, h, false); err == nil || !strings.Contains(err.Error(), "backup chunked") {
		t.Fatalf("handler failure: %v", err)
	}

	// Corrupt repo header.
	if err := os.WriteFile(filepath.Join(storageDir, "_vault", "repo.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.backupItemChunked(ctx, 0, item, dest, nil, h, false); err == nil || !strings.Contains(err.Error(), "open dedup repo") {
		t.Fatalf("corrupt repo: %v", err)
	}
}

func TestWriteManifestFailuresC458(t *testing.T) {
	_, d, storageDir := setupTestRunner(t)
	r := runnerWithKey(d, testServerKey())
	ctx := context.Background()
	job := db.Job{Name: "c458-manifest", Encryption: "age"}

	// Invalid raw settings JSON: marshalling fails before any I/O.
	r.writeManifest(ctx, createLocalDest(t, d, storageDir), "m1", job,
		[]db.JobItem{{ItemName: "x", ItemType: "folder", Settings: "{not json"}}, 1, "full", 1, 0, 1, nil, nil, "ts", "")
	if _, err := os.Stat(filepath.Join(storageDir, "m1", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest should not be written: %v", err)
	}

	// Unknown storage type.
	r.writeManifest(ctx, db.StorageDestination{Type: "bogus"}, "m2", job, nil, 1, "full", 1, 0, 1, nil, nil, "ts", "")

	// Age-encrypted manifest written successfully.
	local := createNamedLocalDest(t, d, "c458-local-2", storageDir)
	r.writeManifest(ctx, local, "m3", job, nil, 1, "full", 1, 0, 1,
		map[string]map[string]string{"x": {"a": "b"}}, map[string]string{"x": "00"}, "ts", "pw")
	b, err := os.ReadFile(filepath.Join(storageDir, "m3", "manifest.json"))
	if err != nil || !strings.Contains(string(b), "age") {
		t.Fatalf("age manifest: %q %v", b, err)
	}

	// Write failure beneath a regular file.
	blocked := c458BlockedLocalDest(t, d)
	r.writeManifest(ctx, blocked, "blocker/m4", job, nil, 1, "full", 1, 0, 1, nil, nil, "ts", "")

	// Dedup destination with a corrupt repo header.
	dedupDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dedupDir, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dedupDir, "_vault", "repo.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	dd := makeDedupDest(t, d, dedupDir)
	r.writeManifest(ctx, dd, "m5", job, nil, 1, "full", 1, 0, 1, nil, nil, "ts", "")
	if _, err := os.Stat(filepath.Join(dedupDir, "m5", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("dedup manifest should not be written with a corrupt repo: %v", err)
	}
}
