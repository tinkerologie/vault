package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestFolderHandlerBackupDifferentialIncludesNewFileWithStaleMtime is the
// classic-path regression test for issue #320: after a full backup, a NEW file
// added with a stale mtime (e.g. copied in with timestamps preserved) must be
// present in the differential archive. The runner passes the previous backup's
// effective-listing paths via "prev_listing_paths"; the engine archives files
// absent from that set regardless of mtime, while unchanged files stay skipped.
func TestFolderHandlerBackupDifferentialIncludesNewFileWithStaleMtime(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour)
	stale := time.Now().Add(-3 * time.Hour)

	src := t.TempDir()
	write := func(name, content string, mtime time.Time) {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if !mtime.IsZero() {
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("old.txt", "old", stale)

	h := &FolderHandler{}
	progress := func(string, int, string) {}

	// Full backup: only old.txt present.
	fullDest := t.TempDir()
	if _, err := h.Backup(context.Background(), BackupItem{
		Name: "test-folder", Type: "folder", Settings: map[string]any{"path": src},
	}, fullDest, progress); err != nil {
		t.Fatalf("full Backup: %v", err)
	}

	// Add a NEW file with a stale mtime after the full backup.
	write("added.txt", "added", stale)

	// Differential backup: changed_since plus the previous listing paths.
	diffDest := t.TempDir()
	if _, err := h.Backup(context.Background(), BackupItem{
		Name: "test-folder",
		Type: "folder",
		Settings: map[string]any{
			"path":               src,
			"changed_since":      changedSince.Format(time.RFC3339),
			"prev_listing_paths": []string{"old.txt"},
		},
	}, diffDest, progress); err != nil {
		t.Fatalf("differential Backup: %v", err)
	}

	archive, err := findArchive(diffDest, "data.tar")
	if err != nil {
		t.Fatalf("findArchive: %v", err)
	}
	extract := t.TempDir()
	if err := untarDirectory(context.Background(), archive, extract); err != nil {
		t.Fatalf("untar: %v", err)
	}

	// The NEW file must be archived (the fix) …
	if _, err := os.Stat(filepath.Join(extract, "added.txt")); err != nil {
		t.Errorf("differential archive missing added.txt (new file with stale mtime): %v", err)
	}
	// … while the unchanged file is still skipped (present in the full backup).
	if _, err := os.Stat(filepath.Join(extract, "old.txt")); err == nil {
		t.Errorf("differential archive should skip unchanged old.txt")
	}
}

// TestFolderHandlerBackupHonoursExclusions verifies the classic tar Backup
// path (used for flash/folder backups on non-dedup destinations) applies
// exclude_paths — previously it passed nil and ignored them (issue #204).
func TestFolderHandlerBackupHonoursExclusions(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	must := func(rel string, data []byte) {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("keep.txt", []byte("keep"))
	must("app.log", []byte("noisy"))            // glob excluded
	must(".Recycle.Bin/trash.txt", []byte("x")) // dir excluded

	h := &FolderHandler{}

	// backupRestore runs a folder backup with the base exclusions plus any
	// extra settings, restores it, and returns the restore destination.
	backupRestore := func(t *testing.T, extra map[string]any) string {
		settings := map[string]any{
			"path":          src,
			"exclude_paths": []string{"*.log", ".Recycle.Bin"},
		}
		for k, v := range extra {
			settings[k] = v
		}
		dest := t.TempDir()
		item := BackupItem{Name: "flash", Type: "folder", Compression: CompressionGzip, Settings: settings}
		if _, err := h.Backup(context.Background(), item, dest, func(string, int, string) {}); err != nil {
			t.Fatalf("backup: %v", err)
		}
		restoreDest := filepath.Join(t.TempDir(), "restored")
		if err := h.Restore(context.Background(),
			BackupItem{Name: "flash", Type: "folder", Settings: map[string]any{"restore_destination": restoreDest}},
			dest, func(string, int, string) {}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		return restoreDest
	}
	assertExcluded := func(t *testing.T, restoreDest string) {
		if _, err := os.Stat(filepath.Join(restoreDest, "keep.txt")); err != nil {
			t.Errorf("expected keep.txt to be backed up: %v", err)
		}
		for _, excluded := range []string{"app.log", ".Recycle.Bin/trash.txt"} {
			if _, err := os.Stat(filepath.Join(restoreDest, excluded)); err == nil {
				t.Errorf("excluded path %q was archived", excluded)
			}
		}
	}

	// full exercises tarDirectory; incremental exercises tarDirectoryFiltered
	// (a changed_since far in the past means everything qualifies by mtime, so
	// exclusions must remain the only thing dropped).
	tests := []struct {
		name  string
		extra map[string]any
	}{
		{name: "full", extra: nil},
		{name: "incremental", extra: map[string]any{"changed_since": "2000-01-01T00:00:00Z"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertExcluded(t, backupRestore(t, tc.extra))
		})
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
	manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
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

// TestFolderChunkedRestoresReservedName guards the plugin-installer refactor:
// the installer lives in Manifest.Installer, not in Files, so a folder that
// legitimately contains a file literally named "__plg" must still round-trip
// through BackupChunked + RestoreChunked without being dropped.
func TestFolderChunkedRestoresReservedName(t *testing.T) {
	src := t.TempDir()
	body := []byte("not an installer, just a user file")
	if err := os.WriteFile(filepath.Join(src, "__plg"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{Name: "reserved", Type: "folder", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
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
	if got, err := os.ReadFile(filepath.Join(dst, "__plg")); err != nil { // #nosec G304 — test-controlled tempdir
		t.Errorf("a real file named __plg must be restored, not dropped: %v", err)
	} else if !bytes.Equal(got, body) {
		t.Errorf("restored __plg content mismatch: got %q", got)
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
	if _, err := h.BackupChunked(context.Background(), item, r, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	after1 := r.Stats().TotalChunks
	if _, err := h.BackupChunked(context.Background(), item, r, nil, nil); err != nil {
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
	mID, err := h.BackupChunked(context.Background(), item, r, nil, nil)
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
	manifestID, err := h.BackupChunked(context.Background(), item, r, nil, nil)
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

// TestFolderBackupFollowsSymlinkSource verifies that folder backups (both
// classic tar and dedup/chunked) traverse into a directory that is a symlink.
// This covers Unraid cache-only shares where /mnt/user/<share> is a symlink
// to /mnt/cache/<share>. Without EvalSymlinks, filepath.Walk uses os.Lstat
// on the root, sees a symlink (not a directory), and never descends into
// the target — producing an empty archive.
func TestFolderBackupFollowsSymlinkSource(t *testing.T) {
	t.Parallel()

	// Create a real directory with files.
	realDir := t.TempDir()
	mustWrite := func(rel string, data []byte) {
		p := filepath.Join(realDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("file1.txt", []byte("hello"))
	mustWrite("subdir/file2.txt", []byte("world"))

	// Create a symlink pointing to the real directory.
	linkDir := filepath.Join(t.TempDir(), "linked_folder")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("filesystem does not support symlinks")
	}

	h := &FolderHandler{}
	ctx := context.Background()
	progress := func(string, int, string) {}

	// Test classic tar path (Backup).
	t.Run("classic_tar", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		item := BackupItem{
			Name:        "symlink-folder",
			Type:        "folder",
			Settings:    map[string]any{"path": linkDir},
			Compression: CompressionGzip,
		}
		res, err := h.Backup(ctx, item, dest, progress)
		if err != nil {
			t.Fatalf("backup: %v", err)
		}
		if !res.Success {
			t.Fatal("expected success")
		}

		// Verify folder_meta.json stores the original symlink path, not the
		// resolved target.
		metaData, err := os.ReadFile(filepath.Join(dest, "folder_meta.json"))
		if err != nil {
			t.Fatalf("reading folder_meta.json: %v", err)
		}
		if !bytes.Contains(metaData, []byte(linkDir)) {
			t.Errorf("folder_meta.json should contain original path %q, got %s", linkDir, metaData)
		}

		// Verify the archive contains files (not empty).
		restoreDest := t.TempDir()
		if err := h.Restore(ctx, BackupItem{
			Name:     "symlink-folder",
			Type:     "folder",
			Settings: map[string]any{"restore_destination": restoreDest},
		}, dest, progress); err != nil {
			t.Fatalf("restore: %v", err)
		}

		if _, err := os.Stat(filepath.Join(restoreDest, "file1.txt")); err != nil {
			t.Errorf("missing file1.txt after restore: %v", err)
		}
		if _, err := os.Stat(filepath.Join(restoreDest, "subdir/file2.txt")); err != nil {
			t.Errorf("missing subdir/file2.txt after restore: %v", err)
		}
	})

	// Test dedup/chunked path (BackupChunked).
	t.Run("chunked", func(t *testing.T) {
		t.Parallel()
		r, _, cleanup := dedup.NewTestRepoForEngine(t)
		defer cleanup()

		item := BackupItem{
			Name:     "symlink-folder",
			Type:     "folder",
			Settings: map[string]any{"path": linkDir},
		}
		manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
		if err != nil {
			t.Fatalf("chunked backup: %v", err)
		}
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}

		m, err := r.GetManifest(manifestID)
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := m.Files["file1.txt"]; !ok {
			t.Errorf("manifest missing file1.txt")
		}
		if _, ok := m.Files["subdir/file2.txt"]; !ok {
			t.Errorf("manifest missing subdir/file2.txt")
		}
	})
}

// TestFolderBackup_ProgressMilestones: folder backups (incl. the Flash
// Drive preset) narrate their phases so the run log has ample lines (#328 QA).
func TestFolderBackup_ProgressMilestones(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "vault.cfg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()

	var msgs []string
	progress := func(_ string, _ int, msg string) { msgs = append(msgs, msg) }

	h, err := NewFolderHandler()
	if err != nil {
		t.Fatalf("NewFolderHandler: %v", err)
	}
	item := BackupItem{
		Name:        "Flash Drive",
		Type:        "folder",
		Settings:    map[string]any{"path": src, "preset": "flash"},
		Compression: "none",
	}
	if _, err := h.Backup(context.Background(), item, dest, progress); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	joined := strings.Join(msgs, "\n")
	cases := []struct {
		name string
		want string
	}{
		{name: "source resolution milestone narrated", want: "resolved source path"},
		{name: "archiving milestone narrated", want: "archiving"},
		{name: "metadata milestone narrated", want: "writing folder metadata"},
		{name: "completion milestone narrated", want: "backup complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("progress milestones missing %q; got:\n%s", tc.want, joined)
			}
		})
	}
}
