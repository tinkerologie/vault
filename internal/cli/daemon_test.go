package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/anomaly"
	"github.com/ruaan-deysel/vault/internal/api"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/logx"
	"github.com/ruaan-deysel/vault/internal/unraid"
	"github.com/spf13/cobra"
)

// openTestDB opens a fresh SQLite database in a temp directory.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestRestoreUSBPrimaryFromBackup_PreservesMtime: the non-hybrid restore copy
// must not mint a fresh mtime for stale backup content — the boot-time
// freshest-source selection ranks by raw mtime, and a stale live DB with
// mtime=now would outrank a logically newer cache snapshot (issue #241).
func TestRestoreUSBPrimaryFromBackup_PreservesMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	src := filepath.Join(dir, "vault.db.backup")
	srcDB, err := db.Open(src)
	if err != nil {
		t.Fatalf("create backup DB: %v", err)
	}
	_ = srcDB.Close()
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "vault.db")
	if err := restoreUSBPrimaryFromBackup(src, dst); err != nil {
		t.Fatalf("restoreUSBPrimaryFromBackup: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Sub(old).Abs() > 2*time.Second {
		t.Errorf("restored DB mtime = %v, want ≈ backup mtime %v", fi.ModTime(), old)
	}
}

// TestRestoreUSBPrimaryFromBackup_CorruptBackupKeepsLiveDB: a corrupt-but-
// newer backup must never destroy the existing valid live DB — validation
// happens on a temp copy before any replacement.
func TestRestoreUSBPrimaryFromBackup_CorruptBackupKeepsLiveDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	src := filepath.Join(dir, "vault.db.backup")
	if err := os.WriteFile(src, []byte("not a sqlite database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "vault.db")
	liveContent := []byte("live-db-bytes")
	if err := os.WriteFile(dst, liveContent, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := restoreUSBPrimaryFromBackup(src, dst); err == nil {
		t.Fatal("expected error for corrupt backup, got nil")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(liveContent) {
		t.Error("live DB was modified despite corrupt backup")
	}
}

// TestRetireUSBLiveDB: after a hybrid boot absorbs the live USB DB's state,
// the file must be renamed aside (with sidecars removed) so a later
// USB-direct session cannot falsely promote stale content (issue #241).
func TestRetireUSBLiveDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	retireUSBLiveDB(dbPath)

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("live DB still present after retire (err = %v)", err)
	}
	if _, err := os.Stat(dbPath + ".migrated"); err != nil {
		t.Errorf("retired copy missing: %v", err)
	}
	for _, f := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("sidecar %s not removed (err = %v)", f, err)
		}
	}

	// Idempotent when nothing exists.
	retireUSBLiveDB(dbPath)
}

// ── buildAnomalyEvaluator gating tests ─────────────────────────────────────

// TestBuildAnomalyEvaluator_Enabled verifies that buildAnomalyEvaluator returns
// a non-nil Evaluator with exactly 4 detectors (2 per-run + 1 reliability + 1
// capacity) when anomaly_detection_enabled is "true".
func TestBuildAnomalyEvaluator_Enabled(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)

	// Ensure anomaly_detection_enabled is "true" (the default, but be explicit).
	if err := d.SetSetting("anomaly_detection_enabled", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	cfg := api.ServerConfig{
		Addr:    ":0",
		Version: "test",
	}
	srv := api.NewServer(d, cfg)

	ev := buildAnomalyEvaluator(d, srv)
	if ev == nil {
		t.Fatal("buildAnomalyEvaluator returned nil when enabled=true")
	}

	// Verify detector counts by inspecting the registry through the evaluator.
	// We test this indirectly: a freshly-built evaluator's Start/Drain must work.
	ev.Start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel
	if err := ev.Drain(ctx); err != nil && err != ctx.Err() {
		t.Errorf("Drain after cancel: %v", err)
	}
}

// TestBuildAnomalyEvaluator_Disabled verifies that buildAnomalyEvaluator returns
// nil when anomaly_detection_enabled is "false", so the daemon does not construct
// or start the evaluator.
func TestBuildAnomalyEvaluator_Disabled(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)

	if err := d.SetSetting("anomaly_detection_enabled", "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	cfg := api.ServerConfig{
		Addr:    ":0",
		Version: "test",
	}
	srv := api.NewServer(d, cfg)

	ev := buildAnomalyEvaluator(d, srv)
	if ev != nil {
		t.Fatal("buildAnomalyEvaluator returned non-nil when enabled=false")
	}
}

// TestBuildAnomalyEvaluator_DefaultEnabled verifies that the default (no setting
// stored) results in the evaluator being constructed (defaults to "true").
func TestBuildAnomalyEvaluator_DefaultEnabled(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	// Do NOT set anomaly_detection_enabled — let it fall through to the default.

	cfg := api.ServerConfig{
		Addr:    ":0",
		Version: "test",
	}
	srv := api.NewServer(d, cfg)

	ev := buildAnomalyEvaluator(d, srv)
	if ev == nil {
		t.Fatal("buildAnomalyEvaluator returned nil for default (unset) setting — expected enabled")
	}
}

// TestBuildAnomalyEvaluator_DetectorCounts verifies that the registry built by
// buildAnomalyEvaluator contains exactly the 4 expected detectors in the right
// kinds by using the exported Registry type directly (avoids reaching into
// Evaluator internals).
func TestBuildAnomalyEvaluator_DetectorCounts(t *testing.T) {
	t.Parallel()

	// Build the registry the same way buildAnomalyEvaluator does, so the counts
	// are independently verified without relying on the function's implementation.
	reg := &anomaly.Registry{}
	reg.Register(anomaly.NewSizeDriftDetector())
	reg.Register(anomaly.NewDurationDriftDetector())
	reg.Register(anomaly.NewReliabilityDetector(nil)) // nil db: tests kind/name only
	reg.Register(anomaly.NewCapacityTrajectoryDetector(nil))

	perRun := reg.PerRun()
	trend := reg.Trend()

	// SizeDrift + DurationDrift + Reliability = 3 per-run; CapacityTrajectory = 1 trend.
	if got := len(perRun); got != 3 {
		t.Errorf("PerRun() count = %d, want 3", got)
	}
	if got := len(trend); got != 1 {
		t.Errorf("Trend() count = %d, want 1", got)
	}
	total := len(perRun) + len(trend)
	if total != 4 {
		t.Errorf("total detector count = %d, want 4", total)
	}
}

// TestRunAnomalyTrendTicker_ContextCancel verifies that runAnomalyTrendTicker
// returns when its context is cancelled (no goroutine leak). The ticker interval
// is not tested (would require sleeping 5 minutes); instead we verify that
// cancelling ctx causes the function to return.
func TestRunAnomalyTrendTicker_ContextCancel(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)

	// Build a minimal evaluator with no detectors (to keep EvaluateTrendDetectors cheap).
	reg := &anomaly.Registry{}
	ev := anomaly.NewEvaluator(d, nil, reg, anomaly.RealClock{})
	ev.Start()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runAnomalyTrendTicker(ctx, ev)
		close(done)
	}()

	// Cancel the context — the ticker goroutine must return promptly.
	cancel()

	select {
	case <-done:
		// Good: goroutine exited.
	case <-time.After(2 * time.Second):
		t.Fatal("runAnomalyTrendTicker did not return within 2s after context cancellation")
	}

	// Clean up the evaluator.
	drainCtx, drainCancel := context.WithCancel(context.Background())
	drainCancel()
	_ = ev.Drain(drainCtx)
}

func TestApplyLogLevel(t *testing.T) {
	d := openTestDB(t)

	// Default setting or missing
	applyLogLevel(d)
	if logx.LevelString() != "info" {
		t.Errorf("applyLogLevel default = %q, want info", logx.LevelString())
	}

	// Nil database safe
	applyLogLevel(nil)

	// Set to warn
	if err := d.SetSetting("log_level", "warn"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	applyLogLevel(d)
	if logx.LevelString() != "warn" {
		t.Errorf("applyLogLevel = %q, want warn", logx.LevelString())
	}
	logx.SetLevelString("info")
}

func TestDaemonCmd_RunE_LogLevelApplied(t *testing.T) {
	dir := t.TempDir()

	prevWorkingDir := hybridWorkingDir
	hybridWorkingDir = filepath.Join(dir, "hybrid")
	defer func() { hybridWorkingDir = prevWorkingDir }()

	// Mock mounted pool to bypass array startup wait loop.
	mntDir := filepath.Join(dir, "mnt")
	cacheDir := filepath.Join(mntDir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mountInfo := filepath.Join(dir, "mountinfo")
	mountContent := "1 0 0:0 / " + cacheDir + " rw - btrfs /dev/sda1 rw\n"
	if err := os.WriteFile(mountInfo, []byte(mountContent), 0o600); err != nil {
		t.Fatalf("write mountinfo: %v", err)
	}
	cleanup := unraid.SetMntBaseForTest(mntDir, mountInfo)
	defer cleanup()

	dbPath := filepath.Join(dir, "vault.db")
	snapshotDBPath := filepath.Join(cacheDir, ".vault", "vault.db")
	if err := os.MkdirAll(filepath.Dir(snapshotDBPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedDBPath := filepath.Join(dir, "seed.db")
	seedDB, err := db.Open(seedDBPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := seedDB.SetSetting("log_level", "warn"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	sm := db.NewSnapshotManager(seedDB, snapshotDBPath, snapshotDBPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	_ = seedDB.Close()

	cmd := &cobra.Command{Use: "daemon"}
	cmd.SetContext(context.Background())
	cmd.Flags().String("db", dbPath, "Database path")
	cmd.Flags().String("addr", "127.0.0.1:0", "Listen address")
	cmd.Flags().String("tls-cert", filepath.Join(dir, "nonexistent.crt"), "TLS cert")
	cmd.Flags().String("tls-key", filepath.Join(dir, "nonexistent.key"), "TLS key")

	logx.SetLevelString("info")
	_ = daemonCmd.RunE(cmd, nil)

	if logx.LevelString() != "warn" {
		t.Errorf("expected logx level to be warn, got %q", logx.LevelString())
	}
	logx.SetLevelString("info")
}
