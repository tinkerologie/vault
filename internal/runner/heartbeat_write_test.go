package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHeartbeatWrite_MkdirAllFails drives the os.MkdirAll error branch
// inside (*Heartbeat).write. We point the heartbeat at a path whose
// parent component is an existing regular file — MkdirAll will fail
// with ENOTDIR.
func TestHeartbeatWrite_MkdirAllFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Pre-create a regular file where the heartbeat's parent dir would go.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("nope"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// path = blocker/heartbeat -> MkdirAll(blocker) fails because blocker is a file.
	hb := NewHeartbeat(filepath.Join(blocker, "heartbeat"), "v", 30*time.Second)
	// Suppress lastWarn rate-limit so warn() actually fires; not strictly
	// required for cov, but exercises the warn path too.
	hb.warnEvery = 0

	hb.write() // exercises the MkdirAll error branch.

	// Confirm no heartbeat file was written.
	if _, err := os.Stat(filepath.Join(blocker, "heartbeat")); err == nil {
		t.Error("expected no heartbeat file when MkdirAll fails")
	}
}

// TestHeartbeatWrite_WriteFileFails drives the os.WriteFile error branch.
// We point the heartbeat at a path whose final component is an existing
// directory — WriteFile will fail with EISDIR.
func TestHeartbeatWrite_WriteFileFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Make the heartbeat path itself a directory so os.WriteFile fails.
	hbPath := filepath.Join(dir, "hb-as-dir")
	if err := os.MkdirAll(hbPath, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	hb := NewHeartbeat(hbPath, "v", 30*time.Second)
	hb.warnEvery = 0
	hb.write() // exercises the WriteFile error branch.

	// The directory we created is still present (write didn't replace it).
	info, err := os.Stat(hbPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected heartbeat path to remain a directory after write failure")
	}
}
