package runner

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestRunFinalizationStepFast verifies that a function completing quickly
// returns promptly — the helper must not add significant latency on the
// happy path.
func TestRunFinalizationStepFast(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	var called int32
	start := time.Now()
	r.runFinalizationStep("fast-step", 1, 100, 5*time.Second, func() {
		atomic.StoreInt32(&called, 1)
	})
	elapsed := time.Since(start)

	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("runFinalizationStep: fast fn was not called")
	}
	// Should return well under 1s for a no-op fn.
	const maxElapsed = 1 * time.Second
	if elapsed > maxElapsed {
		t.Errorf("runFinalizationStep fast path took %v (want < %v)", elapsed, maxElapsed)
	}
}

// TestRunFinalizationStepTimeout verifies that when a fn blocks longer than
// the timeout, runFinalizationStep returns approximately at the timeout and
// does NOT block indefinitely. This directly tests the issue #112 fix:
// a stuck finalization step must not hold the run-slot mutex forever.
func TestRunFinalizationStepTimeout(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	// blocked is closed to unblock the goroutine at test cleanup so it does
	// not leak after the test.
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	const stepTimeout = 50 * time.Millisecond
	start := time.Now()
	r.runFinalizationStep("slow-step", 2, 200, stepTimeout, func() {
		// Block until the test is done (simulates a hung finalization op).
		<-blocked
	})
	elapsed := time.Since(start)

	// Must return significantly faster than the 5-second step duration.
	// Allow generous headroom (500ms) for scheduler jitter.
	const maxElapsed = 500 * time.Millisecond
	if elapsed > maxElapsed {
		t.Errorf("runFinalizationStep did not respect timeout: elapsed %v (want < %v)", elapsed, maxElapsed)
	}
	// Also confirm it took at least roughly the timeout (not returning early).
	if elapsed < stepTimeout/2 {
		t.Errorf("runFinalizationStep returned suspiciously fast: %v (timeout was %v)", elapsed, stepTimeout)
	}
}
