package runner

import (
	"errors"
	"testing"
	"time"
)

// TestHeartbeatWarnFirstLogs verifies that the first warn call emits a log
// line (no rate-limit suppression yet because lastWarn is zero) and that
// the lastWarn timestamp gets stamped.
func TestHeartbeatWarnFirstLogs(t *testing.T) {
	t.Parallel()
	hb := NewHeartbeat("/tmp/nowhere/heartbeat", "v", 30*time.Second)
	before := hb.lastWarn

	hb.warn("op", errors.New("synthetic"))

	if !hb.lastWarn.After(before) {
		t.Errorf("warn() did not advance lastWarn (before=%v after=%v)", before, hb.lastWarn)
	}
}

// TestHeartbeatWarnRateLimited verifies that a second warn() call inside
// the warnEvery window is suppressed (lastWarn is not advanced again).
func TestHeartbeatWarnRateLimited(t *testing.T) {
	t.Parallel()
	hb := NewHeartbeat("/tmp/nowhere/heartbeat", "v", 30*time.Second)
	hb.warnEvery = 1 * time.Hour

	hb.warn("op1", errors.New("first"))
	first := hb.lastWarn

	hb.warn("op2", errors.New("second"))
	if !hb.lastWarn.Equal(first) {
		t.Errorf("second warn within window should have been suppressed; lastWarn changed %v -> %v", first, hb.lastWarn)
	}
}

// TestHeartbeatWarnReEmitsAfterWindow verifies that once the rate-limit
// window has passed, a subsequent warn call logs again and stamps a fresh
// timestamp.
func TestHeartbeatWarnReEmitsAfterWindow(t *testing.T) {
	t.Parallel()
	hb := NewHeartbeat("/tmp/nowhere/heartbeat", "v", 30*time.Second)
	// Tighten the suppression window so the test is fast.
	hb.warnEvery = 1 * time.Millisecond

	hb.warn("op1", errors.New("first"))
	first := hb.lastWarn

	time.Sleep(5 * time.Millisecond)
	hb.warn("op2", errors.New("second"))
	if !hb.lastWarn.After(first) {
		t.Errorf("after warnEvery window, lastWarn should re-advance (%v -> %v)", first, hb.lastWarn)
	}
}

// TestNewHeartbeatDefaultInterval exercises the interval<=0 default branch.
func TestNewHeartbeatDefaultInterval(t *testing.T) {
	t.Parallel()
	hb := NewHeartbeat("/tmp/x", "v", 0)
	if hb.interval != 30*time.Second {
		t.Errorf("default interval = %v, want 30s", hb.interval)
	}
	hb2 := NewHeartbeat("/tmp/x", "v", -1*time.Second)
	if hb2.interval != 30*time.Second {
		t.Errorf("negative interval should default to 30s, got %v", hb2.interval)
	}
}
