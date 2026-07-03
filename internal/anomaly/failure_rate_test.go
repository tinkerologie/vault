package anomaly

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRefreshBaselineFailureRate pins the #181 fix: the runner writes
// "completed" for successes (never "success"), so the old Status!="success"
// test counted every run as failed and persisted FailureRate == 1.0 for
// every job.
func TestRefreshBaselineFailureRate(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{Name: "j181", StorageDestID: destID, BackupTypeChain: "full"})

	dur := 10
	mkRun := func(status string, itemsFailed int) db.JobRun {
		return db.JobRun{JobID: jobID, Status: status, ItemsFailed: itemsFailed, SizeBytes: 100, DurationSeconds: &dur}
	}
	// 3 completed, 1 failed → failure rate 0.25.
	runs := []db.JobRun{
		mkRun("completed", 0),
		mkRun("failed", 1),
		mkRun("completed", 0),
		mkRun("completed", 0),
	}

	ev := newEvaluatorWithBroadcaster(d, nil, &Registry{}, RealClock{})
	job, _ := d.GetJob(jobID)
	ev.refreshBaseline(EvalContext{Job: &job, RecentRuns: runs, Clock: RealClock{}})

	baseline, err := d.GetJobBaseline(jobID)
	if err != nil {
		t.Fatalf("baseline not written: %v", err)
	}
	if baseline.FailureRate != 0.25 {
		t.Errorf("FailureRate = %v, want 0.25 (constant 1.0 = #181 regression)", baseline.FailureRate)
	}
}
