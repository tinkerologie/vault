package db

import (
	"context"
	"testing"
)

// TestRepoMidTransactionErrors_458 drops a single table on a fresh DB so a
// multi-statement method fails part-way through, exercising the later error
// branches that a closed handle cannot reach.
func TestRepoMidTransactionErrors_458(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		drop string
		call func(d *DB) error
	}{
		{name: "DeleteStorageDestination/replication", drop: "replication_sources", call: func(d *DB) error { return d.DeleteStorageDestination(1) }},
		{name: "DeleteDedupState/chunks", drop: "dedup_chunks", call: func(d *DB) error { return d.DeleteDedupState(1) }},
		{name: "DeleteDedupState/packs", drop: "dedup_packs", call: func(d *DB) error { return d.DeleteDedupState(1) }},
		{name: "DeleteDedupState/gc", drop: "dedup_gc_runs", call: func(d *DB) error { return d.DeleteDedupState(1) }},
		{name: "DropDedupState/packs", drop: "dedup_packs", call: func(d *DB) error { return d.DropDedupState(1) }},
		{name: "DropDedupState/gc", drop: "dedup_gc_runs", call: func(d *DB) error { return d.DropDedupState(1) }},
		{name: "DedupAggregates/chunks", drop: "dedup_chunks", call: func(d *DB) error { _, err := d.DedupAggregates(1); return err }},
		{name: "DedupAggregates/logical", drop: "restore_points", call: func(d *DB) error { _, err := d.DedupAggregates(1); return err }},
		{name: "RepointDedupChunks/prepare", drop: "dedup_chunks", call: func(d *DB) error {
			return d.RepointDedupChunks(1, []DedupChunk{{ChunkID: []byte{1}}})
		}},
		{name: "CreateActivityLog/insert", drop: "activity_log", call: func(d *DB) error {
			_, err := d.CreateActivityLog(ActivityLogEntry{Level: "info", Category: "c", Message: "m"})
			return err
		}},
		{name: "AppendRunLog/insert", drop: "run_log_entries", call: func(d *DB) error {
			_, err := d.AppendRunLog(ctx, RunLogEntry{RunID: 1, Level: "info", Message: "m"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := setupTestDB(t)
			if _, err := d.Exec("DROP TABLE " + tc.drop); err != nil {
				t.Fatalf("drop %s: %v", tc.drop, err)
			}
			if err := tc.call(d); err == nil {
				t.Fatalf("expected error after dropping %s", tc.drop)
			}
		})
	}
}

func TestListVerifyRunsForJobHappyPath_458(t *testing.T) {
	d := setupTestDB(t)
	jobID, err := d.CreateJob(Job{Name: "vj"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "success", RunType: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := d.CreateVerifyRun(rpID, "quick"); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := d.ListVerifyRunsForJob(jobID, 0) // 0 -> default limit
	if err != nil || len(runs) != 3 {
		t.Fatalf("ListVerifyRunsForJob = %d, %v", len(runs), err)
	}
	runs, err = d.ListVerifyRunsForJob(jobID, 2)
	if err != nil || len(runs) != 2 || runs[0].RestorePointID != rpID {
		t.Fatalf("limited = %+v, %v", runs, err)
	}
	if _, err := d.GetVerifyRun(999999); err == nil {
		t.Error("missing verify run should error")
	}
	if _, err := d.GetJobRun(999999); err == nil {
		t.Error("missing job run should error")
	}
	if _, err := d.GetRestorePoint(999999); err == nil {
		t.Error("missing restore point should error")
	}
}

func TestUpdateMountSessionStatusActiveClosed_458(t *testing.T) {
	d := closedTestDB(t)
	if err := d.UpdateMountSessionStatus(1, "active", ""); err == nil {
		t.Error("expected error on closed DB")
	}
}
