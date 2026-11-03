package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotPathFromCfgRevalidates(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "vault.cfg")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const fallback = "/mnt/cache/.vault/vault.db"
	if got := snapshotPathFromCfg(cfg, fallback); got != fallback {
		t.Fatalf("missing file: %q", got)
	}
	write("SNAPSHOT_PATH=/etc/passwd\n")
	if got := snapshotPathFromCfg(cfg, fallback); got != fallback {
		t.Fatalf("persisted system path used: %q", got)
	}
	write("SNAPSHOT_PATH=/mnt/user/db/vault.db\n")
	if got := snapshotPathFromCfg(cfg, fallback); got != "/mnt/user/db/vault.db" {
		t.Fatalf("valid path ignored: %q", got)
	}
}

func writeSnapshot(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveSnapshotArtifactsRemovesDatabaseAndSidecars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	db := filepath.Join(dir, "vault.db")
	writeSnapshot(t, db, sqliteMagic+"rest")
	writeSnapshot(t, db+"-wal", "x")
	if err := removeSnapshotArtifacts(db, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory not pruned: %v", err)
	}
	if err := removeSnapshotArtifacts("", false); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts(filepath.Join(t.TempDir(), "gone", "vault.db"), false); err != nil {
		t.Fatal(err)
	}
}

// Issue #543: a planted override must not choose what uninstall deletes.
func TestRemoveSnapshotArtifactsKeepsPlantedTargets(t *testing.T) {
	root := t.TempDir()

	notDB := filepath.Join(root, "important.doc")
	writeSnapshot(t, notDB, "user data, long enough to read a header from")
	if err := removeSnapshotArtifacts(notDB, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(notDB); err != nil {
		t.Errorf("non-database file deleted: %v", err)
	}

	tree := filepath.Join(root, "share")
	writeSnapshot(t, filepath.Join(tree, "file"), "x")
	if err := removeSnapshotArtifacts(tree, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree, "file")); err != nil {
		t.Errorf("directory removed recursively: %v", err)
	}

	// Symlinked final component and symlinked parent.
	outside := t.TempDir()
	victim := filepath.Join(outside, "vault.db")
	writeSnapshot(t, victim, sqliteMagic)
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts(link, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("symlink target deleted: %v", err)
	}

	etc := filepath.Join(root, "etclink")
	if err := os.Symlink("/etc", etc); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts(filepath.Join(etc, "hosts"), false); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts("/etc/hosts", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/etc/hosts"); err != nil {
		t.Errorf("/etc/hosts: %v", err)
	}
}

func TestWriteRecoverReportIsExclusive(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := writeRecoverReport(dir, "report.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := writeRecoverReport(dir, "report.json", []byte("{}")); err == nil {
		t.Fatal("existing report overwritten")
	}
	victim := filepath.Join(outside, "victim")
	if err := os.Symlink(victim, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeRecoverReport(dir, "link.json", []byte("{}")); err == nil {
		t.Fatal("wrote through a planted symlink")
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Fatalf("symlink target created: %v", err)
	}
	if err := writeRecoverReport(filepath.Join(dir, "missing"), "r.json", nil); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestRemoveSnapshotArtifactsOrphanSidecars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	db := filepath.Join(dir, "vault.db")
	writeSnapshot(t, db+"-wal", "x")
	if err := removeSnapshotArtifacts(db, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db + "-wal"); err != nil {
		t.Fatalf("override sidecar removed without a database: %v", err)
	}
	if err := removeSnapshotArtifacts(db, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("default-location sidecar kept: %v", err)
	}
}

func TestRemoveSnapshotArtifactsFailures(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts(filepath.Join(loop, "vault.db"), false); err != nil {
		t.Logf("unresolvable directory: %v", err)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	db := filepath.Join(dir, "s", "vault.db")
	writeSnapshot(t, db, sqliteMagic)
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshotArtifacts(db, false); err == nil {
		t.Fatal("unreadable snapshot treated as removable")
	}
	if _, err := os.Lstat(db); err != nil {
		t.Fatalf("unreadable snapshot removed: %v", err)
	}
}
