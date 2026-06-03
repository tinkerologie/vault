package runner

import (
	"context"
	"testing"
	"time"
)

func TestStallWatchdogCancelsOnNoProgress(t *testing.T) {
	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now().Add(-time.Hour) // already stale
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.startStallWatchdog(ctx, cancel, 1, 2*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond)

	select {
	case <-ctx.Done():
		// cancelled as expected
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel a stalled context")
	}
}

func TestStallWatchdogHeartbeatPreventsCancel(t *testing.T) {
	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now()
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.startStallWatchdog(ctx, cancel, 1, 2*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond)

	// Heartbeat for ~120ms, longer than cancelAfter, to prove fresh progress
	// keeps the context alive.
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()
		if ctx.Err() != nil {
			t.Fatal("watchdog cancelled despite active heartbeat")
		}
		time.Sleep(3 * time.Millisecond)
	}
}
