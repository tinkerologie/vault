package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBackupToFileReservesPrivateLiteralPath(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.SetSetting("snapshot_fixture", "committed"); err != nil {
		t.Fatal(err)
	}
	filename := "snapshot #1?.db"
	if runtime.GOOS == "windows" {
		filename = "snapshot #1.db"
	}
	dest := filepath.Join(t.TempDir(), filename)
	if err := database.BackupToFile(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(dest) // #nosec G304 -- test-owned snapshot
	if err != nil {
		t.Fatal(err)
	}
	isolated := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(isolated, content, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(isolated)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	got, err := recovered.GetSetting("snapshot_fixture", "")
	if err != nil || got != "committed" {
		t.Fatalf("recovered setting = %q, error = %v", got, err)
	}
	for _, path := range []string{"", dest, database.Path()} {
		if err := database.BackupToFile(t.Context(), path); err == nil {
			t.Errorf("accepted existing or empty snapshot path %q", path)
		}
	}
}

func TestBackupToFileCleansUpWhenConnectionWaitIsCancelled(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dest := filepath.Join(t.TempDir(), "snapshot.db")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := database.BackupToFile(ctx, dest); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-cancelled backup returned %v", err)
	}
	waitCtx, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stop()
	if err := database.BackupToFile(waitCtx, dest); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection wait returned %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled backup left a snapshot, stat error = %v", err)
	}
}
