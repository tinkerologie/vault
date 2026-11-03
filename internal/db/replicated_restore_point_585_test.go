package db

import (
	"testing"
	"time"
)

func replicatedFixture(t *testing.T) (*DB, int64) {
	t.Helper()
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateReplicatedJob(Job{Name: "[peer] j", StorageDestID: destID, SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return d, jobID
}

// A replicated restore point is stored with a finished run it references, both
// stamped with the peer's backup time (#585).
func TestCreateReplicatedRestorePoint585(t *testing.T) {
	t.Parallel()
	d, jobID := replicatedFixture(t)
	ts := time.Date(2026, 10, 15, 13, 25, 59, 0, time.FixedZone("AEST", 10*3600))

	runID, rpID, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: jobID, Status: "partial", BackupType: "full", Log: "replicated",
			ItemsTotal: 3, ItemsDone: 2, ItemsFailed: 1, SizeBytes: 42},
		RestorePoint{JobRunID: 999, JobID: jobID, BackupType: "full", StoragePath: "j/run", Metadata: "{}", SizeBytes: 42, SourceID: 1},
		ts)
	if err != nil {
		t.Fatalf("CreateReplicatedRestorePoint: %v", err)
	}

	run, err := d.GetJobRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "partial" || run.RunType != "backup" || run.ItemsDone != 2 || run.ItemsFailed != 1 || run.SizeBytes != 42 || run.Log != "replicated" {
		t.Errorf("run = %+v", run)
	}
	if !run.StartedAt.Equal(ts) || run.CompletedAt == nil || !run.CompletedAt.Equal(ts) {
		t.Errorf("run times = %v / %v, want %v", run.StartedAt, run.CompletedAt, ts)
	}

	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) != 1 {
		t.Fatalf("restore points = %v, %v", rps, err)
	}
	rp := rps[0]
	if rp.ID != rpID || rp.JobRunID != runID || rp.SourceID != 1 || rp.StoragePath != "j/run" {
		t.Errorf("restore point = %+v, want run %d", rp, runID)
	}
	if !rp.CreatedAt.Equal(ts) {
		t.Errorf("restore point created_at = %v, want %v", rp.CreatedAt, ts)
	}
	// SQLite's date functions must read the stored times: retention, run
	// durations and "since" queries depend on them.
	var dur, expired int
	if err := d.QueryRow(`SELECT CAST((julianday(completed_at) - julianday(started_at)) * 86400 AS INTEGER),
		(SELECT COUNT(*) FROM restore_points WHERE created_at < datetime('2026-10-16'))
		FROM job_runs WHERE id = ?`, runID).Scan(&dur, &expired); err != nil {
		t.Fatalf("SQLite cannot read the stored times: %v", err)
	}
	if expired != 1 {
		t.Errorf("restore points before 2026-10-16 = %d, want 1", expired)
	}
	if runs, err := d.GetJobRunsSince(jobID, ts.Add(-time.Hour)); err != nil || len(runs) != 1 {
		t.Errorf("GetJobRunsSince = %d runs, %v; want 1", len(runs), err)
	}

	// A zero time means now.
	before := time.Now().Add(-time.Minute)
	runID2, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		RestorePoint{JobID: jobID, BackupType: "full", StoragePath: "j/run2"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if run2, err := d.GetJobRun(runID2); err != nil || run2.StartedAt.Before(before) {
		t.Errorf("zero-time run = %+v, %v", run2, err)
	}
}

// If the restore point insert fails, the run is rolled back too, so no run
// claims a backup the replica has no record of.
func TestCreateReplicatedRestorePointRollsBack585(t *testing.T) {
	t.Parallel()
	d, jobID := replicatedFixture(t)
	if _, err := d.Exec(`CREATE TRIGGER reject_rp BEFORE INSERT ON restore_points
		BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}

	_, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		RestorePoint{JobID: jobID, BackupType: "full", StoragePath: "j/run"}, time.Now())
	if err == nil {
		t.Fatal("want an error when the restore point insert fails")
	}
	var runs int
	if err := d.QueryRow(`SELECT COUNT(*) FROM job_runs WHERE job_id = ?`, jobID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Errorf("job_runs = %d after a failed insert, want 0", runs)
	}

	// A run for a job that does not exist fails before any restore point.
	if _, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: 9999, Status: "success", BackupType: "full"},
		RestorePoint{JobID: 9999, BackupType: "full", StoragePath: "x/y"}, time.Now()); err == nil {
		t.Error("want an error for a missing job")
	}
}

func TestCreateReplicatedRestorePointClosedDB585(t *testing.T) {
	t.Parallel()
	d, jobID := replicatedFixture(t)
	_ = d.Close()
	if _, _, err := d.CreateReplicatedRestorePoint(JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		RestorePoint{JobID: jobID, BackupType: "full", StoragePath: "j/run"}, time.Now()); err == nil {
		t.Fatal("want an error from a closed database")
	}
}

// The restore point belongs to the job of the run it is recorded with: a zero
// rp.JobID takes the run's job, and a different one is refused with no rows
// written.
func TestCreateReplicatedRestorePointJobFromRun585(t *testing.T) {
	t.Parallel()
	d, jobID := replicatedFixture(t)
	otherID, err := d.CreateReplicatedJob(Job{Name: "[peer] other", SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}

	runID, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		RestorePoint{BackupType: "full", StoragePath: "j/run"}, time.Now())
	if err != nil {
		t.Fatalf("zero rp.JobID: %v", err)
	}
	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) != 1 || rps[0].JobID != jobID || rps[0].JobRunID != runID {
		t.Fatalf("restore points = %+v, %v; want one on job %d, run %d", rps, err, jobID, runID)
	}

	if _, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: jobID, Status: "success", BackupType: "full"},
		RestorePoint{JobID: otherID, BackupType: "full", StoragePath: "j/run2"}, time.Now()); err == nil {
		t.Fatal("want an error when rp.JobID names another job")
	}
	if n := countRunsAndPoints(t, d); n != 2 {
		t.Errorf("rows = %d after the refused call, want 2 (one run, one restore point)", n)
	}
}

func countRunsAndPoints(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM job_runs) + (SELECT COUNT(*) FROM restore_points)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
