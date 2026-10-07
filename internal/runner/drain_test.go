package runner

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newDrainRunner constructs a minimal Runner with the drain primitives
// initialised. Avoids the full runner.New() machinery (DB, hub, etc.)
// because we only exercise activeMu/activeCond/draining here.
func newDrainRunner() *Runner {
	r := &Runner{}
	r.activeCond = sync.NewCond(&r.activeMu)
	return r
}

func TestDrainWaitsForActiveJob(t *testing.T) {
	r := newDrainRunner()
	// Simulate an active job.
	r.markStart()

	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		drainDone <- r.Drain(ctx)
	}()

	// Simulate job finishing.
	time.Sleep(50 * time.Millisecond)
	r.markFinish()

	err := <-drainDone
	if err != nil {
		t.Errorf("expected nil from drain, got %v", err)
	}
	if !r.IsDraining() {
		t.Errorf("Drain should leave runner in draining state")
	}
}

func TestDrainTimesOut(t *testing.T) {
	r := newDrainRunner()
	r.markStart() // Active job never finishes.

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := r.Drain(ctx)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	// Drain's own waiter goroutine must exit once it times out. Count only
	// goroutines running inside Drain: a process-wide count also sees
	// background work left by earlier tests and flakes.
	deadline := time.Now().Add(time.Second)
	for drainGoroutines() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: %d goroutine(s) still inside Runner.Drain", drainGoroutines())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drainGoroutines counts goroutines whose stack is inside Runner.Drain.
func drainGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "runner.(*Runner).Drain") {
			n++
		}
	}
	return n
}

func TestRunJobRefusedWhileDraining(t *testing.T) {
	r := newDrainRunner()
	// Manually set draining without calling Drain (so we don't need a real ctx).
	r.activeMu.Lock()
	r.draining = true
	r.activeMu.Unlock()

	if !r.shouldRefuseStart() {
		t.Errorf("shouldRefuseStart should be true while draining")
	}
}

func TestDrainIsIdempotent(t *testing.T) {
	r := newDrainRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := r.Drain(ctx); err != nil {
		t.Errorf("first drain: %v", err)
	}
	// Second drain should also return cleanly (no active jobs).
	if err := r.Drain(context.Background()); err != nil {
		t.Errorf("second drain: %v", err)
	}
}
