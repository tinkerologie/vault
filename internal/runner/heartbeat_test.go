package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heartbeat")
	hb := NewHeartbeat(path, "test-v1", 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hb.Start(ctx)

	// Wait for first write.
	deadline := time.Now().Add(500 * time.Millisecond)
	var data []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatalf("heartbeat file never written")
	}
	if !strings.Contains(string(data), "test-v1") {
		t.Errorf("heartbeat missing version: %q", string(data))
	}

	// Capture mtime, wait for next tick, expect mtime to advance.
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if !fi2.ModTime().After(fi1.ModTime()) {
		t.Errorf("heartbeat mtime did not advance")
	}
}

func TestHeartbeatStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb")
	interval := 20 * time.Millisecond
	hb := NewHeartbeat(path, "v", interval)

	ctx, cancel := context.WithCancel(context.Background())
	hb.Start(ctx)

	// Make sure the ticker has actually fired at least once (mtime advanced
	// past the immediate first write) so we are genuinely testing that an
	// active writer stops — not a writer that never ran.
	first := hbWaitForWrite(t, path, 2*time.Second)
	if !hbWaitFor(2*time.Second, func() bool {
		m, err := hbMtime(path)
		return err == nil && m.After(first)
	}) {
		t.Fatal("heartbeat ticker never advanced mtime before cancel")
	}
	cancel()

	// After cancel at most one in-flight write lands, then the goroutine exits.
	// Poll until the mtime has been quiet for several intervals — that proves
	// the writer stopped — then confirm it never advances again over a longer
	// window. No fixed sleep races the ticker, so this is deterministic under
	// load (the previous version flaked when a post-cancel tick landed in the
	// fixed observation gap).
	settled := hbWaitForSettle(t, path, 6*interval, 2*time.Second)
	time.Sleep(8 * interval)
	final, err := hbMtime(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if final.After(settled) {
		t.Errorf("heartbeat continued after cancel: mtime advanced from %v to %v", settled, final)
	}
}

// hbMtime returns the modification time of path.
func hbMtime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// hbWaitFor polls cond every 2ms until it returns true or timeout elapses.
func hbWaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// hbWaitForWrite waits until path exists and returns its mtime.
func hbWaitForWrite(t *testing.T, path string, timeout time.Duration) time.Time {
	t.Helper()
	if !hbWaitFor(timeout, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}) {
		t.Fatalf("heartbeat file %q never written within %v", path, timeout)
	}
	m, err := hbMtime(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return m
}

// hbWaitForSettle polls path's mtime until it stops changing for `quiet`
// (i.e. the writer has stopped) and returns the settled mtime. It fails the
// test if the mtime never settles within timeout.
func hbWaitForSettle(t *testing.T, path string, quiet, timeout time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last, err := hbMtime(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		cur, err := hbMtime(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if cur.After(last) {
			last, lastChange = cur, time.Now()
			continue
		}
		if time.Since(lastChange) >= quiet {
			return last
		}
	}
	t.Fatalf("heartbeat mtime never settled within %v", timeout)
	return time.Time{}
}
