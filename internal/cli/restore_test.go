package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestRestoreWithFallback_Primary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a source DB and snapshot it to the "configured" path.
	srcDB, err := db.Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	_, err = srcDB.CreateStorageDestination(db.StorageDestination{
		Name: "primary-src", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	configuredPath := filepath.Join(dir, "configured", "vault.db")
	sm := db.NewSnapshotManager(srcDB, configuredPath, configuredPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	srcDB.Close()

	// Open a fresh RAM-style DB and try fallback.
	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm2 := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm2, configuredPath, filepath.Join(dir, "default.db"), filepath.Join(dir, "usb.db"))

	if info.Source != "primary" {
		t.Errorf("Source = %q, want %q", info.Source, "primary")
	}
	if info.Path != configuredPath {
		t.Errorf("Path = %q, want %q", info.Path, configuredPath)
	}
}

func TestRestoreWithFallback_DefaultCache(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a snapshot at the default cache path only.
	srcDB, err := db.Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	_, err = srcDB.CreateStorageDestination(db.StorageDestination{
		Name: "cache-src", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	defaultCachePath := filepath.Join(dir, "cache", "vault.db")
	sm := db.NewSnapshotManager(srcDB, defaultCachePath, defaultCachePath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	srcDB.Close()

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm2 := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm2, "/nonexistent/configured.db", defaultCachePath, filepath.Join(dir, "usb.db"))

	if info.Source != "default_cache" {
		t.Errorf("Source = %q, want %q", info.Source, "default_cache")
	}
}

func TestRestoreWithFallback_USBBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a snapshot at the USB backup path only.
	srcDB, err := db.Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	_, err = srcDB.CreateStorageDestination(db.StorageDestination{
		Name: "usb-src", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	usbPath := filepath.Join(dir, "usb", "vault.db.backup")
	sm := db.NewSnapshotManager(srcDB, usbPath, usbPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	srcDB.Close()

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm2 := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm2, "/nonexistent/a.db", "/nonexistent/b.db", usbPath)

	if info.Source != "usb_backup" {
		t.Errorf("Source = %q, want %q", info.Source, "usb_backup")
	}
}

func TestRestoreWithFallback_Fresh(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, "/nonexistent/a.db", "/nonexistent/b.db", "/nonexistent/c.db")

	if info.Source != "fresh" {
		t.Errorf("Source = %q, want %q", info.Source, "fresh")
	}
	if info.Path != "" {
		t.Errorf("Path = %q, want empty", info.Path)
	}
}

func TestRestoreWithFallback_SkipsDuplicatePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// When configuredPath == defaultCachePath, the default_cache fallback
	// should be skipped (no double attempt on the same path).
	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	samePath := "/nonexistent/same.db"
	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, samePath, samePath, "/nonexistent/usb.db")

	// Should fall through to fresh (USB doesn't exist either).
	if info.Source != "fresh" {
		t.Errorf("Source = %q, want %q", info.Source, "fresh")
	}
}

func TestValidateConfiguredPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	// Create an accessible directory for staging_dir_override.
	stagingDir := filepath.Join(dir, "staging")
	if err := os.Mkdir(stagingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Set valid paths.
	if err := database.SetSetting("snapshot_path_override", filepath.Join(dir, "snap.db")); err != nil {
		t.Fatalf("set snapshot: %v", err)
	}
	if err := database.SetSetting("staging_dir_override", stagingDir); err != nil {
		t.Fatalf("set staging: %v", err)
	}

	// Should not panic or error (just logs warnings).
	validateConfiguredPaths(database)
}
