package runner

import (
	"context"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// dedupDestForGC returns a dedup-enabled local destination. The repo is never
// initialised — RunDedupGC will fail at OpenRepo — which is fine for these
// tests: they assert *when* GC is allowed to proceed, not what it does.
func dedupDestForGC(t *testing.T, database *db.DB, dir string) db.StorageDestination {
	t.Helper()
	dest := createLocalDest(t, database, dir)
	dest.DedupEnabled = true
	return dest
}

// TestRunDedupGC_SerialisesWithRunSlot pins the #165 contract: GC must not
// touch the dedup repo while a backup/restore holds the run-slot mutex. An
// in-flight run registers packs immediately but its restore point (which makes
// them reachable) lands only at the end — an unserialised GC would sweep them.
func TestRunDedupGC_SerialisesWithRunSlot(t *testing.T) {
	r, database, dir := setupTestRunner(t)
	dest := dedupDestForGC(t, database, dir)

	r.mu.Lock() // simulate an in-flight backup holding the run slot
	done := make(chan struct{})
	go func() {
		r.RunDedupGC(dest, "test-serialise")
		close(done)
	}()

	select {
	case <-done:
		r.mu.Unlock()
		t.Fatal("RunDedupGC completed while the run-slot mutex was held — GC is not serialised against runs (#165)")
	case <-time.After(150 * time.Millisecond):
		// Still blocked behind the run slot — correct.
	}

	r.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunDedupGC did not proceed after the run slot was released")
	}
}

// TestRunDedupGC_RefusesWhileDraining pins the shutdown half of #165: once the
// runner is draining, GC must refuse to start instead of queuing behind the
// lock and running mid-shutdown.
func TestRunDedupGC_RefusesWhileDraining(t *testing.T) {
	r, database, dir := setupTestRunner(t)
	dest := dedupDestForGC(t, database, dir)

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// Hold the run slot: a refused GC returns immediately without ever
	// contending for it; a non-refusing GC would block here forever.
	r.mu.Lock()
	defer r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.RunDedupGC(dest, "test-draining")
		close(done)
	}()
	select {
	case <-done:
		// Refused before touching the run slot — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("RunDedupGC blocked while draining — shouldRefuseStart is not checked (#165)")
	}
}

// TestRunDedupGC_CountsAsActiveForDrain verifies GC participates in the drain
// protocol: Drain must wait for an in-progress GC the same way it waits for a
// backup run.
func TestRunDedupGC_CountsAsActiveForDrain(t *testing.T) {
	r, database, dir := setupTestRunner(t)
	dest := dedupDestForGC(t, database, dir)

	// Hold the run slot so the GC goroutine is parked inside markStart'd
	// territory (markStart happens before the mutex acquisition).
	r.mu.Lock()
	started := make(chan struct{})
	go func() {
		close(started)
		r.RunDedupGC(dest, "test-drain-wait")
	}()
	<-started
	// Give the goroutine time to pass markStart and block on r.mu.
	time.Sleep(100 * time.Millisecond)

	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		drainDone <- r.Drain(ctx)
	}()

	select {
	case err := <-drainDone:
		r.mu.Unlock()
		t.Fatalf("Drain returned (%v) while GC was still active — GC not registered with drain protocol (#165)", err)
	case <-time.After(150 * time.Millisecond):
		// Drain is correctly waiting on the active GC.
	}

	r.mu.Unlock() // let GC finish
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not complete after GC finished")
	}
}
