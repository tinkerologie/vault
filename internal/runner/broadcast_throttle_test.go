package runner

import (
	"testing"
	"time"
)

// TestBroadcastThrottleCollapsesPerFileUpdates covers the progress-flood half
// of issue #256: the chunked folder walk calls its progress callback once per
// file (46,000 times for the reported backup), each admitted call costing a
// JSON marshal plus a hub fan-out. The throttle must collapse a burst into a
// single broadcast.
func TestBroadcastThrottleCollapsesPerFileUpdates(t *testing.T) {
	admit := newBroadcastThrottle()

	var emitted int
	for i := 0; i < 46000; i++ {
		if admit(-1) { // the folder walk reports percent = -1 per file
			emitted++
		}
	}
	if emitted != 1 {
		t.Fatalf("burst of 46000 callbacks emitted %d broadcasts, want 1", emitted)
	}
}

// TestBroadcastThrottleAlwaysEmitsTerminal ensures throttling never swallows
// a completion event, which would leave the UI stuck at the last partial
// update for the item.
func TestBroadcastThrottleAlwaysEmitsTerminal(t *testing.T) {
	admit := newBroadcastThrottle()

	admit(-1) // consume the leading allowance
	for i := 0; i < 3; i++ {
		if !admit(100) {
			t.Fatalf("terminal update %d was throttled", i)
		}
	}
}

// TestBroadcastThrottleReopensAfterInterval confirms updates resume once the
// cadence window has passed, so a long-running item keeps reporting.
func TestBroadcastThrottleReopensAfterInterval(t *testing.T) {
	admit := newBroadcastThrottle()

	if !admit(-1) {
		t.Fatal("first call should always be admitted")
	}
	if admit(-1) {
		t.Fatal("second immediate call should be throttled")
	}
	time.Sleep(1100 * time.Millisecond)
	if !admit(-1) {
		t.Fatal("call after the interval should be admitted")
	}
}
