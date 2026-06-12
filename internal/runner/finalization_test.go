package runner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunFinalizationStepFast verifies that a function completing quickly
// returns promptly — the helper must not add significant latency on the
// happy path — and that the step sees an un-cancelled context.
func TestRunFinalizationStepFast(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	var called int32
	var sawCancelled int32
	start := time.Now()
	r.runFinalizationStep("fast-step", 1, 100, 5*time.Second, func(ctx context.Context) {
		atomic.StoreInt32(&called, 1)
		if ctx.Err() != nil {
			atomic.StoreInt32(&sawCancelled, 1)
		}
	})
	elapsed := time.Since(start)

	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("runFinalizationStep: fast fn was not called")
	}
	if atomic.LoadInt32(&sawCancelled) != 0 {
		t.Fatal("runFinalizationStep: fast fn saw a cancelled context before its timeout elapsed")
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
	r.runFinalizationStep("slow-step", 2, 200, stepTimeout, func(context.Context) {
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

// TestRunFinalizationStepCancelsHungStep verifies the timed-out step's
// context is cancelled so a ctx-aware step exits instead of leaking its
// goroutine forever (issue #112 follow-up: previously the goroutine was
// abandoned with no cancellation signal).
func TestRunFinalizationStepCancelsHungStep(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	released := make(chan struct{})
	start := time.Now()
	r.runFinalizationStep("hung-step", 3, 300, 50*time.Millisecond, func(ctx context.Context) {
		<-ctx.Done() // a step that only exits when cancelled
		close(released)
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runFinalizationStep blocked for %v, want prompt return after the 50ms timeout", elapsed)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("hung step's context was never cancelled; its goroutine would leak")
	}
}
