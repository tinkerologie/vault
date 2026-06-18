package db

import (
	"testing"
	"time"
)

func TestCreateAndGetJob(t *testing.T) {
	d := setupTestDB(t)
	// Need a storage destination first
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})

	job := Job{
		Name: "daily-backup", Description: "Daily container backup",
		Enabled: true, Schedule: "0 2 * * *", BackupTypeChain: "full",
		RetentionCount: 7, RetentionDays: 30, Compression: "zstd",
		ContainerMode: "one_by_one", NotifyOn: "failure", StorageDestID: destID,
	}
	id, err := d.CreateJob(job)
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	got, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got.Name != "daily-backup" {
		t.Errorf("Name = %q, want %q", got.Name, "daily-backup")
	}
	if got.Schedule != "0 2 * * *" {
		t.Errorf("Schedule = %q, want %q", got.Schedule, "0 2 * * *")
	}
	if got.DeferRemoteUpload {
		t.Errorf("DeferRemoteUpload default = true, want false")
	}
}

func TestJobAnomalySensitivityRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})

	id, err := d.CreateJob(Job{
		Name: "anomaly-job", StorageDestID: destID, BackupTypeChain: "full",
	})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}

	// Defaults to "" on create (DB column DEFAULT '').
	got, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got.AnomalySensitivity != "" {
		t.Errorf("AnomalySensitivity after create = %q, want \"\"", got.AnomalySensitivity)
	}

	// Set via UpdateJob and confirm it round-trips through GetJob.
	got.AnomalySensitivity = "strict"
	if err := d.UpdateJob(got); err != nil {
		t.Fatalf("Update error = %v", err)
	}
	got2, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob after update error = %v", err)
	}
	if got2.AnomalySensitivity != "strict" {
		t.Errorf("AnomalySensitivity after update = %q, want \"strict\"", got2.AnomalySensitivity)
	}

	// A subsequent unrelated update (changing the name) must preserve the
	// previously-set sensitivity, not clobber it back to "".
	got2.Name = "anomaly-job-renamed"
	if err := d.UpdateJob(got2); err != nil {
		t.Fatalf("Update (rename) error = %v", err)
	}
	got3, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob after rename error = %v", err)
	}
	if got3.AnomalySensitivity != "strict" {
		t.Errorf("AnomalySensitivity after unrelated update = %q, want \"strict\" (clobbered)", got3.AnomalySensitivity)
	}

	// ListJobs round-trip also returns the value.
	jobs, err := d.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs error = %v", err)
	}
	found := false
	for _, j := range jobs {
		if j.ID == id {
			found = true
			if j.AnomalySensitivity != "strict" {
				t.Errorf("ListJobs AnomalySensitivity = %q, want \"strict\"", j.AnomalySensitivity)
			}
		}
	}
	if !found {
		t.Fatalf("ListJobs did not return job with ID %d", id)
	}
}

func TestJobDeferRemoteUploadRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})

	id, err := d.CreateJob(Job{
		Name: "deferred-job", StorageDestID: destID, BackupTypeChain: "full",
		ContainerMode: "one_by_one", DeferRemoteUpload: true,
	})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	got, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if !got.DeferRemoteUpload {
		t.Errorf("DeferRemoteUpload after create = false, want true")
	}

	got.DeferRemoteUpload = false
	if err := d.UpdateJob(got); err != nil {
		t.Fatalf("Update error = %v", err)
	}
	got2, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob after update error = %v", err)
	}
	if got2.DeferRemoteUpload {
		t.Errorf("DeferRemoteUpload after update to false = true, want false")
	}

	// ListJobs round-trip
	jobs, err := d.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs error = %v", err)
	}
	if len(jobs) == 0 {
		t.Fatalf("ListJobs returned no jobs, want at least 1")
	}
	found := false
	for _, j := range jobs {
		if j.ID == id {
			found = true
			if j.DeferRemoteUpload {
				t.Errorf("ListJobs returned DeferRemoteUpload=true after update to false")
			}
		}
	}
	if !found {
		t.Fatalf("ListJobs did not return job with ID %d", id)
	}
}

func TestGetJobByName(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})

	tests := []struct {
		name    string
		setup   func()
		lookup  string
		wantErr bool
		wantID  int64
	}{
		{
			name: "existing job",
			setup: func() {
				d.CreateJob(Job{Name: "my-backup", StorageDestID: destID, BackupTypeChain: "full"})
			},
			lookup:  "my-backup",
			wantErr: false,
		},
		{
			name:    "non-existent job",
			setup:   func() {},
			lookup:  "does-not-exist",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			got, err := d.GetJobByName(tt.lookup)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetJobByName(%q) error = %v, wantErr %v", tt.lookup, err, tt.wantErr)
			}
			if !tt.wantErr && got.Name != tt.lookup {
				t.Errorf("GetJobByName(%q).Name = %q", tt.lookup, got.Name)
			}
			if tt.wantErr && err != ErrNotFound {
				t.Errorf("expected ErrNotFound, got %v", err)
			}
		})
	}
}

func TestListJobs(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	d.CreateJob(Job{Name: "job-a", StorageDestID: destID})
	d.CreateJob(Job{Name: "job-b", StorageDestID: destID})
	jobs, err := d.ListJobs()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("got %d jobs, want 2", len(jobs))
	}
}

func TestJobItems(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "test-job", StorageDestID: destID})

	d.AddJobItem(JobItem{JobID: jobID, ItemType: "container", ItemName: "plex", ItemID: "abc123", Settings: "{}"})
	d.AddJobItem(JobItem{JobID: jobID, ItemType: "vm", ItemName: "windows", ItemID: "def456", Settings: `{"backup_mode":"cold"}`})

	items, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("GetJobItems error = %v", err)
	}
	if len(items) != 2 {
		t.Errorf("got %d items, want 2", len(items))
	}
}

func TestJobRunsAndRestorePoints(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "test-job", StorageDestID: destID})

	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "running", BackupType: "full", ItemsTotal: 3})
	if err != nil {
		t.Fatalf("CreateJobRun error = %v", err)
	}

	d.UpdateJobRun(JobRun{ID: runID, Status: "success", ItemsDone: 3, SizeBytes: 1024})

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("GetJobRuns error = %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != "success" {
		t.Errorf("status = %q, want %q", runs[0].Status, "success")
	}

	_, err = d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "/backups/test-job/2026-02-28", Metadata: "{}", SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint error = %v", err)
	}

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("ListRestorePoints error = %v", err)
	}
	if len(rps) != 1 {
		t.Errorf("got %d restore points, want 1", len(rps))
	}
}

func TestRestorePointChain(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "chain-job", StorageDestID: destID})
	runID, _ := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full", ItemsTotal: 1})

	fullID, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "/backups/chain-job/full", Metadata: "{}", SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint (full) error = %v", err)
	}

	incrID, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "incremental",
		StoragePath: "/backups/chain-job/incr1", Metadata: "{}",
		SizeBytes: 256, ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint (incremental) error = %v", err)
	}

	// GetRestorePoint should work.
	rp, err := d.GetRestorePoint(incrID)
	if err != nil {
		t.Fatalf("GetRestorePoint error = %v", err)
	}
	if rp.ParentRestorePointID != fullID {
		t.Errorf("ParentRestorePointID = %d, want %d", rp.ParentRestorePointID, fullID)
	}
	if rp.BackupType != "incremental" {
		t.Errorf("BackupType = %q, want %q", rp.BackupType, "incremental")
	}

	// GetLastRestorePointByType should find the full.
	lastFull, err := d.GetLastRestorePointByType(jobID, "full")
	if err != nil {
		t.Fatalf("GetLastRestorePointByType error = %v", err)
	}
	if lastFull.ID != fullID {
		t.Errorf("last full ID = %d, want %d", lastFull.ID, fullID)
	}

	// GetLastRestorePoint should find the incremental (most recent).
	lastAny, err := d.GetLastRestorePoint(jobID)
	if err != nil {
		t.Fatalf("GetLastRestorePoint error = %v", err)
	}
	if lastAny.ID != incrID {
		t.Errorf("last any ID = %d, want %d", lastAny.ID, incrID)
	}
}

func TestGetJobRunsDurationSeconds(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "duration-job", StorageDestID: destID, BackupTypeChain: "full"})

	runID, err := d.CreateJobRun(JobRun{
		JobID: jobID, Status: "running", BackupType: "full", RunType: "backup",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(
		`UPDATE job_runs SET status='completed', started_at=datetime('now','-10 seconds'), completed_at=datetime('now'), size_bytes=10485760 WHERE id=?`,
		runID,
	)
	if err != nil {
		t.Fatal(err)
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least one run")
	}
	run := runs[0]
	if run.DurationSeconds == nil {
		t.Fatal("DurationSeconds should be non-nil for completed run")
	}
	if *run.DurationSeconds < 9 || *run.DurationSeconds > 12 {
		t.Errorf("DurationSeconds = %d, expected ~10", *run.DurationSeconds)
	}
}

func TestMaxParallelUploadsRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}"})
	id, err := d.CreateJob(Job{Name: "mp", StorageDestID: destID, BackupTypeChain: "full", MaxParallelUploads: 5})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	got, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.MaxParallelUploads != 5 {
		t.Errorf("MaxParallelUploads = %d, want 5", got.MaxParallelUploads)
	}
	if got := (Job{MaxParallelUploads: 0}).EffectiveUploadConcurrency(); got != 3 {
		t.Errorf("EffectiveUploadConcurrency(0) = %d, want 3", got)
	}
	if got := (Job{MaxParallelUploads: 99}).EffectiveUploadConcurrency(); got != 16 {
		t.Errorf("EffectiveUploadConcurrency(99) = %d, want 16", got)
	}
}

func TestRetentionCount(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "retention-job", StorageDestID: destID})
	runID, _ := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full", ItemsTotal: 1})

	// Create 5 restore points.
	for i := 0; i < 5; i++ {
		_, err := d.CreateRestorePoint(RestorePoint{
			JobRunID: runID, JobID: jobID, BackupType: "full",
			StoragePath: "/backups/rp" + string(rune('a'+i)), Metadata: "{}",
			SizeBytes: int64(100 * (i + 1)),
		})
		if err != nil {
			t.Fatalf("CreateRestorePoint %d error = %v", i, err)
		}
	}

	// Keep 3 — should return 2 old ones.
	old, err := d.GetOldRestorePoints(jobID, 3)
	if err != nil {
		t.Fatalf("GetOldRestorePoints error = %v", err)
	}
	if len(old) != 2 {
		t.Errorf("got %d old restore points, want 2", len(old))
	}

	// Delete old.
	if err := d.DeleteOldRestorePoints(jobID, 3); err != nil {
		t.Fatalf("DeleteOldRestorePoints error = %v", err)
	}
	remaining, _ := d.ListRestorePoints(jobID)
	if len(remaining) != 3 {
		t.Errorf("got %d remaining restore points, want 3", len(remaining))
	}
}

// TestJobItemMissingSince exercises the stale-item remediation DB methods:
// MarkJobItemsMissing, ClearJobItemsMissing, DeleteJobItem, DeleteJobItemsByIDs.
func TestJobItemMissingSince(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test-stale", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(Job{Name: "stale-job", StorageDestID: destID, BackupTypeChain: "full"})

	id1, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "container", ItemName: "alpha", ItemID: "aaa", Settings: "{}"})
	if err != nil {
		t.Fatalf("AddJobItem 1: %v", err)
	}
	id2, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "container", ItemName: "beta", ItemID: "bbb", Settings: "{}"})
	if err != nil {
		t.Fatalf("AddJobItem 2: %v", err)
	}
	id3, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "container", ItemName: "gamma", ItemID: "ccc", Settings: "{}"})
	if err != nil {
		t.Fatalf("AddJobItem 3: %v", err)
	}

	// --- MarkJobItemsMissing ---
	ts := time.Now().UTC().Format(time.RFC3339)
	if err := d.MarkJobItemsMissing([]int64{id1}, ts); err != nil {
		t.Fatalf("MarkJobItemsMissing: %v", err)
	}

	items, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("GetJobItems after mark: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	byID := make(map[int64]JobItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	if byID[id1].MissingSince == nil {
		t.Errorf("item %d: MissingSince should be non-nil after mark", id1)
	} else if *byID[id1].MissingSince != ts {
		t.Errorf("item %d: MissingSince = %q, want %q", id1, *byID[id1].MissingSince, ts)
	}
	if byID[id2].MissingSince != nil {
		t.Errorf("item %d: MissingSince should be nil (unmarked)", id2)
	}
	if byID[id3].MissingSince != nil {
		t.Errorf("item %d: MissingSince should be nil (unmarked)", id3)
	}

	// Second call with a different timestamp must NOT overwrite the original
	// (only NULL rows are updated).
	ts2 := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	if err := d.MarkJobItemsMissing([]int64{id1}, ts2); err != nil {
		t.Fatalf("MarkJobItemsMissing (second call): %v", err)
	}
	items2, _ := d.GetJobItems(jobID)
	byID2 := make(map[int64]JobItem, len(items2))
	for _, it := range items2 {
		byID2[it.ID] = it
	}
	if byID2[id1].MissingSince == nil || *byID2[id1].MissingSince != ts {
		t.Errorf("item %d: MissingSince should still be original ts %q, got %v", id1, ts, byID2[id1].MissingSince)
	}

	// No-op on empty slice.
	if err := d.MarkJobItemsMissing(nil, ts); err != nil {
		t.Errorf("MarkJobItemsMissing(nil): unexpected error: %v", err)
	}

	// --- ClearJobItemsMissing ---
	if err := d.ClearJobItemsMissing([]int64{id1}); err != nil {
		t.Fatalf("ClearJobItemsMissing: %v", err)
	}
	items3, _ := d.GetJobItems(jobID)
	byID3 := make(map[int64]JobItem, len(items3))
	for _, it := range items3 {
		byID3[it.ID] = it
	}
	if byID3[id1].MissingSince != nil {
		t.Errorf("item %d: MissingSince should be nil after clear, got %v", id1, byID3[id1].MissingSince)
	}

	// No-op on empty slice.
	if err := d.ClearJobItemsMissing(nil); err != nil {
		t.Errorf("ClearJobItemsMissing(nil): unexpected error: %v", err)
	}

	// --- DeleteJobItem ---
	if err := d.DeleteJobItem(id2); err != nil {
		t.Fatalf("DeleteJobItem: %v", err)
	}
	items4, _ := d.GetJobItems(jobID)
	for _, it := range items4 {
		if it.ID == id2 {
			t.Errorf("item %d should have been deleted by DeleteJobItem", id2)
		}
	}
	if len(items4) != 2 {
		t.Errorf("expected 2 items after DeleteJobItem, got %d", len(items4))
	}

	// --- DeleteJobItemsByIDs (no-op nil) ---
	if err := d.DeleteJobItemsByIDs(nil); err != nil {
		t.Errorf("DeleteJobItemsByIDs(nil): unexpected error: %v", err)
	}
	items5, _ := d.GetJobItems(jobID)
	if len(items5) != 2 {
		t.Errorf("no-op DeleteJobItemsByIDs(nil) changed row count to %d, expected 2", len(items5))
	}

	// --- DeleteJobItemsByIDs (remove id3) ---
	if err := d.DeleteJobItemsByIDs([]int64{id3}); err != nil {
		t.Fatalf("DeleteJobItemsByIDs: %v", err)
	}
	items6, _ := d.GetJobItems(jobID)
	for _, it := range items6 {
		if it.ID == id3 {
			t.Errorf("item %d should have been deleted by DeleteJobItemsByIDs", id3)
		}
	}
	if len(items6) != 1 {
		t.Errorf("expected 1 item after DeleteJobItemsByIDs, got %d", len(items6))
	}
}

func TestPurgeEligibleRuns(t *testing.T) {
	t.Parallel()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close() //nolint:errcheck

	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: `{"path":"/tmp"}`})
	jobID, _ := d.CreateJob(Job{Name: "j", Schedule: "@daily", Compression: "none", Encryption: "none", StorageDestID: destID})

	mkRun := func(daysAgo int, withRP bool) int64 {
		t.Helper()
		runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "success", BackupType: "full"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		ts := time.Now().AddDate(0, 0, -daysAgo).UTC().Format("2006-01-02 15:04:05")
		if _, err := d.Exec(`UPDATE job_runs SET completed_at = ? WHERE id = ?`, ts, runID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if withRP {
			if _, err := d.CreateRestorePoint(RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p"}); err != nil {
				t.Fatalf("rp: %v", err)
			}
		}
		return runID
	}

	oldOrphan := mkRun(100, false)
	oldWithRP := mkRun(100, true)
	recent := mkRun(5, false)

	n, err := d.PurgeEligibleRuns(30)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	exists := func(id int64) bool {
		var x int64
		return d.QueryRow(`SELECT id FROM job_runs WHERE id = ?`, id).Scan(&x) == nil
	}
	if exists(oldOrphan) {
		t.Errorf("old orphan run should be purged")
	}
	if !exists(oldWithRP) {
		t.Errorf("old run with restore point must be kept")
	}
	if !exists(recent) {
		t.Errorf("recent run must be kept")
	}
	if n, _ := d.PurgeEligibleRuns(0); n != 0 {
		t.Errorf("keepDays=0 purged %d, want 0", n)
	}
}
