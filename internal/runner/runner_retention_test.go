package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// createRestorePointWithAge inserts a job_run + restore_point and back-dates
// the restore_point.created_at via direct UPDATE so callers can simulate old
// backups. Returns the new restore point ID.
func createRestorePointWithAge(t *testing.T, database *db.DB, jobID int64, backupType, storagePath string, parentRPID int64, ago time.Duration) int64 {
	t.Helper()
	runID, err := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: backupType,
	})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	rpID, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: backupType,
		StoragePath: storagePath, Metadata: "{}",
		ParentRestorePointID: parentRPID,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	// Back-date the row so retention-by-days bites.
	when := time.Now().Add(-ago).UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`UPDATE restore_points SET created_at = ? WHERE id = ?`, when, rpID); err != nil {
		t.Fatalf("back-date created_at: %v", err)
	}
	return rpID
}

// TestEnforceRetentionKeepCountTrimsOlder confirms the count-based retention
// drops everything beyond the most-recent N restore points.
func TestEnforceRetentionKeepCountTrimsOlder(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, err := database.CreateJob(db.Job{
		Name: "ret-job", BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// 5 standalone fulls aged 4d→0d.
	var ids []int64
	for i := 0; i < 5; i++ {
		ago := time.Duration(4-i) * 24 * time.Hour
		path := filepath.Join("ret-job", "run_"+time.Now().Add(-ago).Format("20060102"))
		ids = append(ids, createRestorePointWithAge(t, database, jobID, "full", path, 0, ago))
		// Create the storage dir so deleteStorageDir has something to remove.
		dir := filepath.Join(storageDir, path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	r.enforceRetention(dest, jobID, 2, 0)

	remaining, err := database.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("ListRestorePoints: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("got %d restore points after retention, want 2 (ids: %v)", len(remaining), ids)
	}
}

// TestEnforceRetentionKeepDaysCutoff confirms time-based retention drops
// restore points older than keepDays.
func TestEnforceRetentionKeepDaysCutoff(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "ret-days-job", BackupTypeChain: "full", StorageDestID: dest.ID,
	})

	// One fresh (1d ago) full + two stale (30d ago) fulls.
	createRestorePointWithAge(t, database, jobID, "full", "ret-days-job/recent", 0, 24*time.Hour)
	createRestorePointWithAge(t, database, jobID, "full", "ret-days-job/old1", 0, 30*24*time.Hour)
	createRestorePointWithAge(t, database, jobID, "full", "ret-days-job/old2", 0, 60*24*time.Hour)

	r.enforceRetention(dest, jobID, 0, 7) // keep 7 days

	remaining, _ := database.ListRestorePoints(jobID)
	if len(remaining) != 1 {
		t.Errorf("expected 1 restore point within 7 days, got %d", len(remaining))
	}
}

// TestEnforceRetentionProtectsChainAncestors verifies that when an
// incremental is protected, its full parent is also kept regardless of age.
func TestEnforceRetentionProtectsChainAncestors(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "ret-chain-job", BackupTypeChain: "full,incremental", StorageDestID: dest.ID,
	})

	parent := createRestorePointWithAge(t, database, jobID, "full", "ret-chain-job/full", 0, 90*24*time.Hour)
	createRestorePointWithAge(t, database, jobID, "incremental", "ret-chain-job/inc", parent, 1*24*time.Hour)

	// keepCount=1 → only the incremental is directly protected, but the full
	// is its ancestor so should survive.
	r.enforceRetention(dest, jobID, 1, 0)

	remaining, _ := database.ListRestorePoints(jobID)
	if len(remaining) != 2 {
		t.Errorf("expected 2 restore points (incremental + full ancestor), got %d", len(remaining))
	}
}

// TestEnforceRetentionLTRKeepsLatest exercises the LTR path: KeepLatest=2
// should retain the two newest restore points.
func TestEnforceRetentionLTRKeepsLatest(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "ltr-job", BackupTypeChain: "full", StorageDestID: dest.ID,
		KeepLatest: 2,
	})

	for i := 0; i < 4; i++ {
		ago := time.Duration(i) * 24 * time.Hour
		createRestorePointWithAge(t, database, jobID, "full",
			"ltr-job/run_"+time.Now().Add(-ago).Format("20060102"), 0, ago)
	}

	r.enforceRetentionLTR(dest, jobID, LTRPolicy{KeepLatest: 2})

	remaining, _ := database.ListRestorePoints(jobID)
	if len(remaining) != 2 {
		t.Errorf("expected 2 restore points after LTR retention, got %d", len(remaining))
	}
}

// TestDeleteVMCheckpointsForRPNoMetadata covers the early-return when the
// restore point has empty metadata.
func TestDeleteVMCheckpointsForRPNoMetadata(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.deleteVMCheckpointsForRP(db.RestorePoint{Metadata: ""})
}

// TestDeleteVMCheckpointsForRPBadJSON covers the json.Unmarshal-failure
// early-return path.
func TestDeleteVMCheckpointsForRPBadJSON(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.deleteVMCheckpointsForRP(db.RestorePoint{Metadata: "{not json"})
}

// TestDeleteVMCheckpointsForRPNoCheckpoints covers metadata without a
// vm_checkpoints map (returns silently).
func TestDeleteVMCheckpointsForRPNoCheckpoints(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.deleteVMCheckpointsForRP(db.RestorePoint{Metadata: `{"other":"data"}`})
}

// TestDeleteStorageDirInternalWrapper exercises the lowercase wrapper so
// runner.go's deleteStorageDir helper is covered. The public counterpart
// is already tested by TestDeleteStorageDir in import_test.go.
func TestDeleteStorageDirInternalWrapper(t *testing.T) {
	t.Parallel()
	r, _, storageDir := setupTestRunner(t)

	dir := filepath.Join(storageDir, "wrap-job", "run1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	adapter := storage.NewLocalAdapter(storageDir)
	r.deleteStorageDir(adapter, "wrap-job/run1")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory should have been removed: %v", err)
	}
}
