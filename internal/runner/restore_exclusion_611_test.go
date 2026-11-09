package runner

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

// overlapRecorder stands in for the restore engine and for a tracked task,
// recording how many run at once.
type overlapRecorder struct {
	inFlight, peak atomic.Int32
}

func (o *overlapRecorder) work() {
	n := o.inFlight.Add(1)
	for {
		p := o.peak.Load()
		if n <= p || o.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	o.inFlight.Add(-1)
}

// Issue #611: an untracked (MCP) restore takes the same runner exclusion as a
// tracked backup, restore or maintenance task, so its restore steps never run
// alongside one; while the runner is held it is refused, not queued.
func TestUntrackedRestoresNeverOverlap(t *testing.T) {
	r, _ := newTestRunner(t)
	rec := &overlapRecorder{}
	r.untrackedRestoreFn = func(ctx context.Context, _ db.RestorePoint, _, _, _, _ string, _ ...RestoreItemOptions) error {
		engine.ApprovalsSettled(ctx)
		rec.work()
		return nil
	}
	// A tracked task (backup, RunRestore, GC, orphan delete) holds r.mu for
	// its whole run.
	tracked := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		rec.work()
	}

	var wg sync.WaitGroup
	var started, refused atomic.Int32
	count := func(err error) {
		switch {
		case err == nil:
			started.Add(1)
		case errors.Is(err, ErrRunnerBusy):
			refused.Add(1)
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	for i := range 30 {
		if i%5 == 0 {
			wg.Go(tracked)
		}
		wg.Go(func() { count(r.StartRestoreItem(t.Context(), db.RestorePoint{}, "web", "container", "", "")) })
		wg.Go(func() { count(r.RestoreItem(db.RestorePoint{}, "web", "container", "", "")) })
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	// Wait for background restores that were told "started" to finish.
	r.mu.Lock()
	r.mu.Unlock() //nolint:staticcheck // waiting for the last holder
	// With the runner free, an untracked restore starts.
	count(r.StartRestoreItem(t.Context(), db.RestorePoint{}, "web", "container", "", ""))
	r.mu.Lock()
	r.mu.Unlock() //nolint:staticcheck // waiting for the last holder
	if got := rec.peak.Load(); got != 1 {
		t.Errorf("peak concurrent restore/tracked work = %d, want 1", got)
	}
	if started.Load() == 0 || refused.Load() == 0 {
		t.Errorf("started = %d, refused = %d; want both", started.Load(), refused.Load())
	}
}

// A held runner refuses an untracked restore straight away, and a draining
// daemon refuses one before it starts.
func TestUntrackedRestoreRefusals(t *testing.T) {
	r, _ := newTestRunner(t)
	ran := false
	r.untrackedRestoreFn = func(context.Context, db.RestorePoint, string, string, string, string, ...RestoreItemOptions) error {
		ran = true
		return nil
	}
	r.mu.Lock()
	err := r.StartRestoreItem(t.Context(), db.RestorePoint{}, "web", "container", "", "")
	r.mu.Unlock()
	if !errors.Is(err, ErrRunnerBusy) || ran {
		t.Fatalf("busy: err = %v, ran = %v", err, ran)
	}
	r.activeMu.Lock()
	r.draining = true
	r.activeMu.Unlock()
	if err := r.RestoreItem(db.RestorePoint{}, "web", "container", "", ""); !errors.Is(err, ErrRunnerDraining) || ran {
		t.Fatalf("draining: err = %v, ran = %v", err, ran)
	}
	// A restore that runs releases the runner and the drain count afterwards.
	r.activeMu.Lock()
	r.draining = false
	r.activeMu.Unlock()
	if err := r.RestoreItem(db.RestorePoint{}, "web", "container", "", ""); err != nil || !ran {
		t.Fatalf("free: err = %v, ran = %v", err, ran)
	}
	if !r.mu.TryLock() {
		t.Fatal("runner still held after the restore")
	}
	r.mu.Unlock()
	r.activeMu.Lock()
	active := r.activeCount
	r.activeMu.Unlock()
	if active != 0 {
		t.Errorf("activeCount = %d after the restore", active)
	}
}

// Shutdown never misses an admitted task: whenever Drain races the admission
// of a backup, a restore (tracked or untracked), a dedup GC or an orphan
// delete, either the task is refused, or Drain waits for it to finish. No task
// may be counted active once shutdown has begun (Greptile, Aikido and Copilot
// review of #612).
func TestDrainNeverMissesAnAdmittedTask(t *testing.T) {
	r, _ := newTestRunner(t)
	r.untrackedRestoreFn = func(context.Context, db.RestorePoint, string, string, string, string, ...RestoreItemOptions) error {
		runtime.Gosched()
		return nil
	}
	cases := []struct {
		name  string
		start func()
	}{
		{"untracked restore", func() { _ = r.RestoreItem(db.RestorePoint{}, "web", "container", "", "") }},
		{"tracked backup", func() { r.RunJob(9999) }},
		{"tracked restore", func() { r.RunRestore(db.RestorePoint{JobID: 9999}, nil, "", "") }},
		{"dedup GC", func() { r.RunDedupGC(db.StorageDestination{ID: 9999, Type: "bogus", DedupEnabled: true}, "gc-drain") }},
		{"orphan delete", func() { r.DeleteStorageOrphans(db.StorageDestination{ID: 9999, Type: "bogus"}, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var drained, late atomic.Bool
			// onAdmitted runs under activeMu, so it sees draining exactly as
			// Drain set it. A task counted once draining is set got past a
			// stale check, and Drain may already have returned without it.
			r.onAdmitted = func() {
				if r.draining || drained.Load() {
					late.Store(true)
				}
			}
			t.Cleanup(func() { r.onAdmitted = nil })
			for range 2000 {
				r.activeMu.Lock()
				r.draining = false // a fresh shutdown each round
				r.activeMu.Unlock()
				drained.Store(false)
				var wg sync.WaitGroup
				wg.Go(tc.start)
				wg.Go(func() {
					if err := r.Drain(t.Context()); err != nil {
						t.Errorf("Drain: %v", err)
					}
					drained.Store(true)
				})
				wg.Wait()
				if late.Load() {
					t.Fatal("a task was counted active after shutdown began")
				}
			}
		})
	}
}
