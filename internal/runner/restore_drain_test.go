package runner

import (
	"context"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunRestore_RefusesWhileDraining pins the #172 contract: a draining
// daemon must not start a new restore. A refused restore returns without
// contending for the run slot (held here to prove it never gets that far).
func TestRunRestore_RefusesWhileDraining(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.RunRestore(db.RestorePoint{JobID: 1}, nil, "", "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRestore blocked while draining — shouldRefuseStart not checked (#172)")
	}
}

// TestRunRestore_CountsAsActiveForDrain verifies Drain waits for an
// in-flight restore the same way it waits for a backup run.
func TestRunRestore_CountsAsActiveForDrain(t *testing.T) {
	r, _, _ := setupTestRunner(t)

	r.mu.Lock() // park the restore inside markStart'd territory
	started := make(chan struct{})
	go func() {
		close(started)
		r.RunRestore(db.RestorePoint{JobID: 1}, nil, "", "")
	}()
	<-started
	time.Sleep(100 * time.Millisecond) // let it pass markStart and block on r.mu

	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		drainDone <- r.Drain(ctx)
	}()

	select {
	case err := <-drainDone:
		r.mu.Unlock()
		t.Fatalf("Drain returned (%v) while a restore was active — restores not in drain protocol (#172)", err)
	case <-time.After(150 * time.Millisecond):
	}

	r.mu.Unlock()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not complete after the restore finished")
	}
}
