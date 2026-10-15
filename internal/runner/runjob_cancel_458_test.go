//go:build unix

package runner

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// d458GatedPreScript writes a pre-script that signals it has started through
// one FIFO and then blocks until the test writes to a second FIFO. That parks
// the run deterministically after its context and live status exist but
// before the item loop, with no sleeps or polling.
func d458GatedPreScript(t *testing.T) (script, started, release string) {
	t.Helper()
	dir := t.TempDir()
	started = filepath.Join(dir, "started")
	release = filepath.Join(dir, "release")
	for _, p := range []string{started, release} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}
	}
	script = filepath.Join(dir, "pre.sh")
	body := "#!/bin/sh\necho started > '" + started + "'\nread line < '" + release + "'\n"
	if err := writeExecutableScript(script, body); err != nil {
		t.Fatal(err)
	}
	return script, started, release
}

// d458RunGated starts RunJob for a job with a gated pre-script, calls
// whileParked once the script is running, releases it and waits for RunJob
// to return.
func d458RunGated(t *testing.T, r *Runner, jobID int64, started, release string, whileParked func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.RunJob(jobID)
	}()
	if _, err := os.ReadFile(started); err != nil {
		t.Fatalf("waiting for pre-script: %v", err)
	}
	whileParked()
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("releasing pre-script: %v", err)
	}
	<-done
}

// TestRunJob458UserCancelDuringPreScript covers the operator-cancel outcome:
// the item loop sees the cancelled context, the run ends "cancelled" with the
// user message, and no retry is scheduled.
func TestRunJob458UserCancelDuringPreScript(t *testing.T) {
	r, d := newTestRunner(t)
	script, started, release := d458GatedPreScript(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{PreScript: script, RetryMaxOverride: new(int64(3))},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	d458RunGated(t, r, jobID, started, release, func() {
		if err := r.CancelJob(jobID); err != nil {
			t.Errorf("CancelJob: %v", err)
		}
	})

	run := d458LatestRun(t, d, jobID)
	if run.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", run.Status)
	}
	if run.RetryNextAt != nil {
		t.Error("operator cancel must not schedule a retry")
	}
	if run.ItemsDone != 0 {
		t.Errorf("items done = %d, want 0", run.ItemsDone)
	}
}

// TestRunJob458WatchdogStyleCancelSchedulesRetry covers the non-operator
// cancellation outcome (the stall-watchdog branch): the context is cancelled
// without the Cancelling flag, so the run is "cancelled" and a retry is due.
func TestRunJob458WatchdogStyleCancelSchedulesRetry(t *testing.T) {
	r, d := newTestRunner(t)
	script, started, release := d458GatedPreScript(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{PreScript: script, RetryMaxOverride: new(int64(3))},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	d458RunGated(t, r, jobID, started, release, func() {
		r.cancelMu.Lock()
		fn := r.cancelFn
		r.cancelMu.Unlock()
		if fn == nil {
			t.Fatal("expected an installed cancel func while the run is parked")
		}
		fn()
	})

	run := d458LatestRun(t, d, jobID)
	if run.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", run.Status)
	}
	if run.RetryNextAt == nil {
		t.Error("a watchdog cancel should schedule a retry")
	}
}
