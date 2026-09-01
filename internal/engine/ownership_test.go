package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// modeOf returns a path's permission bits, failing the test if it cannot be
// stat'd — every assertion below is about a path the restore was supposed to
// have created.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// A restore has to reproduce the permissions the backup captured, not the
// permissions the extractor happens to create with. Modes are asserted rather
// than ownership because the test runs unprivileged: chown to another user is
// not permitted, and applyOwner is deliberately best-effort about that.
func TestUntarDirectoryRestoresModes(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "private/secret.conf"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Written 0700/0600/0755 above, but WriteFile/MkdirAll are subject to the
	// process umask — chmod explicitly so the archive records exactly these.
	for path, mode := range map[string]os.FileMode{
		"private":             0o700,
		"private/secret.conf": 0o600,
		"run.sh":              0o755,
	} {
		if err := os.Chmod(filepath.Join(src, path), mode); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(t.TempDir(), "vol.tar")
	if err := tarDirectory(context.Background(), src, archive, nil, "none"); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := untarDirectory(context.Background(), archive, dst); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]os.FileMode{
		"private":             0o700,
		"private/secret.conf": 0o600,
		"run.sh":              0o755,
	} {
		if got := modeOf(t, filepath.Join(dst, path)); got != want {
			t.Errorf("%s: mode = %04o, want %04o", path, got, want)
		}
	}
}

// Restoring over an existing tree must correct modes too: O_CREATE and
// MkdirAll both leave an existing path's permissions alone, so without an
// explicit chmod the restored file keeps whatever it had before.
func TestUntarDirectoryCorrectsExistingModes(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "conf/app.ini"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "conf/app.ini"), 0o640); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "vol.tar")
	if err := tarDirectory(context.Background(), src, archive, nil, "none"); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dst, "conf"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "conf/app.ini"), []byte("stale"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dst, "conf"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dst, "conf/app.ini"), 0o666); err != nil {
		t.Fatal(err)
	}

	if err := untarDirectory(context.Background(), archive, dst); err != nil {
		t.Fatal(err)
	}

	if got := modeOf(t, filepath.Join(dst, "conf")); got != 0o750 {
		t.Errorf("existing dir mode = %04o, want 0750", got)
	}
	if got := modeOf(t, filepath.Join(dst, "conf/app.ini")); got != 0o640 {
		t.Errorf("existing file mode = %04o, want 0640", got)
	}
}

// A directory whose mode denies writes must still receive its children: the
// mode is applied only once the extraction beneath it is finished.
func TestUntarDirectoryReadOnlyDirStillGetsChildren(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "ro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ro/data.txt"), []byte("payload"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "vol.tar")
	if err := tarDirectory(context.Background(), src, archive, nil, "none"); err != nil {
		t.Fatal(err)
	}
	// Leave the source writable again so t.TempDir cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "ro"), 0o755) })

	dst := t.TempDir()
	if err := untarDirectory(context.Background(), archive, dst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dst, "ro"), 0o755) })

	body, err := os.ReadFile(filepath.Join(dst, "ro/data.txt"))
	if err != nil {
		t.Fatalf("child of read-only dir was not restored: %v", err)
	}
	if string(body) != "payload" {
		t.Errorf("content = %q, want %q", body, "payload")
	}
	if got := modeOf(t, filepath.Join(dst, "ro")); got != 0o555 {
		t.Errorf("dir mode = %04o, want 0555", got)
	}
}

// The dedup path has to match the classic one: same backup, same restored
// permissions, whichever destination type the job uses.
func TestFolderChunkedRestoreRestoresModes(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "private/secret.conf"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "private/secret.conf"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{Name: "conf", Type: "folder", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := h.RestoreChunked(ctx, item, repo, manifestID, dst, nil); err != nil {
		t.Fatal(err)
	}

	if got := modeOf(t, filepath.Join(dst, "private")); got != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", got)
	}
	if got := modeOf(t, filepath.Join(dst, "private/secret.conf")); got != 0o600 {
		t.Errorf("file mode = %04o, want 0600", got)
	}
}

// The manifest records the owner it saw, and a manifest written before
// ownership existed reports "unknown" rather than root — restoring the latter
// as 0:0 would silently hand every file to root.
func TestManifestEntryOwner(t *testing.T) {
	var absent dedup.ManifestEntry
	if uid, gid := absent.Owner(); uid != -1 || gid != -1 {
		t.Errorf("absent owner = %d:%d, want -1:-1", uid, gid)
	}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	var entry dedup.ManifestEntry
	setManifestOwner(&entry, info)
	wantUID, wantGID := fileOwner(info)
	if wantUID < 0 {
		t.Skip("platform records no numeric owner")
	}
	if uid, gid := entry.Owner(); uid != wantUID || gid != wantGID {
		t.Errorf("owner = %d:%d, want %d:%d", uid, gid, wantUID, wantGID)
	}

	// Round-trips through the stored manifest, not just in memory.
	blob, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var decoded dedup.ManifestEntry
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if uid, gid := decoded.Owner(); uid != wantUID || gid != wantGID {
		t.Errorf("decoded owner = %d:%d, want %d:%d", uid, gid, wantUID, wantGID)
	}
}

// The volume's own root directory is described by no archive entry, so the
// pointer entry in the container manifest is the only record of it.
func TestVolumePointerEntryRecordsRoot(t *testing.T) {
	src := t.TempDir()
	if err := os.Chmod(src, 0o770); err != nil {
		t.Fatal(err)
	}
	entry := volumePointerEntry(src, dedup.ID{0x01})
	if entry.Mode != 0o770 {
		t.Errorf("mode = %04o, want 0770", entry.Mode)
	}
	if len(entry.Chunks) != 1 || entry.Chunks[0] != (dedup.ID{0x01}) {
		t.Errorf("chunks = %v, want the sub-manifest id", entry.Chunks)
	}
	if entry.Size != 0 {
		t.Errorf("size = %d, want 0 (a pointer holds no bytes of its own)", entry.Size)
	}

	// An unreadable source must not lose the pointer — the volume's contents
	// are still restorable, only its root metadata is unknown.
	missing := volumePointerEntry(filepath.Join(src, "nope"), dedup.ID{0x02})
	if missing.Mode != 0 {
		t.Errorf("mode = %04o, want 0 for an unstat-able source", missing.Mode)
	}
	if len(missing.Chunks) != 1 {
		t.Errorf("chunks = %v, want the sub-manifest id preserved", missing.Chunks)
	}
}

// applyVolumeRootMeta is what puts the mount root back; a backup that recorded
// no mode must leave the directory exactly as the restore made it.
func TestApplyVolumeRootMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	applyVolumeRootMeta(dir, 0o2775, -1, -1)
	if got := modeOf(t, dir); got != 0o775 {
		t.Errorf("mode = %04o, want 0775 (permission bits only)", got)
	}

	applyVolumeRootMeta(dir, 0, -1, -1)
	if got := modeOf(t, dir); got != 0o775 {
		t.Errorf("mode = %04o after an unrecorded root, want it untouched at 0775", got)
	}
}

// A backup taken before root metadata existed decodes as zeroes, and the
// restore must read that as "not recorded" rather than as mode 0.
func TestVolumeManifestEntryRootDefaults(t *testing.T) {
	var entry volumeManifestEntry
	if err := json.Unmarshal([]byte(`{"index":0,"source":"/mnt/appdata/x","destination":"/config","backed_up":true}`), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.RootMode != 0 {
		t.Errorf("root_mode = %04o, want 0 for a legacy manifest", entry.RootMode)
	}
}

// The dedup path must survive a read-only directory for the same reason the
// classic one does — the files inside it are written after the directory
// exists, so its recorded mode cannot be applied up front.
func TestFolderChunkedRestoreReadOnlyDir(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "ro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ro/data.txt"), []byte("payload"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "ro"), 0o755) })

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{Name: "ro", Type: "folder", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := h.RestoreChunked(ctx, item, repo, manifestID, dst, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dst, "ro"), 0o755) })

	body, err := os.ReadFile(filepath.Join(dst, "ro/data.txt"))
	if err != nil {
		t.Fatalf("child of read-only dir was not restored: %v", err)
	}
	if string(body) != "payload" {
		t.Errorf("content = %q, want %q", body, "payload")
	}
	if got := modeOf(t, filepath.Join(dst, "ro")); got != 0o555 {
		t.Errorf("dir mode = %04o, want 0555", got)
	}
}

// Every metadata call resolves its path against the restore root before it
// gets here, so a relative or traversing path means something upstream is
// broken: the safest response is to do nothing rather than chmod or chown a
// path outside the restore.
func TestRestorePathSafe(t *testing.T) {
	cases := map[string]bool{
		"/mnt/user/appdata/x":      true,
		"/mnt/user/backups..2026":  true, // a legitimate name, not traversal
		"/mnt/user/appdata/../etc": false,
		"relative/path":            false,
		"":                         false,
		"..":                       false,
	}
	for path, want := range cases {
		if got := restorePathSafe(path); got != want {
			t.Errorf("restorePathSafe(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestApplyMetadataRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o600); err != nil {
		t.Fatal(err)
	}

	// Built by concatenation, not filepath.Join, which would clean the ".."
	// away and defeat the point of the test.
	unsafe := dir + "/sub/../keep.txt"
	applyMode(unsafe, 0o777)
	if got := modeOf(t, victim); got != 0o600 {
		t.Errorf("mode = %04o, want the untouched 0600", got)
	}
	applyOwner(unsafe, 0, 0)

	if err := mkdirRestored(dir+"/sub/../made", 0o755); err == nil {
		t.Error("mkdirRestored() accepted a traversing path, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err == nil {
		t.Error("mkdirRestored() created a directory from a traversing path")
	}
}

// mkdirRestored always leaves the daemon able to write inside what it creates,
// whatever mode the backup recorded — the entries beneath it come next.
func TestMkdirRestoredForcesOwnerWriteBit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := mkdirRestored(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o755 {
		t.Errorf("mode = %04o, want 0755 while the restore is still writing", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("x"), 0o644); err != nil {
		t.Errorf("could not write inside a freshly restored directory: %v", err)
	}
}

// An owner the backup never recorded must leave the path alone rather than
// hand it to root.
func TestApplyOwnerUnknownIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	applyOwner(path, -1, -1)
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeUID, beforeGID := fileOwner(before)
	afterUID, afterGID := fileOwner(after)
	if beforeUID != afterUID || beforeGID != afterGID {
		t.Errorf("owner changed from %d:%d to %d:%d", beforeUID, beforeGID, afterUID, afterGID)
	}
}
