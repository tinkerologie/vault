package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/logx"
	"github.com/tinkerologie/vault/internal/tempdir"
	"github.com/tinkerologie/vault/internal/unraid"
)

// daemonEnv458 isolates every host path the daemon touches at startup.
type daemonEnv458 struct {
	dir       string
	mntDir    string
	mountInfo string
	usbBackup string
	dbPath    string
	sleeps    int
	onSleep   func(n int)
}

func newDaemonEnv458(t *testing.T) *daemonEnv458 {
	t.Helper()
	dir := t.TempDir()
	e := &daemonEnv458{
		dir:       dir,
		mntDir:    filepath.Join(dir, "mnt"),
		mountInfo: filepath.Join(dir, "mountinfo"),
		usbBackup: filepath.Join(dir, "vault.db.backup"),
		dbPath:    filepath.Join(dir, "boot", "vault.db"),
	}
	// The heartbeat goroutine writes asynchronously and may outlive RunE.
	// A directory at each heartbeat path makes that write fail harmlessly
	// instead of racing t.TempDir cleanup.
	for _, d := range []string{
		e.mntDir,
		filepath.Join(filepath.Dir(e.dbPath), "heartbeat"),
		filepath.Join(dir, "hybrid", "heartbeat"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(e.mountInfo, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unraid.SetMntBaseForTest(e.mntDir, e.mountInfo))
	t.Cleanup(tempdir.SetCachePathsForTest([]string{filepath.Join(dir, "stagecache")}))

	prevWD, prevUSB, prevSleep, prevNotify := hybridWorkingDir, daemonUSBBackupPath, poolDetectionSleep, shutdownNotifyContext
	hybridWorkingDir = filepath.Join(dir, "hybrid")
	daemonUSBBackupPath = e.usbBackup
	poolDetectionSleep = func(time.Duration) {
		e.sleeps++
		if e.onSleep != nil {
			e.onSleep(e.sleeps)
		}
	}
	t.Cleanup(func() {
		hybridWorkingDir, daemonUSBBackupPath, poolDetectionSleep, shutdownNotifyContext = prevWD, prevUSB, prevSleep, prevNotify
		logx.SetLevelString("info")
	})
	return e
}

// mountPool creates a pool directory and optionally registers it as mounted.
func (e *daemonEnv458) mountPool(t *testing.T, name string, mounted bool) string {
	t.Helper()
	p := filepath.Join(e.mntDir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if mounted {
		e.markMounted(t, p)
	}
	return p
}

func (e *daemonEnv458) markMounted(t *testing.T, p string) {
	t.Helper()
	line := "1 0 0:0 / " + p + " rw - btrfs /dev/sda1 rw\n"
	if err := os.WriteFile(e.mountInfo, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gracefulShutdown makes the daemon's signal context pre-cancelled so the
// API server shuts down immediately and RunE takes the graceful path.
func gracefulShutdown() {
	shutdownNotifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
}

// run executes RunE with a pre-cancelled shutdown context, so the daemon
// starts up, then immediately takes the graceful shutdown path. Waiting for
// the graceful path (rather than failing the listener) guarantees the
// pre-shutdown flush has finished before the test's temp dirs are removed.
func (e *daemonEnv458) run(t *testing.T) error {
	t.Helper()
	gracefulShutdown()
	cmd := &cobra.Command{Use: "daemon"}
	cmd.SetContext(context.Background())
	cmd.Flags().String("db", e.dbPath, "")
	cmd.Flags().String("addr", "127.0.0.1:0", "")
	cmd.Flags().String("tls-cert", "", "")
	cmd.Flags().String("tls-key", "", "")
	return daemonCmd.RunE(cmd, nil)
}

func seedDB458(t *testing.T, path string, settings map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range settings {
		if err := d.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func readSetting458(t *testing.T, path, key string) string {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	v, err := d.GetSetting(key, "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDaemon458_NoPool_RestoresFromUSBBackup_Graceful(t *testing.T) {
	e := newDaemonEnv458(t)
	seedDB458(t, e.usbBackup, map[string]string{
		"encryption_passphrase":  "legacy-secret",
		"history_retention_days": "not-a-number",
	})

	if err := e.run(t); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if e.sleeps != 14 {
		t.Errorf("pool wait sleeps = %d, want 14", e.sleeps)
	}
	// Legacy passphrase migrated to sealed storage.
	if got := readSetting458(t, e.dbPath, "encryption_passphrase"); got != "" {
		t.Errorf("plaintext passphrase not cleared: %q", got)
	}
	if got := readSetting458(t, e.dbPath, "encryption_passphrase_sealed"); got == "" {
		t.Error("sealed passphrase not written")
	}
}

func TestDaemon458_NoPool_CorruptUSBBackup(t *testing.T) {
	e := newDaemonEnv458(t)
	if err := os.WriteFile(e.usbBackup, []byte("garbage, not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The corrupt backup is rejected and the daemon starts on a fresh DB.
	if err := e.run(t); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := os.Stat(e.dbPath); err != nil {
		t.Errorf("fresh DB not created: %v", err)
	}
}

func TestDaemon458_NoPool_NewerUSBBackupReplacesLiveDB(t *testing.T) {
	e := newDaemonEnv458(t)
	seedDB458(t, e.dbPath, map[string]string{"log_level": "info"})
	seedDB458(t, e.usbBackup, map[string]string{"log_level": "warn"})
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(e.dbPath, old, old); err != nil {
		t.Fatal(err)
	}
	_ = e.run(t)
	if got := readSetting458(t, e.dbPath, "log_level"); got != "warn" {
		t.Errorf("log_level = %q, want warn (restored from newer backup)", got)
	}
}

func TestDaemon458_NoPool_NewerCorruptBackupKeepsLiveDB(t *testing.T) {
	e := newDaemonEnv458(t)
	seedDB458(t, e.dbPath, map[string]string{"log_level": "error"})
	if err := os.WriteFile(e.usbBackup, []byte("garbage, not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(e.dbPath, old, old); err != nil {
		t.Fatal(err)
	}
	_ = e.run(t)
	if got := readSetting458(t, e.dbPath, "log_level"); got != "error" {
		t.Errorf("log_level = %q, want error (live DB kept)", got)
	}
}

func TestDaemon458_PoolAppearsAfterRetry(t *testing.T) {
	e := newDaemonEnv458(t)
	pool := e.mountPool(t, "cache", false)
	e.onSleep = func(n int) {
		if n == 1 {
			e.markMounted(t, pool)
		}
	}
	_ = e.run(t)
	if e.sleeps != 1 {
		t.Errorf("sleeps = %d, want 1", e.sleeps)
	}
}

func TestDaemon458_PoolUnmounted_FallsBackToUSB(t *testing.T) {
	e := newDaemonEnv458(t)
	e.mountPool(t, "cache", false)
	_ = e.run(t)
	if _, err := os.Stat(e.dbPath); err != nil {
		t.Errorf("USB-direct DB not created: %v", err)
	}
}

func TestDaemon458_PoolNotAccessible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	e := newDaemonEnv458(t)
	e.mountPool(t, "cache", false)
	// Readable but not searchable: the pool is listed by ReadDir, yet
	// stat on it fails, so it is reported as not accessible.
	if err := os.Chmod(e.mntDir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.mntDir, 0o700) })
	_ = e.run(t)
	if _, err := os.Stat(e.dbPath); err != nil {
		t.Errorf("USB-direct DB not created: %v", err)
	}
}

func TestDaemon458_Hybrid_MigratesUSBDB_Graceful(t *testing.T) {
	e := newDaemonEnv458(t)
	pool := e.mountPool(t, "cache", true)
	customSnap := filepath.Join(pool, "custom", "vault.db")
	cfg := filepath.Join(filepath.Dir(e.dbPath), "vault.cfg")
	if err := os.WriteFile(cfg, []byte(`SNAPSHOT_PATH="`+customSnap+`"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedDB458(t, e.dbPath, map[string]string{"log_level": "warn"})

	if err := e.run(t); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := os.Stat(customSnap); err != nil {
		t.Errorf("snapshot not written at cfg path: %v", err)
	}
	if _, err := os.Stat(e.usbBackup); err != nil {
		t.Errorf("USB DB not preserved as backup: %v", err)
	}
	if _, err := os.Stat(e.dbPath); !os.IsNotExist(err) {
		t.Errorf("USB live DB should be gone, stat err = %v", err)
	}
	if logx.LevelString() != "warn" {
		t.Errorf("log level = %q, want warn", logx.LevelString())
	}
}

func TestDaemon458_Hybrid_MigrationRenameFails(t *testing.T) {
	e := newDaemonEnv458(t)
	e.mountPool(t, "cache", true)
	seedDB458(t, e.dbPath, nil)
	// Backup destination's parent does not exist → rename fails → remove fallback.
	daemonUSBBackupPath = filepath.Join(e.dir, "missing-dir", "vault.db.backup")
	_ = e.run(t)
	if _, err := os.Stat(e.dbPath); !os.IsNotExist(err) {
		t.Errorf("migrated USB DB should be removed, stat err = %v", err)
	}
}

func TestDaemon458_Hybrid_WorkingDirError(t *testing.T) {
	e := newDaemonEnv458(t)
	e.mountPool(t, "cache", true)
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hybridWorkingDir = filepath.Join(blocker, "sub")
	err := e.run(t)
	if err == nil || !strings.Contains(err.Error(), "hybrid working directory") {
		t.Fatalf("err = %v, want hybrid working directory error", err)
	}
}

func TestDaemon458_Hybrid_SnapshotDirError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	e := newDaemonEnv458(t)
	pool := e.mountPool(t, "cache", true)
	seedDB458(t, e.dbPath, nil)
	// Point SNAPSHOT_PATH beneath a read-only directory: stat reports
	// not-exist, but the snapshot directory cannot be created.
	ro := filepath.Join(pool, "ro")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	cfg := filepath.Join(filepath.Dir(e.dbPath), "vault.cfg")
	snap := filepath.Join(ro, "sub", "vault.db")
	if err := os.WriteFile(cfg, []byte(`SNAPSHOT_PATH="`+snap+`"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.run(t)
	if err == nil || !strings.Contains(err.Error(), "snapshot directory") {
		t.Fatalf("err = %v, want snapshot directory error", err)
	}
}

func TestDaemon458_Hybrid_USBDBUnopenable(t *testing.T) {
	e := newDaemonEnv458(t)
	e.mountPool(t, "cache", true)
	if err := os.WriteFile(e.dbPath, []byte("garbage, definitely not a sqlite file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t); err == nil {
		t.Fatal("expected error for unusable USB database")
	}
}

func TestDaemon458_OpenDBError(t *testing.T) {
	e := newDaemonEnv458(t)
	// The DB path is a directory: db.Open must fail.
	if err := os.MkdirAll(e.dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t); err == nil {
		t.Fatal("expected db.Open error")
	}
}

func TestDaemon458_ServerKeyError(t *testing.T) {
	e := newDaemonEnv458(t)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(e.dbPath), "vault.key"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := e.run(t)
	if err == nil || !strings.Contains(err.Error(), "server key") {
		t.Fatalf("err = %v, want server key error", err)
	}
}

func TestDetectBootKind458(t *testing.T) {
	prev := bootKindMountInfo
	t.Cleanup(func() { bootKindMountInfo = prev })
	dir := t.TempDir()
	cases := map[string]string{
		"36 35 98:0 / /mnt rw - xfs /dev/md1 rw\n36 35 98:0 / /boot rw - vfat /dev/sda1 rw\n": "flash",
		"1 0 0:1 / /boot rw - zfs boot/root rw\n":                                             "internal",
		"1 0 0:1 / /boot rw - fuse x rw\n":                                                    "unknown",
		"1 0 0:1 / /boot rw shared:1\n":                                                       "unknown",
		"1 0 0:1 / /mnt rw - xfs x rw\n":                                                      "unknown",
	}
	i := 0
	for content, want := range cases {
		i++
		p := filepath.Join(dir, "mi"+strconv.Itoa(i))
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		bootKindMountInfo = p
		if got := detectBootKind(); got != want {
			t.Errorf("detectBootKind(%q) = %q, want %q", content, got, want)
		}
	}
	bootKindMountInfo = filepath.Join(dir, "absent")
	if got := detectBootKind(); got != "unknown" {
		t.Errorf("missing mountinfo = %q, want unknown", got)
	}
}

func TestMountinfoHelpers458(t *testing.T) {
	lines := splitLines([]byte("a\n\nb\nc"))
	if strings.Join(lines, ",") != "a,b,c" {
		t.Errorf("splitLines = %v", lines)
	}
	line := "36 35 98:0 / /boot rw,noatime shared:1 - vfat /dev/sda1 rw"
	if indexOfMountPoint(line, "/boot") != 4 {
		t.Error("indexOfMountPoint /boot")
	}
	if indexOfMountPoint(line, "/mnt") != -1 || indexOfMountPoint("a b", "/boot") != -1 {
		t.Error("indexOfMountPoint negative cases")
	}
	if fstypeAfterSep(line) != "vfat" || fstypeAfterSep("a b c") != "" {
		t.Error("fstypeAfterSep")
	}
	many := strings.Repeat("x ", 80)
	if n := len(splitFields(many)); n != 64 {
		t.Errorf("splitFields cap = %d, want 64", n)
	}
	if f := splitFields("a\tb  c"); len(f) != 3 {
		t.Errorf("splitFields = %v", f)
	}
}

func TestCopyFile458_Errors(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dst")); err == nil {
		t.Error("expected open error")
	}
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, filepath.Join(dir, "nodir", "dst")); err == nil {
		t.Error("expected create error")
	}
	// Reading a directory fails inside io.Copy.
	if err := copyFile(dir, filepath.Join(dir, "dst2")); err == nil {
		t.Error("expected copy error")
	}
}

func TestRestoreUSBPrimaryFromBackup458_Errors(t *testing.T) {
	dir := t.TempDir()
	if err := restoreUSBPrimaryFromBackup(filepath.Join(dir, "missing"), filepath.Join(dir, "vault.db")); err == nil {
		t.Error("expected copy error")
	}
	src := filepath.Join(dir, "backup.db")
	seedDB458(t, src, nil)
	// Destination is a non-empty directory: the final rename fails.
	dst := filepath.Join(dir, "dstdir")
	if err := os.MkdirAll(filepath.Join(dst, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restoreUSBPrimaryFromBackup(src, dst); err == nil {
		t.Error("expected rename error")
	}
}

func TestRetireUSBLiveDB458_RenameFails(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "vault.db")
	if err := os.WriteFile(live, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the rename target makes os.Rename fail.
	if err := os.MkdirAll(filepath.Join(live+".migrated", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	retireUSBLiveDB(live)
	if _, err := os.Stat(live); err != nil {
		t.Errorf("live DB should remain after failed rename: %v", err)
	}
	retireUSBLiveDB(filepath.Join(dir, "absent.db")) // no-op
}

func TestApplyLogLevel458_Nil(t *testing.T) {
	applyLogLevel(nil)
}

func TestDaemon458_StartupMaintenanceWithConfiguration(t *testing.T) {
	e := newDaemonEnv458(t)
	d, err := db.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"` + e.dir + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "failed", "success"} {
		if _, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	// Backdate terminal runs so failed-run pruning and history purge both fire.
	if _, err := d.Exec(`UPDATE job_runs SET completed_at = datetime('now', '-400 days') WHERE status != 'running'`); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("history_retention_days", "100"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	_ = e.run(t)

	d2, err := db.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	var n int
	if err := d2.QueryRow(`SELECT COUNT(*) FROM job_runs WHERE status='running'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("stale running runs = %d, want 0", n)
	}
}
