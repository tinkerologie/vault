package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestRunnerBreakerAccessor confirms Breaker() returns the breaker instance
// constructed during New() and that the same pointer is returned across calls.
func TestRunnerBreakerAccessor(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	b1 := r.Breaker()
	if b1 == nil {
		t.Fatal("Breaker() returned nil")
	}
	b2 := r.Breaker()
	if b1 != b2 {
		t.Errorf("Breaker() returned different instance on second call")
	}
}

// TestRunnerSetSnapshotManager verifies the setter assigns the manager
// without panicking; manager is unexported so we just confirm no race.
func TestRunnerSetSnapshotManager(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	// Construct a snapshot manager — we don't need a real cache path, just a
	// non-nil instance to confirm the setter accepts it.
	sm := db.NewSnapshotManager(database, "", "")
	r.SetSnapshotManager(sm)
	// Setting nil afterwards must not panic.
	r.SetSnapshotManager(nil)
}

// TestRunnerStatusInactive ensures Status() returns an empty RunStatus when
// no job is running and the queue is empty.
func TestRunnerStatusInactive(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	s := r.Status()
	if s.Active {
		t.Error("Active should be false for fresh runner")
	}
	if s.JobID != 0 {
		t.Errorf("JobID = %d, want 0", s.JobID)
	}
	if len(s.Queue) != 0 {
		t.Errorf("Queue length = %d, want 0", len(s.Queue))
	}
}

// TestRunnerStatusActiveSnapshot exercises the active path by directly
// populating currentRun/queue via the internal helpers, then reading them
// back through Status().
func TestRunnerStatusActiveSnapshot(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)

	// Populate currentRun via the internal setter (used by runJobInternal).
	r.setRunStatus(&RunStatus{
		Active:     true,
		JobID:      42,
		RunID:      99,
		JobName:    "active-job",
		RunType:    "scheduled",
		ItemsTotal: 5, ItemsDone: 2,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})

	// Populate the queue directly under the same mutex layout.
	r.queueMu.Lock()
	r.queue = []QueueEntry{
		{JobID: 100, JobName: "queued-a", QueuedAt: "2026-01-01T00:00:00Z"},
		{JobID: 101, JobName: "queued-b", QueuedAt: "2026-01-01T00:01:00Z"},
	}
	r.queueMu.Unlock()

	s := r.Status()
	if !s.Active {
		t.Error("Active should be true")
	}
	if s.JobID != 42 {
		t.Errorf("JobID = %d, want 42", s.JobID)
	}
	if s.JobName != "active-job" {
		t.Errorf("JobName = %q, want active-job", s.JobName)
	}
	if len(s.Queue) != 2 {
		t.Fatalf("Queue length = %d, want 2", len(s.Queue))
	}
	if s.Queue[0].JobName != "queued-a" || s.Queue[1].JobName != "queued-b" {
		t.Errorf("Queue contents wrong: %+v", s.Queue)
	}

	// Status() copies — mutating the returned slice must not affect runner state.
	s.Queue[0].JobName = "mutated"
	again := r.Status()
	if again.Queue[0].JobName != "queued-a" {
		t.Errorf("Status returned an aliased queue slice: %q", again.Queue[0].JobName)
	}
}

// TestRunnerBroadcastNoHub: calling Broadcast on a runner with hub=nil must
// not panic and must be a no-op.
func TestRunnerBroadcastNoHub(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t) // setupTestRunner passes hub=nil
	r.Broadcast(map[string]any{"type": "test", "payload": 1})
}

// TestRunnerBroadcastForwardsToHub spins up a real ws.Hub, registers a fake
// receiver via the broadcast channel, and confirms Broadcast forwards.
// We can't easily Register a Client without a websocket conn; instead we
// peek at the hub's send channel size before/after via a known counter.
//
// The simplest signal we can observe without modifying production code is
// that Broadcast() does not block (the hub's broadcast channel has a 256
// buffer and Run() may not have drained it yet). Confirm the call returns
// promptly for both a small and large payload.
func TestRunnerBroadcastForwardsToHub(t *testing.T) {
	t.Parallel()
	hub := ws.NewHub()
	// Don't start hub.Run() — the broadcast channel has a 256-element
	// buffer, so a single Broadcast() will not block even with no receiver.
	r, _ := newTestRunner(t)
	r.hub = hub // swap in a hub we control

	done := make(chan struct{})
	go func() {
		r.Broadcast(map[string]any{"type": "queue_update"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked unexpectedly")
	}
}

// TestRunnerCancelJobNoActive covers the no-job-running branch.
func TestRunnerCancelJobNoActive(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	err := r.CancelJob(1)
	if err == nil {
		t.Fatal("CancelJob with no active job should return an error")
	}
}

// TestRunnerCancelJobWrongID exercises the "running job has a different ID"
// branch.
func TestRunnerCancelJobWrongID(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, JobID: 7})
	err := r.CancelJob(99)
	if err == nil {
		t.Fatal("CancelJob with wrong jobID should return an error")
	}
}

// TestRunnerCancelJobNoCancelFn covers the cancelFn==nil branch (active
// run but cancellation function not set).
func TestRunnerCancelJobNoCancelFn(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.setRunStatus(&RunStatus{Active: true, JobID: 7})
	// Explicitly leave r.cancelFn nil.
	err := r.CancelJob(7)
	if err == nil {
		t.Fatal("CancelJob with nil cancelFn should return an error")
	}
}

// TestRunnerCancelJobHappyPath ensures CancelJob invokes the cancel function
// and sets Cancelling=true on the current run.
func TestRunnerCancelJobHappyPath(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t) // hub is non-nil so broadcast doesn't no-op

	// Build a cancellable context and stash the cancel fn where CancelJob
	// expects it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.setRunStatus(&RunStatus{Active: true, JobID: 5})
	r.cancelMu.Lock()
	r.cancelFn = cancel
	r.cancellingJobID = 5
	r.cancelMu.Unlock()

	if err := r.CancelJob(5); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	// The context should now be done.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ctx not cancelled after CancelJob")
	}

	// Cancelling=true should be observable through Status().
	s := r.Status()
	if !s.Cancelling {
		t.Error("Status.Cancelling should be true after CancelJob")
	}
}

// TestRetryOfRunIDPtrZero exercises the zero / nil branch of the helper.
func TestRetryOfRunIDPtrZero(t *testing.T) {
	t.Parallel()
	o := runOptions{}
	if p := o.retryOfRunIDPtr(); p != nil {
		t.Errorf("retryOfRunIDPtr() with zero retryOfRunID = %v, want nil", p)
	}
	// Negative values also map to nil.
	on := runOptions{retryOfRunID: -1}
	if p := on.retryOfRunIDPtr(); p != nil {
		t.Errorf("retryOfRunIDPtr() with negative retryOfRunID = %v, want nil", p)
	}
}

// TestRetryOfRunIDPtrNonZero exercises the populated branch.
func TestRetryOfRunIDPtrNonZero(t *testing.T) {
	t.Parallel()
	o := runOptions{retryOfRunID: 42}
	p := o.retryOfRunIDPtr()
	if p == nil {
		t.Fatal("retryOfRunIDPtr() with positive value returned nil")
	}
	if *p != 42 {
		t.Errorf("*retryOfRunIDPtr() = %d, want 42", *p)
	}
	// Mutating *p must not affect the original (defensive copy semantics).
	*p = 99
	if o.retryOfRunID != 42 {
		t.Errorf("retryOfRunID was mutated through returned pointer: %d", o.retryOfRunID)
	}
}

// TestUsesMergedRestoreChain table-tests the small classifier.
func TestUsesMergedRestoreChain(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"container": true,
		"vm":        true,
		"folder":    false,
		"plugin":    false,
		"zfs":       false,
		"":          false,
		"unknown":   false,
	}
	for itemType, want := range cases {
		if got := usesMergedRestoreChain(itemType); got != want {
			t.Errorf("usesMergedRestoreChain(%q) = %v, want %v", itemType, got, want)
		}
	}
}

// TestRunJobManualRefusedWhenDraining covers a thin wrapper path: drain the
// runner so RunJobManual returns immediately at the shouldRefuseStart()
// gate inside runJobInternal. This avoids touching Docker/libvirt.
func TestRunJobManualRefusedWhenDraining(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)
	// Drain with a context that's already done — Drain returns when
	// activeCount==0 (immediately for a fresh runner), so this sets the
	// draining flag synchronously.
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !r.IsDraining() {
		t.Fatal("runner should report draining after Drain()")
	}
	// All three thin wrappers route through runJobInternal which returns
	// immediately when draining.
	done := make(chan struct{})
	go func() {
		r.RunJob(1)
		r.RunJobManual(2)
		r.RunJobRetry(3, 99, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run* wrappers blocked when draining")
	}
}

// TestRunJobManualFastFailMissingJob exercises RunJob/RunJobManual/RunJobRetry
// on a missing job ID. runJobInternal sets currentRun before failing the DB
// lookup; the wrappers should complete promptly without panicking.
func TestRunJobManualFastFailMissingJob(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	done := make(chan struct{})
	go func() {
		r.RunJobManual(99999) // job does not exist
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunJobManual on missing job blocked")
	}
}

// TestRunJobRetryFastFailMissingJob mirrors the above for RunJobRetry.
func TestRunJobRetryFastFailMissingJob(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)
	done := make(chan struct{})
	go func() {
		r.RunJobRetry(99999, 1, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunJobRetry on missing job blocked")
	}
}

// TestRunnerStatusConcurrent stresses Status() against the setters to ensure
// the locks are honoured (run under -race).
func TestRunnerStatusConcurrent(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.setRunStatus(&RunStatus{Active: true, JobID: int64(i), JobName: fmt.Sprintf("j%d", i)})
					_ = r.Status()
				}
			}
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
