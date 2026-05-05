package db

import (
	"testing"
	"time"
)

func TestDBPathAndVacuum(t *testing.T) {
	d := setupTestDB(t)
	if d.Path() == "" {
		t.Error("Path() returned empty")
	}
	if err := d.Vacuum(); err != nil {
		t.Errorf("Vacuum: %v", err)
	}
}

func TestActivityLogLifecycle(t *testing.T) {
	d := setupTestDB(t)

	// LogActivity (convenience) plus direct CreateActivityLog
	d.LogActivity("info", "test", "hello", "details")
	id, err := d.CreateActivityLog(ActivityLogEntry{
		Level: "warn", Category: "system", Message: "issue", Details: "data",
	})
	if err != nil || id == 0 {
		t.Fatalf("create: id=%d err=%v", id, err)
	}

	// ListActivityLogs (no filter)
	all, err := d.ListActivityLogs(10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected ≥2 entries, got %d", len(all))
	}

	// ListActivityLogs (with category filter)
	filtered, err := d.ListActivityLogs(10, "system")
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Category != "system" {
		t.Errorf("filter mismatch: %+v", filtered)
	}

	// CapActivityLogs — keep only 1
	if err := d.CapActivityLogs(1); err != nil {
		t.Errorf("cap: %v", err)
	}
	remaining, _ := d.ListActivityLogs(10, "")
	if len(remaining) != 1 {
		t.Errorf("expected 1 row after cap, got %d", len(remaining))
	}

	// DeleteOldActivityLogs (no-op for fresh entries)
	if err := d.DeleteOldActivityLogs(30); err != nil {
		t.Errorf("delete old: %v", err)
	}
}

func TestSettingsLifecycle(t *testing.T) {
	d := setupTestDB(t)

	// GetSetting default-fallback path
	v, err := d.GetSetting("missing_key", "default-val")
	if err != nil || v != "default-val" {
		t.Errorf("default: got v=%q err=%v", v, err)
	}

	// SetSetting then GetSetting
	if err := d.SetSetting("foo", "bar"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err = d.GetSetting("foo", "default")
	if err != nil || v != "bar" {
		t.Errorf("get: got v=%q err=%v", v, err)
	}

	// Update path (ON CONFLICT)
	if err := d.SetSetting("foo", "baz"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// GetAllSettings
	all, err := d.GetAllSettings()
	if err != nil {
		t.Fatalf("getAll: %v", err)
	}
	if all["foo"] != "baz" {
		t.Errorf("expected foo=baz, got %v", all)
	}
}

func TestJobUpdateAndDelete(t *testing.T) {
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(Job{Name: "j", BackupTypeChain: "full", StorageDestID: id})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.GetJob(jobID)
	job.Name = "j-updated"
	job.Description = "updated desc"
	if err := d.UpdateJob(job); err != nil {
		t.Errorf("update: %v", err)
	}
	got, _ := d.GetJob(jobID)
	if got.Name != "j-updated" {
		t.Errorf("update did not persist: %+v", got)
	}
	if err := d.DeleteJob(jobID); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := d.GetJob(jobID); err == nil {
		t.Error("expected error after delete")
	}
}

func TestJobItemsDelete(t *testing.T) {
	d := setupTestDB(t)
	id, _ := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", BackupTypeChain: "full", StorageDestID: id})
	if _, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "container", ItemName: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteJobItems(jobID); err != nil {
		t.Errorf("delete items: %v", err)
	}
	items, _ := d.GetJobItems(jobID)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestJobRunProgressAndCleanup(t *testing.T) {
	d := setupTestDB(t)
	sid, _ := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", BackupTypeChain: "full", StorageDestID: sid})
	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "running", BackupType: "full", ItemsTotal: 5})
	if err != nil {
		t.Fatal(err)
	}

	// UpdateJobRunProgress
	if err := d.UpdateJobRunProgress(runID, 3, 1, 1024); err != nil {
		t.Errorf("progress: %v", err)
	}

	// CleanupStaleRuns — should mark our running run as failed
	n, err := d.CleanupStaleRuns()
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 stale run, got %d", n)
	}

	// ListRecentRuns
	recent, err := d.ListRecentRuns(10)
	if err != nil {
		t.Errorf("recent: %v", err)
	}
	if len(recent) != 1 || recent[0].Status != "failed" {
		t.Errorf("recent runs: %+v", recent)
	}
}

func TestDeleteOldFailedRunsAndPurge(t *testing.T) {
	d := setupTestDB(t)
	sid, _ := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", BackupTypeChain: "full", StorageDestID: sid})

	// Insert a synthetic failed run with completed_at far in the past.
	_, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, run_type, items_total, completed_at)
		VALUES (?, 'failed', 'full', 'backup', 1, datetime('now', '-60 days'))`,
		jobID,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a recent failed run that should NOT be deleted by keepDays=30.
	_, err = d.CreateJobRun(JobRun{JobID: jobID, Status: "failed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec("UPDATE job_runs SET completed_at=CURRENT_TIMESTAMP WHERE completed_at IS NULL")
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := d.DeleteOldFailedRuns(30)
	if err != nil {
		t.Errorf("delete old: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deletion, got %d", deleted)
	}

	// PurgeJobRuns
	purged, err := d.PurgeJobRuns()
	if err != nil {
		t.Errorf("purge: %v", err)
	}
	if purged < 1 {
		t.Errorf("expected ≥1 purged, got %d", purged)
	}
}

func TestRestorePointDeletes(t *testing.T) {
	d := setupTestDB(t)
	sid, _ := d.CreateStorageDestination(StorageDestination{Name: "s", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "j", BackupTypeChain: "full", StorageDestID: sid})
	runID, _ := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpID, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "/path/a", SizeBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteRestorePoint(rpID); err != nil {
		t.Errorf("delete RP: %v", err)
	}
	rps, _ := d.ListRestorePoints(jobID)
	if len(rps) != 0 {
		t.Errorf("expected 0 rps, got %d", len(rps))
	}

	// Insert an expired RP for GetExpiredRestorePoints
	_, err = d.Exec(
		`INSERT INTO restore_points (job_run_id, job_id, backup_type, storage_path, size_bytes, created_at)
		VALUES (?, ?, 'full', '/expired', 0, datetime('now', '-60 days'))`,
		runID, jobID,
	)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := d.GetExpiredRestorePoints(jobID, 30)
	if err != nil {
		t.Errorf("get expired: %v", err)
	}
	if len(expired) != 1 {
		t.Errorf("expected 1 expired, got %d", len(expired))
	}

	if err := d.DeleteExpiredRestorePoints(jobID, 30); err != nil {
		t.Errorf("delete expired: %v", err)
	}
	expiredAfter, _ := d.GetExpiredRestorePoints(jobID, 30)
	if len(expiredAfter) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(expiredAfter))
	}

	// Touch time package so unused-import goroutine isn't an issue elsewhere.
	_ = time.Now
}

func TestStorageUpdateAndDelete(t *testing.T) {
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "first", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := d.GetStorageDestination(id)
	dest.Name = "renamed"
	dest.Config = `{"path":"/new"}`
	if err := d.UpdateStorageDestination(dest); err != nil {
		t.Errorf("update: %v", err)
	}
	got, _ := d.GetStorageDestination(id)
	if got.Name != "renamed" {
		t.Errorf("update not persisted: %+v", got)
	}
	if err := d.DeleteStorageDestination(id); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	// Ping/exec failure: open a path inside a non-existent dir.
	_, err := Open("/nonexistent-vault-test-dir-xyz/db.sqlite")
	if err == nil {
		t.Error("expected error for unwritable path")
	}
}

func TestSnapshotClose(t *testing.T) {
	d := setupTestDB(t)
	if err := d.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	// Closing again should be a no-op (or safe error).
	_ = d.Close()
}

func TestGetSettingDBClosed(t *testing.T) {
	d := setupTestDB(t)
	d.Close()
	// Real DB error path
	_, err := d.GetSetting("k", "default")
	if err == nil {
		t.Error("expected error from closed DB")
	}
}
