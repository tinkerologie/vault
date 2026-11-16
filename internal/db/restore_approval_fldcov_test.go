package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fldcovRequest(t *testing.T, d *DB, id string) (RestoreRequest, int64) {
	t.Helper()
	dest, err := d.CreateStorageDestination(StorageDestination{Name: "Local " + id, Type: "local", Config: `{"path":"/tmp"}`})
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.CreateJob(Job{Name: "Job " + id, StorageDestID: dest})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	request := RestoreRequest{
		ID: id, OwnerDigest: "owner-" + id, RequesterName: "fixture", Operation: json.RawMessage(`{}`),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if _, created, err := d.AdmitRestoreRequest(context.Background(), request, id, id, 5, 5, now.Add(-time.Hour)); err != nil || !created {
		t.Fatalf("admit: %v %v", created, err)
	}
	return request, job
}

func TestFinishApprovedRestoreRefusesInactiveOutcomes(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	if err := d.FinishApprovedRestore(ctx, strings.Repeat("0", 32), JobRun{ID: 1, Status: "completed"}, "completed", ""); !errors.Is(err, errApprovedRestoreInactive) {
		t.Fatalf("unknown request: %v", err)
	}

	request, job := fldcovRequest(t, d, strings.Repeat("a", 32))
	run := JobRun{JobID: job, BackupType: "full", ItemsTotal: 1, RunType: "restore", Status: "running"}
	runID, err := d.ClaimRestoreApproval(ctx, RestoreApproval{RequestID: request.ID, OperatorName: "root", ReviewDigest: "d"}, run, time.Now)
	if err != nil || runID == 0 {
		t.Fatalf("claim: %d %v", runID, err)
	}
	if _, err := d.Exec(`UPDATE job_runs SET status = 'failed' WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	run.ID, run.Status = runID, "completed"
	if err := d.FinishApprovedRestore(ctx, request.ID, run, "completed", ""); !errors.Is(err, errApprovedRestoreInactive) {
		t.Fatalf("finished run: %v", err)
	}
	// The failed transaction must not move the request on its own.
	stored, err := d.GetRestoreRequest(ctx, request.ID, "")
	if err != nil || stored.Status != "running" {
		t.Fatalf("request after refused finish: %+v %v", stored, err)
	}
	if status, err := d.GetApprovedRestoreRunStatus(ctx, runID); err != nil || status != "failed" {
		t.Fatalf("linked run status: %q %v", status, err)
	}
	if _, err := d.GetApprovedRestoreRunStatus(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked run: %v", err)
	}
}

func TestRestoreApprovalRepoSurfacesClosedDatabase(t *testing.T) {
	d := closedTestDB(t)
	ctx := context.Background()
	if _, err := d.ClaimRestoreApproval(ctx, RestoreApproval{RequestID: "x"}, JobRun{}, time.Now); err == nil {
		t.Fatal("claim on closed database succeeded")
	}
	if _, err := d.cleanupStaleRuns(ctx); err == nil {
		t.Fatal("stale run cleanup on closed database succeeded")
	}
	started := time.Now()
	if err := d.FinishApprovedRestore(ctx, "x", JobRun{ID: 1}, "failed", ""); err == nil || errors.Is(err, errApprovedRestoreInactive) {
		t.Fatalf("finish on closed database: %v", err)
	}
	if time.Since(started) < 75*time.Millisecond {
		t.Fatal("finish did not retry the transient failure")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := d.FinishApprovedRestore(cancelled, "x", JobRun{ID: 1}, "failed", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled finish: %v", err)
	}
}

func TestCarryAssistantKeysSurfacesClosedDatabases(t *testing.T) {
	live := setupTestDB(t)
	if err := CarryAssistantKeys(context.Background(), closedTestDB(t), live); err == nil {
		t.Fatal("carried keys from a closed database")
	}
	if err := CarryAssistantKeys(context.Background(), live, closedTestDB(t)); err == nil {
		t.Fatal("carried keys into a closed database")
	}
}
