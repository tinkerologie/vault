package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunVerifyClassicQuickPasses drives the full runVerifyLoop (classic
// per-file path) with a recorded checksum that matches the actual file.
// Mode=quick so deep streaming isn't exercised (covered separately).
func TestRunVerifyClassicQuickPasses(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Lay down a real backup file with a known checksum.
	jobID, _ := database.CreateJob(db.Job{
		Name: "classic-job", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})

	storagePath := "classic-job/1_run"
	if err := os.MkdirAll(filepath.Join(storageDir, storagePath, "ItemA"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("classic verify payload")
	if err := os.WriteFile(filepath.Join(storageDir, storagePath, "ItemA", "data.tar"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	meta := map[string]any{
		"items":       1,
		"item_sizes":  map[string]int64{"ItemA": int64(len(payload))},
		"checksums":   map[string]map[string]string{"ItemA": {"data.tar": hexSum}},
		"backup_type": "full",
	}
	metaJSON, _ := json.Marshal(meta)

	rpID, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: storagePath, Metadata: string(metaJSON), SizeBytes: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rp, err := database.GetLastRestorePoint(jobID)
	if err != nil {
		t.Fatal(err)
	}
	_ = rpID

	verifyID, err := r.RunVerify(rp, VerifyModeQuick)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	run := waitForVerifyCompletion(t, database, verifyID, 5*time.Second)
	if run.Status != "passed" {
		t.Errorf("status = %q, want passed (summary=%q)", run.Status, run.ErrorSummary)
	}
	if run.FilesChecked != 1 {
		t.Errorf("FilesChecked = %d, want 1", run.FilesChecked)
	}
	if run.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d, want 0", run.FilesFailed)
	}
}

// TestRunVerifyClassicDeepDetectsMismatch drives the classic deep-mode
// path with a recorded checksum that does NOT match the on-disk file,
// proving the failure summary is captured.
func TestRunVerifyClassicDeepDetectsMismatch(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "classic-mismatch", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})

	storagePath := "classic-mismatch/1_run"
	if err := os.MkdirAll(filepath.Join(storageDir, storagePath, "ItemA"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, storagePath, "ItemA", "data.tar"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Wrong checksum.
	bad := sha256.Sum256([]byte("not-real"))
	wrongHex := hex.EncodeToString(bad[:])

	meta := map[string]any{
		"items":       1,
		"item_sizes":  map[string]int64{"ItemA": 4},
		"checksums":   map[string]map[string]string{"ItemA": {"data.tar": wrongHex}},
		"backup_type": "full",
	}
	metaJSON, _ := json.Marshal(meta)

	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: storagePath, Metadata: string(metaJSON), SizeBytes: 4,
	}); err != nil {
		t.Fatal(err)
	}
	rp, _ := database.GetLastRestorePoint(jobID)

	verifyID, err := r.RunVerify(rp, VerifyModeDeep)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	run := waitForVerifyCompletion(t, database, verifyID, 5*time.Second)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed", run.Status)
	}
	if run.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1", run.FilesFailed)
	}
	if run.ErrorSummary == "" {
		t.Error("expected non-empty error summary on deep mismatch")
	}
}

// TestRunVerifyClassicNoChecksumsFails drives the early-error branch in
// runVerifyLoop where the restore point metadata has no checksums map.
func TestRunVerifyClassicNoChecksumsFails(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	jobID, _ := database.CreateJob(db.Job{
		Name: "no-sums", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})

	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "no-sums/1_run", Metadata: "{}", SizeBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	rp, _ := database.GetLastRestorePoint(jobID)

	verifyID, err := r.RunVerify(rp, VerifyModeQuick)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	run := waitForVerifyCompletion(t, database, verifyID, 5*time.Second)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed (no checksums)", run.Status)
	}
	if run.ErrorSummary == "" {
		t.Errorf("expected error summary mentioning missing checksums")
	}
}

// TestRunVerifyInvalidMode exercises the early-return mode validation.
func TestRunVerifyInvalidMode(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "rv-inv", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rv-inv/1", Metadata: "{}", SizeBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rp, _ := database.GetLastRestorePoint(jobID)

	if _, err := r.RunVerify(rp, VerifyMode("nope")); err == nil {
		t.Error("expected error for invalid mode")
	}
}
