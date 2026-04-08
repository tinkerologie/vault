package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestFlushToUSB_SnapshotFails(t *testing.T) {
	dir := t.TempDir()
	d := setupTestDB(t)

	// Use an invalid snapshot path so SaveSnapshot will fail.
	snapshotPath := "/nonexistent/dir/snap.db"
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
	if err := sm.SetSnapshotPath(customPath); err != nil {
		t.Fatalf("SetSnapshotPath custom: %v", err)
	}
	if sm.SnapshotPath() != customPath {
		t.Errorf("SnapshotPath() = %q, want %q", sm.SnapshotPath(), customPath)
	}
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom snapshot not created: %v", err)
	}

	// Reset to default (empty string) — should use defaultPath.
	if err := sm.SetSnapshotPath(""); err != nil {
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
