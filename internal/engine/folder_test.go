package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

func TestFolderHandlerListItems(t *testing.T) {
	t.Parallel()
	h, err := NewFolderHandler()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	items, err := h.ListItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// On macOS test hosts /boot does not exist, so items may be empty.
	// Just verify the slice is non-nil and any present item has the right shape.
	if items == nil {
		t.Error("expected non-nil items slice")
	}
	for _, it := range items {
		if it.Type != "folder" {
			t.Errorf("expected type=folder, got %q", it.Type)
		}
	}
}

func TestFolderHandlerBackupRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	subdir := filepath.Join(src, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "b.txt"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &FolderHandler{}
	dest := t.TempDir()
	progress := func(string, int, string) {}
	item := BackupItem{
		Name:        "test-folder",
		Type:        "folder",
		Settings:    map[string]any{"path": src},
		Compression: CompressionGzip,
	}

	res, err := h.Backup(context.Background(), item, dest, progress)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !res.Success {
		t.Error("expected success")
	}
	if _, err := os.Stat(filepath.Join(dest, "data.tar.gz")); err != nil {
		t.Errorf("archive not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "folder_meta.json")); err != nil {
		t.Errorf("metadata not written: %v", err)
	}

	// Restore to a new location using restore_destination override.
	restoreDest := filepath.Join(t.TempDir(), "restored")
	restoreItem := BackupItem{
		Name: "test-folder",
		Type: "folder",
		Settings: map[string]any{
			"restore_destination": restoreDest,
		},
	}
	if err := h.Restore(context.Background(), restoreItem, dest, progress); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(restoreDest, "a.txt")); err != nil {
		t.Errorf("restore missing a.txt: %v", err)
	} else if string(data) != "hello" {
		t.Errorf("a.txt content mismatch: %q", data)
	}
}

func TestFolderHandlerBackupNoPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	_, err := h.Backup(context.Background(), BackupItem{Name: "x", Settings: map[string]any{}}, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestFolderHandlerBackupBadPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	item := BackupItem{Name: "x", Settings: map[string]any{"path": "/nonexistent-folder-xyz-abc"}}
	_, err := h.Backup(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing src path")
	}
}

func TestFolderHandlerRestoreNoPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	item := BackupItem{Name: "x", Settings: map[string]any{}}
	err := h.Restore(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing destination")
	}
}

func TestFolderHandlerRestoreUsesMeta(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &FolderHandler{}
	stage := t.TempDir()
	if _, err := h.Backup(context.Background(),
		BackupItem{Name: "x", Settings: map[string]any{"path": src}},
		stage, func(string, int, string) {}); err != nil {
		t.Fatal(err)
	}

	// Restore with no settings — should fall back to metadata path.
	if err := h.Restore(context.Background(),
		BackupItem{Name: "x", Settings: map[string]any{}}, stage,
		func(string, int, string) {}); err != nil {
		t.Errorf("restore via metadata: %v", err)
	}
}

func TestFolderHandlerRestoreMissingArchive(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	stage := t.TempDir()
	dest := filepath.Join(t.TempDir(), "dest")
	item := BackupItem{Name: "x", Settings: map[string]any{"restore_destination": dest}}
	err := h.Restore(context.Background(), item, stage, func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing archive")
	}
}

// TestFolderChunkedRoundTrip backs up a small file tree (mixed sizes plus an
// empty file plus a nested subdirectory) into a dedup repo, restores it to a
// new directory, and verifies every regular file's bytes match by SHA-256.
// Exercises the happy path of BackupChunked + RestoreChunked end-to-end.
func TestFolderChunkedRoundTrip(t *testing.T) {
	src := t.TempDir()
	must := func(p string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(src, "a.txt"), []byte("hello world"))
	must(filepath.Join(src, "sub/b.bin"), bytes.Repeat([]byte{0x42}, 50_000))
	must(filepath.Join(src, "sub/c.empty"), nil)

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{Name: "test", Type: "folder", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := h.RestoreChunked(ctx, item, r, manifestID, dst, nil); err != nil {
		t.Fatal(err)
	}

	// Compare every file's SHA-256.
	errs := 0
	_ = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		srcBody, _ := os.ReadFile(p) // #nosec G304 — test-controlled tempdir
		dstBody, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("missing restored file %s: %v", rel, err)
			errs++
			return nil
		}
		if sha256.Sum256(srcBody) != sha256.Sum256(dstBody) {
			t.Errorf("file %s SHA-256 mismatch", rel)
			errs++
		}
		return nil
	})
	if errs > 0 {
		t.Fatalf("%d file mismatches", errs)
	}
}

// TestFolderChunkedDedupSkipsRepeats verifies that running BackupChunked
// twice against an unchanged source tree does not add any new chunks to the
// repository (content-defined dedup is the whole point of the feature).
func TestFolderChunkedDedupSkipsRepeats(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.bin"), bytes.Repeat([]byte{0xab}, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &FolderHandler{}
	item := BackupItem{Name: "test", Type: "folder", Settings: map[string]any{"path": src}}
	if _, err := h.BackupChunked(context.Background(), item, r, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	after1 := r.Stats().TotalChunks
	if _, err := h.BackupChunked(context.Background(), item, r, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	after2 := r.Stats().TotalChunks
	if after2 != after1 {
		t.Fatalf("second backup added chunks: %d → %d", after1, after2)
	}
}

// TestFolderChunkedSkipsNonRegularFiles confirms that BackupChunked silently
// skips symlinks (and by extension sockets / fifos) without aborting the run.
// Skipped on filesystems that disallow symlink creation (rare in tests).
func TestFolderChunkedSkipsNonRegularFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skip("symlink unsupported in test fs")
	}
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &FolderHandler{}
	item := BackupItem{Name: "test", Type: "folder", Settings: map[string]any{"path": src}}
	mID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := r.GetManifest(mID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files["real.txt"]; !ok {
		t.Error("manifest missing real.txt")
	}
	if _, ok := m.Files["link.txt"]; ok {
		t.Error("manifest unexpectedly includes symlink")
	}
}

// TestFolderChunkedHonoursExclusions verifies BackupChunked skips files and
// whole directories matching exclude_paths, mirroring the classic tar path
// (tarDirectoryFiltered). Without this, dedup folder and container-volume
// backups walk content the user explicitly excluded — e.g. a container that
// bind-mounts the host root at /rootfs would have its entire filesystem
// chunked despite a /rootfs exclusion.
func TestFolderChunkedHonoursExclusions(t *testing.T) {
	src := t.TempDir()
	must := func(p string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(src, "keep.txt"), []byte("keep me"))
	must(filepath.Join(src, "app.log"), []byte("noisy log"))          // glob excluded
	must(filepath.Join(src, "logs/debug.txt"), []byte("dir content")) // dir excluded
	must(filepath.Join(src, "data/blob.bin"), []byte("payload"))      // kept

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{
		Name: "test",
		Type: "folder",
		Settings: map[string]any{
			"path":          src,
			"exclude_paths": []string{"*.log", "logs"},
		},
	}
	manifestID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := r.GetManifest(manifestID)
	if err != nil {
		t.Fatal(err)
	}

	for _, excluded := range []string{"app.log", "logs", "logs/debug.txt"} {
		if _, ok := m.Files[excluded]; ok {
			t.Errorf("manifest unexpectedly contains excluded entry %q", excluded)
		}
	}
	for _, included := range []string{"keep.txt", "data/blob.bin"} {
		if _, ok := m.Files[included]; !ok {
			t.Errorf("manifest missing expected entry %q (have %v)", included, manifestKeys(m))
		}
	}
}
