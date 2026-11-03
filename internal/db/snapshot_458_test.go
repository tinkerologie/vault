package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSnapshotTestManager(t *testing.T) (*SnapshotManager, string) {
	t.Helper()
	d := setupTestDB(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "vault.db")
	return NewSnapshotManager(d, snap, snap), dir
}

// nonEmptyDir creates a directory at p holding one file, so renaming a file
// onto p or removing p fails on every platform.
func nonEmptyDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSnapshotPath_458(t *testing.T) {
	if _, err := validateSnapshotPath(""); err == nil {
		t.Error("empty path should fail")
	}
	if _, err := validateSnapshotPath("/a/../b"); err == nil {
		t.Error("traversal should fail")
	}
	got, err := validateSnapshotPath("/tmp/backups..2026/vault.db")
	if err != nil || got != filepath.Clean("/tmp/backups..2026/vault.db") {
		t.Errorf("legit dotted name = %q, %v", got, err)
	}
}

func TestRotateSnapshotBranches_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)

	// Empty path: no-op.
	sm.snapshotPath = ""
	sm.rotateSnapshot()

	// Traversal path: logged and ignored.
	sm.snapshotPath = "/a/../b/vault.db"
	sm.rotateSnapshot()

	// Missing source: nothing rotated.
	sm.snapshotPath = filepath.Join(dir, "vault.db")
	sm.rotateSnapshot()
	if _, err := os.Stat(filepath.Join(dir, "rotated")); !os.IsNotExist(err) {
		t.Fatalf("rotated dir should not exist when source missing: %v", err)
	}

	// mkdir failure: a file occupies the rotated dir name.
	if err := os.WriteFile(sm.snapshotPath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rotated"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm.rotateSnapshot()
	if err := os.Remove(filepath.Join(dir, "rotated")); err != nil {
		t.Fatal(err)
	}

	// Success, then pruning keeps only rotatedSnapshotsKept entries.
	rotated := filepath.Join(dir, "rotated")
	if err := os.MkdirAll(rotated, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := range rotatedSnapshotsKept + 3 {
		name := fmt.Sprintf("vault.db.2000-01-01T00-00-%02d.000000000", i)
		if err := os.WriteFile(filepath.Join(rotated, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sm.rotateSnapshot()
	entries, err := os.ReadDir(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != rotatedSnapshotsKept {
		t.Fatalf("rotated entries = %d, want %d", len(entries), rotatedSnapshotsKept)
	}
	for _, e := range entries {
		if e.Name() == "vault.db.2000-01-01T00-00-00.000000000" {
			t.Fatal("oldest rotated copy should have been pruned")
		}
	}
}

func TestPruneRotatedBranches_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)
	// ReadDir failure is logged, not fatal.
	sm.pruneRotated(filepath.Join(dir, "missing"))

	// Undeletable entry (a non-empty directory) is logged and skipped.
	rotated := filepath.Join(dir, "rotated")
	nonEmptyDir(t, filepath.Join(rotated, "a-oldest"))
	for i := range rotatedSnapshotsKept {
		if err := os.WriteFile(filepath.Join(rotated, fmt.Sprintf("b-%02d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sm.pruneRotated(rotated)
	if _, err := os.Stat(filepath.Join(rotated, "a-oldest")); err != nil {
		t.Fatalf("non-empty dir should survive prune: %v", err)
	}
}

func TestBackupWorkingDBToPathErrors_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)

	if err := sm.backupWorkingDBToPath("/x/../y.db"); err == nil {
		t.Error("traversal dest should fail")
	}
	// Backup into a missing directory fails in NewBackup / Step.
	if err := sm.backupWorkingDBToPath(filepath.Join(dir, "nope", "sub", "v.db")); err == nil {
		t.Error("missing parent dir should fail")
	}
	// Rename onto a non-empty directory fails after a successful backup.
	dest := filepath.Join(dir, "occupied")
	nonEmptyDir(t, dest)
	err := sm.backupWorkingDBToPath(dest)
	if err == nil || !strings.Contains(err.Error(), "activating snapshot") {
		t.Errorf("rename failure = %v", err)
	}
	if _, statErr := os.Stat(dest + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("temp file should be cleaned up: %v", statErr)
	}

	// Closed DB: acquiring a connection fails.
	if err := sm.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sm.backupWorkingDBToPath(filepath.Join(dir, "after-close.db")); err == nil {
		t.Error("closed DB should fail")
	}
}

func TestCopySnapshotArtifactErrors_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)
	src := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(src, []byte("snapshot-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sm.copySnapshotArtifact("/a/../b", filepath.Join(dir, "d")); err == nil {
		t.Error("bad source should fail")
	}
	if err := sm.copySnapshotArtifact(src, "/a/../b"); err == nil {
		t.Error("bad dest should fail")
	}
	if err := sm.copySnapshotArtifact(filepath.Join(dir, "missing.db"), filepath.Join(dir, "d")); err == nil {
		t.Error("missing source should fail")
	}
	if err := sm.copySnapshotArtifact(src, filepath.Join(dir, "nodir", "d")); err == nil {
		t.Error("missing dest dir should fail")
	}
	occupied := filepath.Join(dir, "occupied")
	nonEmptyDir(t, occupied)
	if err := sm.copySnapshotArtifact(src, occupied); err == nil || !strings.Contains(err.Error(), "activating USB copy") {
		t.Errorf("rename failure = %v", err)
	}

	// Happy path copies bytes.
	dst := filepath.Join(dir, "usb.db")
	if err := sm.copySnapshotArtifact(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "snapshot-bytes" {
		t.Fatalf("copied = %q, %v", b, err)
	}
}

func TestRetireSnapshotArtifactsBranches_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)
	newPath := filepath.Join(dir, "new", "vault.db")

	if m := sm.retireSnapshotArtifacts("", newPath); m != nil {
		t.Errorf("empty old = %+v", m)
	}
	if m := sm.retireSnapshotArtifacts(newPath, newPath); m != nil {
		t.Errorf("same path = %+v", m)
	}
	m := sm.retireSnapshotArtifacts("/a/../vault.db", newPath)
	if m == nil || m.Warning == "" || m.Completed {
		t.Errorf("invalid old = %+v", m)
	}
	// Equal after cleaning.
	if m := sm.retireSnapshotArtifacts(filepath.Join(dir, "new", ".", "vault.db"), newPath); m != nil {
		t.Errorf("cleaned-equal = %+v", m)
	}

	// New snapshot missing: old left in place.
	oldDir := filepath.Join(dir, ".vault")
	oldPath := filepath.Join(oldDir, "vault.db")
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = sm.retireSnapshotArtifacts(oldPath, newPath)
	if m == nil || m.Completed || !strings.Contains(m.Warning, "could not be verified") {
		t.Errorf("unverified new = %+v", m)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old must survive: %v", err)
	}

	// Removal failure: old .tmp is a non-empty directory, and a rotated
	// entry is a non-empty directory with a matching prefix (skipped as a
	// dir) alongside a removable rotated copy and a foreign file.
	if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonEmptyDir(t, oldPath+".tmp")
	rot := filepath.Join(oldDir, "rotated")
	nonEmptyDir(t, filepath.Join(rot, "vault.db.dir"))
	if err := os.WriteFile(filepath.Join(rot, "vault.db.2000"), []byte("r"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rot, "foreign.txt"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = sm.retireSnapshotArtifacts(oldPath, newPath)
	if m == nil || m.Completed || m.FilesRetired != 2 || !strings.Contains(m.Warning, "could not be removed") {
		t.Errorf("partial retire = %+v", m)
	}
	if _, err := os.Stat(filepath.Join(rot, "foreign.txt")); err != nil {
		t.Errorf("foreign file must be preserved: %v", err)
	}
}

func TestRestoreFromSnapshotAndPathErrors_458(t *testing.T) {
	sm, dir := newSnapshotTestManager(t)

	sm.snapshotPath = "/a/../b.db"
	if err := sm.RestoreFromSnapshot(); err == nil {
		t.Error("bad snapshot path should fail")
	}

	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not a sqlite db ", 512)), 0o600); err != nil {
		t.Fatal(err)
	}
	sm.snapshotPath = garbage
	if err := sm.RestoreFromSnapshot(); err == nil {
		t.Error("garbage snapshot should fail to restore")
	}
	if err := sm.RestoreFromPath(garbage); err == nil {
		t.Error("garbage source should fail to restore")
	}

	if err := sm.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sm.RestoreFromSnapshot(); err == nil {
		t.Error("closed DB should fail RestoreFromSnapshot")
	}
	if err := sm.RestoreFromPath(garbage); err == nil {
		t.Error("closed DB should fail RestoreFromPath")
	}
}

// Issue #543: a snapshot path reached through a symlink must not be restored
// from or written to.
func TestSnapshotRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "working.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	real := filepath.Join(dir, "real.db")
	sm := NewSnapshotManager(database, real, real)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := sm.RestoreFromPath(real); err != nil {
		t.Fatalf("regular snapshot refused: %v", err)
	}
	if err := sm.RestoreFromSnapshot(); err != nil {
		t.Fatalf("regular snapshot refused: %v", err)
	}

	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := sm.RestoreFromPath(link); err == nil {
		t.Error("RestoreFromPath followed a symlink")
	}
	linked := NewSnapshotManager(database, link, real)
	if err := linked.RestoreFromSnapshot(); err == nil {
		t.Error("RestoreFromSnapshot followed a symlink")
	}
	if err := linked.SaveSnapshot(); err == nil {
		t.Error("SaveSnapshot wrote through a symlink")
	}

	etc := filepath.Join(dir, "etc")
	if err := os.Symlink("/etc", etc); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.SetSnapshotPath(filepath.Join(etc, "vault.db")); err == nil {
		t.Error("SetSnapshotPath accepted a symlink out of the approved roots")
	}
	if err := NewSnapshotManager(database, "/etc/vault.db", real).SaveSnapshot(); err == nil {
		t.Error("SaveSnapshot accepted a system path")
	}
	if err := sm.RestoreFromPath("/etc/hosts"); err == nil {
		t.Error("RestoreFromPath accepted a system path")
	}
}

func TestCopySnapshotPrivately(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	copied, remove, err := copySnapshotPrivately(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(copied)
	if err != nil || string(got) != "bytes" {
		t.Fatalf("copy = %q, %v", got, err)
	}
	info, err := os.Stat(copied)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("copy mode = %v, %v", info, err)
	}
	remove()
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Fatalf("copy not removed: %v", err)
	}
	for name, bad := range map[string]string{
		"system path": "/etc/hosts",
		"directory":   dir,
		"missing":     filepath.Join(dir, "missing.db"),
		"no parent":   filepath.Join(dir, "gone", "x.db"),
	} {
		if _, _, err := copySnapshotPrivately(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Committed changes still in the -wal sidecar must survive a restore.
func TestRestoreFromPathKeepsWALChanges(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	source, err := Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if err := source.SetSetting("wal_marker", "committed"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(srcPath + "-wal"); err != nil || info.Size() == 0 {
		t.Skipf("no WAL content to test with: %v", err)
	}
	target, err := Open(filepath.Join(dir, "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	if err := NewSnapshotManager(target, srcPath, srcPath).RestoreFromPath(srcPath); err != nil {
		t.Fatal(err)
	}
	if got, _ := target.GetSetting("wal_marker", ""); got != "committed" {
		t.Fatalf("wal_marker = %q; the WAL was dropped", got)
	}
}
