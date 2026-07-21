package db

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateSnapshotPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid absolute path", "/mnt/cache/.vault/vault.db", false},
		{"valid nested path", "/boot/config/plugins/vault/vault.db", false},
		{"empty path", "", true},
		{"traversal with dotdot", "/mnt/cache/../../etc/passwd", true},
		{"relative path resolves", "relative/path/vault.db", false}, // Abs makes it absolute
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := validateSnapshotPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSnapshotPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
			if err == nil && result == "" {
				t.Error("validateSnapshotPath returned empty string without error")
			}
		})
	}
}

func TestValidateSnapshotPath_RejectsTraversal(t *testing.T) {
	t.Parallel()

	// Paths containing ".." components are rejected before normalisation.
	_, err := validateSnapshotPath("/mnt/cache/../cache/vault.db")
	if err == nil {
		t.Fatal("expected error for path containing '..' component")
	}
}

func TestSetSnapshotPath_RejectsEmptyDefault(t *testing.T) {
	d := setupTestDB(t)
	// Create a manager with an empty default path — SetSnapshotPath("") should fail
	// because validateSnapshotPath rejects empty strings.
	sm := NewSnapshotManager(d, filepath.Join(t.TempDir(), "snap.db"), "")
	_, err := sm.SetSnapshotPath("")
	if err == nil {
		t.Fatal("SetSnapshotPath with empty default should fail validation")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	// Open a DB and insert data.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	snapshotPath := filepath.Join(dir, "snapshots", "vault.db")

	srcDB, err := Open(srcPath)
	if err != nil {
		t.Fatalf("open source DB: %v", err)
	}

	_, err = srcDB.CreateStorageDestination(StorageDestination{
		Name:   "test-dest",
		Type:   "local",
		Config: `{"path":"/mnt/backups"}`,
	})
	if err != nil {
		t.Fatalf("insert storage destination: %v", err)
	}

	// Save a snapshot.
	sm := NewSnapshotManager(srcDB, snapshotPath, snapshotPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	srcDB.Close()

	// Open a fresh DB and restore the snapshot into it.
	freshPath := filepath.Join(dir, "fresh.db")
	freshDB, err := Open(freshPath)
	if err != nil {
		t.Fatalf("open fresh DB: %v", err)
	}

	sm2 := NewSnapshotManager(freshDB, snapshotPath, snapshotPath)
	if err := sm2.RestoreFromSnapshot(); err != nil {
		t.Fatalf("RestoreFromSnapshot: %v", err)
	}
	freshDB.Close()

	// Reopen and verify the data survived.
	reopened, err := Open(freshPath)
	if err != nil {
		t.Fatalf("reopen DB: %v", err)
	}
	defer reopened.Close()

	dests, err := reopened.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list storage destinations: %v", err)
	}
	if len(dests) != 1 {
		t.Fatalf("got %d destinations, want 1", len(dests))
	}
	if dests[0].Name != "test-dest" {
		t.Errorf("Name = %q, want %q", dests[0].Name, "test-dest")
	}
}

func TestSnapshotManagerNoSnapshotFile(t *testing.T) {
	d := setupTestDB(t)
	path := filepath.Join(t.TempDir(), "nonexistent", "vault.db")
	sm := NewSnapshotManager(d, path, path)

	err := sm.RestoreFromSnapshot()
	if err != nil {
		t.Fatalf("RestoreFromSnapshot with no file should return nil, got: %v", err)
	}
}

func TestSnapshotManagerLastSnapshot(t *testing.T) {
	d := setupTestDB(t)
	snapshotPath := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)

	// Before any save, LastSnapshot should be zero.
	if !sm.LastSnapshot().IsZero() {
		t.Errorf("LastSnapshot before save should be zero, got %v", sm.LastSnapshot())
	}

	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// After save, LastSnapshot should be non-zero.
	if sm.LastSnapshot().IsZero() {
		t.Error("LastSnapshot after save should be non-zero")
	}
}

func TestRestoreFromPath(t *testing.T) {
	dir := t.TempDir()

	// Create a source DB with data and snapshot it.
	srcDB, err := Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	_, err = srcDB.CreateStorageDestination(StorageDestination{
		Name:   "from-path",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("insert data: %v", err)
	}
	snapshotPath := filepath.Join(dir, "snapshot.db")
	sm := NewSnapshotManager(srcDB, snapshotPath, snapshotPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	srcDB.Close()

	// Open a fresh DB and restore using RestoreFromPath.
	freshDB, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	unusedPath := filepath.Join(dir, "unused.db")
	sm2 := NewSnapshotManager(freshDB, unusedPath, unusedPath)
	if err := sm2.RestoreFromPath(snapshotPath); err != nil {
		t.Fatalf("RestoreFromPath: %v", err)
	}

	dests, err := freshDB.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dests) != 1 || dests[0].Name != "from-path" {
		t.Errorf("got %d destinations, want 1 named 'from-path'", len(dests))
	}
}

func TestRestoreFromPathNotExist(t *testing.T) {
	d := setupTestDB(t)
	path := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, path, path)

	err := sm.RestoreFromPath("/nonexistent/vault.db")
	if err == nil {
		t.Fatal("RestoreFromPath with nonexistent file should return error")
	}
}

func TestSaveSnapshotAndUSBBackup(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)

	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("SaveSnapshotAndUSBBackup: %v", err)
	}

	// Both files should exist.
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Errorf("snapshot file not created: %v", err)
	}
	if _, err := os.Stat(usbPath); err != nil {
		t.Errorf("USB backup file not created: %v", err)
	}
}

func TestFlushToUSB(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)
	sm.usbMinInterval = 1 * time.Hour

	// First throttled write.
	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("SaveSnapshotAndUSBBackup: %v", err)
	}
	fi1, err := os.Stat(usbPath)
	if err != nil {
		t.Fatalf("USB backup not created: %v", err)
	}
	modTime1 := fi1.ModTime()

	// FlushToUSB should bypass the throttle and update the file.
	// Ensure enough time passes for modtime to differ.
	time.Sleep(10 * time.Millisecond)
	if err := sm.FlushToUSB(); err != nil {
		t.Fatalf("FlushToUSB: %v", err)
	}
	fi2, err := os.Stat(usbPath)
	if err != nil {
		t.Fatalf("USB backup missing after flush: %v", err)
	}
	if !fi2.ModTime().After(modTime1) {
		t.Error("FlushToUSB did not bypass throttle — USB file was not updated")
	}
}

// TestFlushToUSB_USBIsSnapshotArtifact: when the primary snapshot save
// succeeds, the USB shadow must be a byte-copy of that exact artifact with an
// identical mtime, so the boot-time freshest-source selection (issue #241)
// sees an exact tie and reports the primary — never a "newer" USB shadow that
// could mask or be masked by interleaved mutations.
func TestFlushToUSB_USBIsSnapshotArtifact(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)

	if err := sm.FlushToUSB(); err != nil {
		t.Fatalf("FlushToUSB: %v", err)
	}
	snapFi, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	usbFi, err := os.Stat(usbPath)
	if err != nil {
		t.Fatalf("stat usb: %v", err)
	}
	if !usbFi.ModTime().Equal(snapFi.ModTime()) {
		t.Errorf("USB mtime %v != snapshot mtime %v — same-flush copies must tie exactly", usbFi.ModTime(), snapFi.ModTime())
	}
	snapBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	usbBytes, err := os.ReadFile(usbPath)
	if err != nil {
		t.Fatalf("read usb: %v", err)
	}
	if !bytes.Equal(snapBytes, usbBytes) {
		t.Error("USB shadow is not a byte-copy of the primary snapshot artifact")
	}
}

// TestSaveUSBBackup_NoLiveFallbackWhenArtifactCopyFails: when the primary
// save reportedly succeeded but the artifact copy fails, the shadow must NOT
// be captured from the live DB — a mutation committed after the primary save
// would give it divergent, newer-mtime content. Keep the previous shadow.
func TestSaveUSBBackup_NoLiveFallbackWhenArtifactCopyFails(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	// snapshotPath does not exist, so copySnapshotArtifact must fail.
	sm := NewSnapshotManager(d, filepath.Join(dir, "missing-snap.db"), filepath.Join(dir, "missing-snap.db"))
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")
	sm.SetUSBBackupPath(usbPath)

	if err := sm.saveUSBBackup(true, true); err == nil {
		t.Error("expected an artifact-copy error from saveUSBBackup, got nil")
	}

	if _, err := os.Stat(usbPath); !os.IsNotExist(err) {
		t.Errorf("USB shadow was written from the live DB despite fromSnapshot=true and a failed artifact copy (stat err = %v)", err)
	}
}

func TestFlushToUSB_SnapshotFails(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	// Use a path whose parent is a regular file so MkdirAll always fails,
	// even when running as root (e.g. in Docker / CI).
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(blocker, "sub", "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)

	// FlushToUSB should return a snapshot error but still write USB backup.
	err := sm.FlushToUSB()
	if err == nil {
		t.Fatal("expected snapshot error, got nil")
	}
	if _, statErr := os.Stat(usbPath); statErr != nil {
		t.Fatalf("USB backup should still be written when snapshot fails: %v", statErr)
	}
}

func TestUSBBackupThrottling(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)
	sm.usbMinInterval = 1 * time.Hour // large interval for test

	// First save should create the USB backup.
	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("first SaveSnapshotAndUSBBackup: %v", err)
	}

	fi1, err := os.Stat(usbPath)
	if err != nil {
		t.Fatalf("USB backup not created: %v", err)
	}
	modTime1 := fi1.ModTime()

	// Second save within interval should NOT update USB backup.
	if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
		t.Fatalf("second SaveSnapshotAndUSBBackup: %v", err)
	}

	fi2, err := os.Stat(usbPath)
	if err != nil {
		t.Fatalf("USB backup missing: %v", err)
	}
	if fi2.ModTime() != modTime1 {
		t.Error("USB backup was updated within throttle interval")
	}
}

func TestRestorationInfo(t *testing.T) {
	d := setupTestDB(t)
	path := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, path, path)

	// Initially nil.
	if sm.RestorationSource() != nil {
		t.Error("RestorationSource should be nil initially")
	}

	info := &RestorationInfo{
		Source: "primary",
		Path:   "/mnt/cache/.vault/vault.db",
		Reason: "restored from configured snapshot path",
	}
	sm.SetRestorationInfo(info)

	got := sm.RestorationSource()
	if got == nil {
		t.Fatal("RestorationSource should not be nil after SetRestorationInfo")
	}
	if got.Source != "primary" {
		t.Errorf("Source = %q, want %q", got.Source, "primary")
	}
}

func TestSetSnapshotPath(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	// Insert data so we can verify it survives the path change.
	_, err := d.CreateStorageDestination(StorageDestination{
		Name:   "path-change-test",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("insert data: %v", err)
	}

	originalPath := filepath.Join(dir, "original", "vault.db")
	defaultPath := filepath.Join(dir, "default", "vault.db")
	sm := NewSnapshotManager(d, originalPath, defaultPath)

	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatalf("original snapshot not created: %v", err)
	}

	// Change to a custom path — should save a fresh snapshot there.
	customPath := filepath.Join(dir, "custom", "vault.db")
	if _, err := sm.SetSnapshotPath(customPath); err != nil {
		t.Fatalf("SetSnapshotPath custom: %v", err)
	}
	if sm.SnapshotPath() != customPath {
		t.Errorf("SnapshotPath() = %q, want %q", sm.SnapshotPath(), customPath)
	}
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom snapshot not created: %v", err)
	}

	// Reset to default (empty string) — should use defaultPath.
	if _, err := sm.SetSnapshotPath(""); err != nil {
		t.Fatalf("SetSnapshotPath reset: %v", err)
	}
	if sm.SnapshotPath() != defaultPath {
		t.Errorf("SnapshotPath() after reset = %q, want %q", sm.SnapshotPath(), defaultPath)
	}
	if _, err := os.Stat(defaultPath); err != nil {
		t.Fatalf("default snapshot not created after reset: %v", err)
	}

	// Verify the snapshot at the default path has fresh data.
	freshDB, err := Open(filepath.Join(dir, "verify.db"))
	if err != nil {
		t.Fatalf("open verify DB: %v", err)
	}
	defer freshDB.Close()
	verifier := NewSnapshotManager(freshDB, defaultPath, defaultPath)
	if err := verifier.RestoreFromSnapshot(); err != nil {
		t.Fatalf("restore from default: %v", err)
	}
	dests, err := freshDB.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dests) != 1 || dests[0].Name != "path-change-test" {
		t.Errorf("got %d destinations, want 1 named 'path-change-test'", len(dests))
	}
}

func TestSaveSnapshotRunsWALCheckpoint(t *testing.T) {
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer src.Close()

	// Write rows so there are pages in the WAL.
	for i := 0; i < 100; i++ {
		if _, err := src.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`,
			fmt.Sprintf("wal-test-%d", i), "v"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	snapPath := filepath.Join(dir, "snap.db")
	sm := NewSnapshotManager(src, snapPath, snapPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// After save with TRUNCATE, the WAL file should be 0 bytes (or absent).
	walPath := filepath.Join(dir, "src.db-wal")
	if fi, err := os.Stat(walPath); err == nil && fi.Size() > 0 {
		t.Errorf("WAL not truncated: size=%d", fi.Size())
	}

	// Verify snapshot is openable and has the data.
	dst, err := Open(snapPath)
	if err != nil {
		t.Fatalf("open snap: %v", err)
	}
	defer dst.Close()
	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'wal-test-%'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 100 {
		t.Errorf("got %d rows in snapshot, want 100", n)
	}
}

func TestSnapshotRotationKeepsSeven(t *testing.T) {
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	snapPath := filepath.Join(dir, "vault.db")
	sm := NewSnapshotManager(src, snapPath, snapPath)

	// 9 saves; rotation should keep only 7 in rotated/.
	for i := 0; i < 9; i++ {
		if _, err := src.Exec(`INSERT INTO settings (key, value) VALUES (?, '')`,
			fmt.Sprintf("rotate-test-%d", i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err := sm.SaveSnapshot(); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		// Brief pause so even nanosecond-resolution timestamps differ.
		time.Sleep(2 * time.Millisecond)
	}

	rotatedDir := filepath.Join(dir, "rotated")
	entries, err := os.ReadDir(rotatedDir)
	if err != nil {
		t.Fatalf("readdir rotated: %v", err)
	}
	if got := len(entries); got != 7 {
		t.Errorf("rotated count = %d, want 7", got)
	}
}

func TestDefaultSnapshotPath(t *testing.T) {
	d := setupTestDB(t)
	dir := t.TempDir()
	activePath := filepath.Join(dir, "active.db")
	defaultPath := filepath.Join(dir, "default.db")

	sm := NewSnapshotManager(d, activePath, defaultPath)
	if sm.DefaultSnapshotPath() != defaultPath {
		t.Errorf("DefaultSnapshotPath() = %q, want %q", sm.DefaultSnapshotPath(), defaultPath)
	}
}

// TestScheduleFlushCoalescesAndFlushes verifies the debounced async flush:
// a burst of config changes coalesces into a single queued flush that runs
// after the debounce window without blocking the caller (issue #143).
func TestScheduleFlushCoalescesAndFlushes(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	snapshotPath := filepath.Join(dir, "snap.db")
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")

	sm := NewSnapshotManager(d, snapshotPath, snapshotPath)
	sm.SetUSBBackupPath(usbPath)
	sm.flushDebounce = 30 * time.Millisecond

	// A burst of rapid config changes coalesces into one queued flush.
	for i := 0; i < 10; i++ {
		sm.ScheduleFlush()
	}
	if !sm.flushPending.Load() {
		t.Fatal("flushPending = false right after burst, want true (one flush queued)")
	}

	// After the debounce window the flush runs once and clears the flag.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(usbPath); err == nil && !sm.flushPending.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(usbPath); err != nil {
		t.Fatalf("USB backup not created after ScheduleFlush: %v", err)
	}
	if sm.flushPending.Load() {
		t.Error("flushPending still true after flush completed")
	}
}

// TestSaveSnapshotAtomic pins the #182 contract: snapshot writes go through
// a temp file + rename, leaving no .tmp residue and always producing a
// readable snapshot at the destination.
func TestSaveSnapshotAtomic(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dir := t.TempDir()
	snap := filepath.Join(dir, "vault.db")
	sm := NewSnapshotManager(d, snap, snap)

	for i := 0; i < 2; i++ { // second save overwrites the first atomically
		if err := sm.SaveSnapshot(); err != nil {
			t.Fatalf("SaveSnapshot #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if _, err := os.Stat(snap + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind after successful save (#182)")
	}
	// The snapshot must be an openable SQLite database.
	check, err := Open(snap)
	if err != nil {
		t.Fatalf("snapshot not a valid database: %v", err)
	}
	_ = check.Close()
}

func TestRemoveValidated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "victim.tmp")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeValidated(p); err != nil {
		t.Fatalf("valid absolute path refused: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if err := removeValidated("relative/path.tmp"); err == nil {
		t.Fatal("relative path must be refused")
	}
	if err := removeValidated(filepath.Join(dir, "..", "escape.tmp")); err == nil {
		t.Fatal("traversal path must be refused")
	}
}

// newMigrationTestManager returns a SnapshotManager whose snapshot already
// exists at customPath, together with its default path, for exercising
// database-location changes.
func newMigrationTestManager(t *testing.T, dir, snapshotPath, defaultPath string) *SnapshotManager {
	t.Helper()
	srcDB, err := Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatalf("open source DB: %v", err)
	}
	t.Cleanup(func() { srcDB.Close() })
	if _, err := srcDB.CreateStorageDestination(StorageDestination{
		Name: "migration-dest", Type: "local", Config: `{"path":"/mnt/backups"}`,
	}); err != nil {
		t.Fatalf("insert storage destination: %v", err)
	}
	sm := NewSnapshotManager(srcDB, snapshotPath, defaultPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("initial SaveSnapshot: %v", err)
	}
	return sm
}

// TestSetSnapshotPathMovesDatabase covers the round trip a user makes when
// pointing the database at a custom pool and later reverting to the default:
// the snapshot must exist at the new location and leave nothing behind at the
// old one, in both directions. A leftover stale copy is not cosmetic — the
// boot-time freshest-source scan ranks it as a restore candidate, so it can
// silently undo the location change on a later boot.
func TestSetSnapshotPathMovesDatabase(t *testing.T) {
	dir := t.TempDir()
	defaultPath := filepath.Join(dir, "cache", ".vault", "vault.db")
	customPath := filepath.Join(dir, "garbage", "vault.db")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o750); err != nil {
		t.Fatal(err)
	}
	sm := newMigrationTestManager(t, dir, defaultPath, defaultPath)

	// Rotated copies accumulate beside the snapshot and must travel with it.
	sm.rotateSnapshot()
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(defaultPath), "rotated")); err != nil || len(entries) == 0 {
		t.Fatalf("expected a rotated copy at the default location, got %v (%v)", entries, err)
	}

	// Default -> custom.
	migration, err := sm.SetSnapshotPath(customPath)
	if err != nil {
		t.Fatalf("SetSnapshotPath(custom): %v", err)
	}
	// The move must be reportable so the UI can confirm it, not just that the
	// setting was saved.
	if migration == nil {
		t.Fatal("expected a migration result for a location change")
	}
	if !migration.Completed {
		t.Errorf("migration not reported complete: %+v", migration)
	}
	if migration.To != customPath || migration.From != defaultPath {
		t.Errorf("migration reported %s -> %s, want %s -> %s",
			migration.From, migration.To, defaultPath, customPath)
	}
	// vault.db plus the rotated copy created above.
	if migration.FilesRetired < 2 {
		t.Errorf("FilesRetired = %d, want at least 2", migration.FilesRetired)
	}
	if fi, err := os.Stat(customPath); err != nil || fi.Size() == 0 {
		t.Fatalf("expected snapshot at custom path, got %v (%v)", fi, err)
	}
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Errorf("old snapshot still present at %s (err=%v)", defaultPath, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(defaultPath), "rotated")); !os.IsNotExist(err) {
		t.Errorf("rotated copies left behind at the default location")
	}
	// ".vault" is Vault's own directory, so it is removed once emptied.
	if _, err := os.Stat(filepath.Dir(defaultPath)); !os.IsNotExist(err) {
		t.Errorf("empty .vault directory left behind at %s", filepath.Dir(defaultPath))
	}

	// Custom -> back to default (empty string selects defaultSnapshotPath).
	if _, err := sm.SetSnapshotPath(""); err != nil {
		t.Fatalf("SetSnapshotPath(default): %v", err)
	}
	if fi, err := os.Stat(defaultPath); err != nil || fi.Size() == 0 {
		t.Fatalf("expected snapshot back at default path, got %v (%v)", fi, err)
	}
	if _, err := os.Stat(customPath); !os.IsNotExist(err) {
		t.Errorf("snapshot still present at custom path %s after reverting", customPath)
	}
}

// TestSetSnapshotPathPreservesForeignFiles guards the most dangerous case: a
// user pointing the database at a share or pool root (e.g. /mnt/garbage).
// Retiring the old location must remove only Vault's own files and must never
// delete the directory itself or anything else living in it.
func TestSetSnapshotPathPreservesForeignFiles(t *testing.T) {
	dir := t.TempDir()
	poolRoot := filepath.Join(dir, "garbage")
	oldPath := filepath.Join(poolRoot, "vault.db")
	newPath := filepath.Join(dir, "cache", ".vault", "vault.db")
	if err := os.MkdirAll(poolRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	// A user file and a foreign file inside Vault's rotated directory.
	userFile := filepath.Join(poolRoot, "important-user-data.txt")
	if err := os.WriteFile(userFile, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm := newMigrationTestManager(t, dir, oldPath, newPath)
	sm.rotateSnapshot()
	foreignRotated := filepath.Join(poolRoot, "rotated", "someone-elses.db")
	if err := os.WriteFile(foreignRotated, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.SetSnapshotPath(newPath); err != nil {
		t.Fatalf("SetSnapshotPath: %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("vault.db not retired from %s", poolRoot)
	}
	if _, err := os.Stat(poolRoot); err != nil {
		t.Fatalf("pool root directory was removed: %v", err)
	}
	if b, err := os.ReadFile(userFile); err != nil || string(b) != "do not delete" {
		t.Errorf("user file was disturbed: %q (%v)", b, err)
	}
	if _, err := os.Stat(foreignRotated); err != nil {
		t.Errorf("foreign file inside rotated/ was removed: %v", err)
	}
}

// TestSetSnapshotPathKeepsOldWhenNewUnusable ensures the old copy survives if
// the new snapshot did not materialise — the database must never be deleted
// from a location that still holds the only good copy.
func TestSetSnapshotPathKeepsOldWhenNewUnusable(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old", "vault.db")
	newPath := filepath.Join(dir, "new", "vault.db")
	sm := newMigrationTestManager(t, dir, oldPath, oldPath)

	// Simulate a save that reported success but produced nothing usable.
	if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sm.retireSnapshotArtifacts(oldPath, newPath)

	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("old snapshot removed despite unusable new snapshot: %v", err)
	}
}

// TestRetireSnapshotArtifactsNoOpOnSamePath ensures re-applying the current
// path never deletes the live snapshot.
func TestRetireSnapshotArtifactsNoOpOnSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")
	sm := newMigrationTestManager(t, dir, path, path)

	sm.retireSnapshotArtifacts(path, path)

	if _, err := os.Stat(path); err != nil {
		t.Errorf("snapshot deleted when path was unchanged: %v", err)
	}
}

// TestPathBarriersRejectSuspiciousPaths locks in the guards that stand between
// the operator-configured database location and the filesystem. They are the
// barrier CodeQL recognises for go/path-injection, so weakening them silently
// re-opens those alerts as well as the underlying exposure.
func TestPathBarriersRejectSuspiciousPaths(t *testing.T) {
	t.Parallel()

	bad := []struct{ name, path string }{
		{"relative", "relative/vault.db"},
		{"traversal", "/mnt/user/../../etc/vault.db"},
		{"bare traversal", ".."},
		{"empty", ""},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := removeValidated(tc.path); err == nil {
				t.Errorf("removeValidated(%q) = nil, want refusal", tc.path)
			}
			if err := mkdirAllValidated(tc.path, 0o750); err == nil {
				t.Errorf("mkdirAllValidated(%q) = nil, want refusal", tc.path)
			}
			if _, err := statValidated(tc.path); err == nil {
				t.Errorf("statValidated(%q) = nil, want refusal", tc.path)
			}
		})
	}

	// A legitimate absolute path must still work, or the guards would break
	// the feature they protect.
	dir := t.TempDir()
	nested := filepath.Join(dir, "pool", ".vault")
	if err := mkdirAllValidated(nested, 0o750); err != nil {
		t.Fatalf("mkdirAllValidated(%q): %v", nested, err)
	}
	snap := filepath.Join(nested, "vault.db")
	if err := os.WriteFile(snap, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := statValidated(snap); err != nil {
		t.Errorf("statValidated(%q): %v", snap, err)
	}
	if err := removeValidated(snap); err != nil {
		t.Errorf("removeValidated(%q): %v", snap, err)
	}
}
