package runner

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestStallWatchdogCancelsOnNoProgress(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now().Add(-time.Hour) // already stale
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	r.startStallWatchdog(ctx, done, cancel, 1, 0, 2*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond)

	select {
	case <-ctx.Done():
		// cancelled as expected
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel a stalled context")
	}
}

func TestStallWatchdogHeartbeatPreventsCancel(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now()
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	r.startStallWatchdog(ctx, done, cancel, 1, 0, 2*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond)

	// Heartbeat for ~120ms, longer than cancelAfter, to prove fresh progress
	// keeps the context alive.
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()
		if ctx.Err() != nil {
			t.Fatal("watchdog cancelled despite active heartbeat")
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// syncBuffer is a concurrency-safe log sink: the watchdog writes from its own
// goroutine while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestStallWatchdogKeepsWarningAfterCancel pins the issue #265 fix. The
// watchdog used to return the moment it called cancel(), so a run wedged in a
// handler that never observes cancellation produced no further log output at
// all — the operator saw a job stuck at "running" with a dead log and no way
// to tell a live backup from a wedged daemon. The watchdog must now keep
// reporting until the run itself finishes.
func TestStallWatchdogKeepsWarningAfterCancel(t *testing.T) {
	var sink syncBuffer
	prev := log.Writer()
	log.SetOutput(&sink)
	t.Cleanup(func() { log.SetOutput(prev) })

	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now().Add(-time.Hour) // already stale
	r.lastProgressMu.Unlock()

	// A context the watchdog cancels, standing in for a run whose handler is
	// blocked in an uncancellable call: nothing closes done.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	r.startStallWatchdog(ctx, done, cancel, 1, 0, 2*time.Millisecond, 4*time.Millisecond, 6*time.Millisecond)

	<-ctx.Done() // the cancel has fired

	// Well past the cancel, the watchdog must still be reporting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sink.String(), "has not unwound") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("watchdog went silent after cancelling; log was:\n%s", sink.String())
}

// TestStallWatchdogReportsExternalCancel covers the operator-cancel path: the
// run is cancelled from outside while progress is perfectly fresh, so the
// stall threshold is never reached. The watchdog must still notice the run has
// not unwound rather than waiting for a stall that will never come.
func TestStallWatchdogReportsExternalCancel(t *testing.T) {
	var sink syncBuffer
	prev := log.Writer()
	log.SetOutput(&sink)
	t.Cleanup(func() { log.SetOutput(prev) })

	r := &Runner{}
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now() // fresh — no stall
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer close(done)
	// cancelAfter is an hour: only the external cancel can trigger reporting.
	r.startStallWatchdog(ctx, done, cancel, 1, 0, 2*time.Millisecond, time.Hour, time.Hour)

	cancel() // operator hits Cancel; the handler is wedged and never returns

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sink.String(), "has not unwound") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("watchdog never reported a wedged run after an external cancel; log was:\n%s", sink.String())
}

// TestStallWatchdogRecordsStalledState covers the part of #265 that logging
// alone cannot: the reporter asked to be able to tell a stuck run from a
// healthy one, and status stays "running" in both cases because the run has
// genuinely not finished. The stall must therefore be recorded on the run row
// where the API can serve it.
func TestStallWatchdogRecordsStalledState(t *testing.T) {
	r, database, _ := setupTestRunner(t)

	jobID, err := database.CreateJob(db.Job{Name: "stall-test", Schedule: "0 2 * * *"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	r.lastProgressMu.Lock()
	r.lastProgress = time.Now().Add(-time.Hour) // already stale
	r.lastProgressMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	r.startStallWatchdog(ctx, done, cancel, jobID, runID, 2*time.Millisecond, 4*time.Millisecond, 6*time.Millisecond)

	<-ctx.Done()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := database.GetJobRun(runID)
		if err != nil {
			t.Fatalf("GetJobRun: %v", err)
		}
		if run.StalledAt != nil {
			if run.StallReason == "" {
				t.Error("stalled run recorded without a reason")
			}
			// Status must NOT have been flipped: the run has not finished,
			// and pretending otherwise would lose the real outcome.
			if run.Status != "running" {
				t.Errorf("status = %q, want it left as running", run.Status)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("watchdog never recorded stalled_at on the run row")
}
