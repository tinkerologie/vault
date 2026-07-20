package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	info := restoreWithFallback(sm2, configuredPath, filepath.Join(dir, "default.db"), "", filepath.Join(dir, "usb.db"))

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
	info := restoreWithFallback(sm2, "/nonexistent/configured.db", defaultCachePath, "", filepath.Join(dir, "usb.db"))

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
	info := restoreWithFallback(sm2, "/nonexistent/a.db", "/nonexistent/b.db", "", usbPath)

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
	info := restoreWithFallback(sm, "/nonexistent/a.db", "/nonexistent/b.db", "", "/nonexistent/c.db")

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
	info := restoreWithFallback(sm, samePath, samePath, "", "/nonexistent/usb.db")

	// Should fall through to fresh (USB doesn't exist either).
	if info.Source != "fresh" {
		t.Errorf("Source = %q, want %q", info.Source, "fresh")
	}
}

// makeSnapshotDB creates a DB with one storage destination in its own subdir
// (so snapshots do not share a rotated/ dir) and returns the snapshot path.
func makeSnapshotDB(t *testing.T, dir, sub, destName string) string {
	t.Helper()
	p := filepath.Join(dir, sub, "src.db")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	d, err := db.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", sub, err)
	}
	if _, err := d.CreateStorageDestination(db.StorageDestination{
		Name: destName, Type: "local", Config: `{"path":"/tmp"}`,
	}); err != nil {
		t.Fatalf("insert %s: %v", sub, err)
	}
	sm := db.NewSnapshotManager(d, p+".snap", p+".snap")
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("snapshot %s: %v", sub, err)
	}
	d.Close()
	return p + ".snap"
}

// ageTree sets the mtime of every file under root to ts.
func ageTree(t *testing.T, root string, ts time.Time) {
	t.Helper()
	if err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		return os.Chtimes(p, ts, ts)
	}); err != nil {
		t.Fatalf("chtimes %s: %v", root, err)
	}
}

// TestRestoreWithFallback_FreshestSourceWins reproduces issue #241: a boot in
// USB-direct mode (pool unavailable) writes the newest configuration to the
// live USB DB; the next hybrid boot must restore that instead of the older
// cache snapshot, or the configuration silently reverts.
func TestRestoreWithFallback_FreshestSourceWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stalePrimary := makeSnapshotDB(t, dir, "stale", "old-config")
	freshUSBLive := makeSnapshotDB(t, dir, "fresh-usb", "new-config")

	// The primary snapshot (and its rotated copies) are a day older than the
	// USB-direct live DB.
	ageTree(t, filepath.Dir(stalePrimary), time.Now().Add(-24*time.Hour))

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, stalePrimary, "/nonexistent/default.db", freshUSBLive, "/nonexistent/usb.db")

	if info.Source != "usb_direct" {
		t.Errorf("Source = %q, want %q (freshest valid source must win over stale primary)", info.Source, "usb_direct")
	}
	dests, err := freshDB.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dests) != 1 || dests[0].Name != "new-config" {
		t.Errorf("restored destinations = %+v, want the fresh USB-direct config", dests)
	}
}

// TestRestoreWithFallback_USBDirectSlightlyNewerWins guards the fix for the
// adversarial-review finding on #241: even a small freshness advantage (well
// under the usb_backup twin window) must let the divergent USB-direct DB win
// over the primary snapshot — freshness clamping applies only to the
// same-content USB shadow, never to usb_direct.
func TestRestoreWithFallback_USBDirectSlightlyNewerWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stalePrimary := makeSnapshotDB(t, dir, "stale", "old-config")
	freshUSBLive := makeSnapshotDB(t, dir, "fresh-usb", "new-config")

	// Primary is only 30 seconds older than the USB-direct DB.
	now := time.Now()
	ageTree(t, filepath.Dir(stalePrimary), now.Add(-30*time.Second))
	ageTree(t, filepath.Dir(freshUSBLive), now)

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, stalePrimary, "/nonexistent/default.db", freshUSBLive, "/nonexistent/usb.db")

	if info.Source != "usb_direct" {
		t.Errorf("Source = %q, want %q (30 s fresher usb_direct must win)", info.Source, "usb_direct")
	}
}

// TestRestoreWithFallback_PrimaryLabelPreferredOverUSBTwin: FlushToUSB aligns
// the USB shadow's mtime with the primary snapshot it was written alongside
// (SnapshotManager.alignUSBTwinMtime). With equal mtimes, tier order must
// report the primary as the restore source so the health endpoint does not
// claim a degraded usb_backup restore every boot.
func TestRestoreWithFallback_PrimaryLabelPreferredOverUSBTwin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	primary := makeSnapshotDB(t, dir, "primary", "config")
	usbTwin := makeSnapshotDB(t, dir, "usb", "config-usb-twin")

	// Same-flush twins carry identical mtimes after alignment.
	now := time.Now()
	ageTree(t, filepath.Dir(primary), now)
	ageTree(t, filepath.Dir(usbTwin), now)

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, primary, "/nonexistent/default.db", "", usbTwin)

	if info.Source != "primary" {
		t.Errorf("Source = %q, want %q (equal-mtime USB twin must not outrank primary)", info.Source, "primary")
	}
}

// TestRestoreWithFallback_NewerUSBBackupBeatsStalePrimary covers the
// failed-primary-save scenario from the adversarial review: FlushToUSB writes
// the USB shadow even when the primary snapshot save fails, so a USB backup
// only seconds newer than the primary is genuinely divergent and must win.
func TestRestoreWithFallback_NewerUSBBackupBeatsStalePrimary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stalePrimary := makeSnapshotDB(t, dir, "primary", "old-config")
	newerUSB := makeSnapshotDB(t, dir, "usb", "new-config")

	now := time.Now()
	ageTree(t, filepath.Dir(stalePrimary), now.Add(-30*time.Second))
	ageTree(t, filepath.Dir(newerUSB), now)

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, stalePrimary, "/nonexistent/default.db", "", newerUSB)

	if info.Source != "usb_backup" {
		t.Errorf("Source = %q, want %q (30 s fresher USB backup must beat stale primary)", info.Source, "usb_backup")
	}
	dests, err := freshDB.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dests) != 1 || dests[0].Name != "new-config" {
		t.Errorf("restored destinations = %+v, want the newer USB backup config", dests)
	}
}

// TestRestoreWithFallback_WALSidecarCountsForFreshness: a USB-direct DB left
// with an uncheckpointed WAL after an abrupt stop keeps an old mtime on the
// main file; the -wal sidecar's newer mtime must count, or a stale cache
// snapshot outranks it and reverts the committed configuration (issue #241).
func TestRestoreWithFallback_WALSidecarCountsForFreshness(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	midPrimary := makeSnapshotDB(t, dir, "primary", "mid-config")
	usbLive := makeSnapshotDB(t, dir, "usb-live", "usb-config")

	now := time.Now()
	ageTree(t, filepath.Dir(usbLive), now.Add(-1*time.Hour))
	ageTree(t, filepath.Dir(midPrimary), now.Add(-30*time.Minute))
	// Empty WAL sidecar (valid for SQLite) with the newest mtime marks the
	// USB-direct DB as holding the freshest committed state.
	if err := os.WriteFile(usbLive+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(usbLive+"-wal", now, now); err != nil {
		t.Fatal(err)
	}

	freshDB, err := db.Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer freshDB.Close()

	sm := db.NewSnapshotManager(freshDB, filepath.Join(dir, "working-snap.db"), filepath.Join(dir, "working-snap.db"))
	info := restoreWithFallback(sm, midPrimary, "/nonexistent/default.db", usbLive, "/nonexistent/usb.db")

	if info.Source != "usb_direct" {
		t.Errorf("Source = %q, want %q (WAL sidecar mtime must count toward freshness)", info.Source, "usb_direct")
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
