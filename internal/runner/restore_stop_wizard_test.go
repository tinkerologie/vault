package runner

import (
	"context"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// A wizard stop targets only the exact active restore run (#701).
func TestStopRestoreRunWizardMatchesOnlyTheActiveRestore(t *testing.T) {
	r, database := newTestRunner(t)
	jobID, err := database.CreateJob(db.Job{Name: "wizard stop", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", RunType: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	stopped := 0
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	r.publishRestoreCancellation(&RunStatus{Active: true, JobID: jobID, RunID: runID, RunType: "restore"}, func(cause error) {
		stopped++
		cancel(cause)
	})

	// Another run ID never matches.
	if ok, err := r.StopRestoreRun(context.Background(), runID+1, "operator"); ok || err != nil {
		t.Fatalf("stop of another run: %v %v", ok, err)
	}
	// A backup that owns the cancel function under the same run ID is not a restore.
	r.statusMu.Lock()
	r.currentRun.RunType = "backup"
	r.statusMu.Unlock()
	if ok, err := r.StopRestoreRun(context.Background(), runID, "operator"); ok || err != nil || stopped != 0 {
		t.Fatalf("stop of a backup run: %v %v stopped=%d", ok, err, stopped)
	}
	r.statusMu.Lock()
	r.currentRun.RunType = "restore"
	r.statusMu.Unlock()

	if ok, err := r.StopRestoreRun(context.Background(), runID, "operator"); !ok || err != nil {
		t.Fatalf("stop: %v %v", ok, err)
	}
	if ok, err := r.StopRestoreRun(context.Background(), runID, "operator"); !ok || err != nil || stopped != 1 {
		t.Fatalf("repeated stop: %v %v stopped=%d", ok, err, stopped)
	}
	if context.Cause(ctx) != errOperatorRestoreStop {
		t.Fatalf("stop cause = %v, want the operator stop", context.Cause(ctx))
	}
	if !r.Status().Cancelling {
		t.Fatal("runner status does not show the stop")
	}
	run, err := database.GetJobRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.StoppedBy != "operator" || run.StopRequestedAt == nil {
		t.Fatalf("stop audit: %+v", run)
	}
}

// A stop that races the run's own completion reports nothing to stop.
func TestStopRestoreRunWizardAfterTheRunFinished(t *testing.T) {
	r, database := newTestRunner(t)
	jobID, err := database.CreateJob(db.Job{Name: "finished", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", RunType: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	r.publishRestoreCancellation(&RunStatus{Active: true, JobID: jobID, RunID: runID, RunType: "restore"}, func(error) {
		t.Fatal("a finished run must not be cancelled")
	})
	if ok, err := r.StopRestoreRun(context.Background(), runID, "operator"); ok || err != nil {
		t.Fatalf("late stop: %v %v", ok, err)
	}
}
