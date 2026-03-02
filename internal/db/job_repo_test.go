package db

import "testing"

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
