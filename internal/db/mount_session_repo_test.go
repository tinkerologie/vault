package db

import (
	"testing"
	"time"
)

func TestMountSessionRepo_Lifecycle(t *testing.T) {
	d := setupTestDB(t)

	destID, err := d.CreateStorageDestination(StorageDestination{
		Name:         "test-dest",
		Type:         "local",
		Config:       `{"path":"/tmp/test"}`,
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(Job{
		Name:            "test-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	runID, err := d.CreateJobRun(JobRun{
		JobID:      jobID,
		Status:     "completed",
		BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	rpID, err := d.CreateRestorePoint(RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "backups/test",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	// 1. Create mount session
	sessID, err := d.CreateMountSession(MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  destID,
		MountPath:      "/mnt/vault-fuse/test-1",
	})
	if err != nil {
		t.Fatalf("CreateMountSession: %v", err)
	}
	if sessID <= 0 {
		t.Fatalf("invalid session ID: %d", sessID)
	}

	// 2. Get mount session
	s, err := d.GetMountSession(sessID)
	if err != nil {
		t.Fatalf("GetMountSession: %v", err)
	}
	if s.Status != "active" {
		t.Errorf("status = %q, want active", s.Status)
	}
	if s.JobName != "test-job" {
		t.Errorf("job_name = %q, want test-job", s.JobName)
	}
	if s.StorageName != "test-dest" {
		t.Errorf("storage_name = %q, want test-dest", s.StorageName)
	}
	if s.MountPath != "/mnt/vault-fuse/test-1" {
		t.Errorf("mount_path = %q, want /mnt/vault-fuse/test-1", s.MountPath)
	}

	// 3. List active sessions
	activeList, err := d.ListMountSessions(true)
	if err != nil {
		t.Fatalf("ListMountSessions: %v", err)
	}
	if len(activeList) != 1 {
		t.Fatalf("active count = %d, want 1", len(activeList))
	}

	// 4. Update activity
	time.Sleep(10 * time.Millisecond)
	if err := d.UpdateMountSessionActivity(sessID); err != nil {
		t.Fatalf("UpdateMountSessionActivity: %v", err)
	}

	// 5. Update status to stopped
	if err := d.UpdateMountSessionStatus(sessID, "stopped", ""); err != nil {
		t.Fatalf("UpdateMountSessionStatus: %v", err)
	}
	s, err = d.GetMountSession(sessID)
	if err != nil {
		t.Fatalf("GetMountSession after stop: %v", err)
	}
	if s.Status != "stopped" || s.StoppedAt == nil {
		t.Errorf("status = %q, stopped_at = %v; want stopped with non-nil time", s.Status, s.StoppedAt)
	}

	// 6. Cleanup stale sessions
	// Create another active session
	// Test UpdateMountSessionPath
	if err := d.UpdateMountSessionPath(sessID, "/mnt/vault-fuse/test-updated"); err != nil {
		t.Fatalf("UpdateMountSessionPath: %v", err)
	}
	if s, err := d.GetMountSession(sessID); err != nil || s.MountPath != "/mnt/vault-fuse/test-updated" {
		t.Fatalf("mount_path not updated: %v, path=%q", err, s.MountPath)
	}

	staleID, err := d.CreateMountSession(MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  destID,
		MountPath:      "/mnt/vault-fuse/stale-1",
	})
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}

	crashed, err := d.CleanupStaleMountSessions()
	if err != nil {
		t.Fatalf("CleanupStaleMountSessions: %v", err)
	}
	if len(crashed) != 1 || crashed[0].ID != staleID {
		t.Fatalf("crashed sessions = %+v, want staleID %d", crashed, staleID)
	}

	staleSess, err := d.GetMountSession(staleID)
	if err != nil {
		t.Fatalf("GetMountSession stale: %v", err)
	}
	if staleSess.Status != "crashed" {
		t.Errorf("stale status = %q, want crashed", staleSess.Status)
	}

	// 7. Delete session
	if err := d.DeleteMountSession(staleID); err != nil {
		t.Fatalf("DeleteMountSession: %v", err)
	}
	if _, err := d.GetMountSession(staleID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}
