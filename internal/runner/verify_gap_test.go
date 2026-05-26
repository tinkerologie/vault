package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestRunScheduledVerifyUnknownJob exercises the "cannot load job" log
// branch. RunScheduledVerify is a void function so we observe only that
// the call returns without panicking.
func TestRunScheduledVerifyUnknownJob(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	r.RunScheduledVerify(99999, "quick") // job doesn't exist
}

// TestRunScheduledVerifyNoRestorePoints exercises the "job has no restore
// points yet" branch. The job is created but never runs, so
// GetLastRestorePoint returns sql.ErrNoRows.
func TestRunScheduledVerifyNoRestorePoints(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, err := database.CreateJob(db.Job{
		Name:            "verify-no-rps",
		Enabled:         true,
		BackupTypeChain: "full",
		StorageDestID:   dest.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.RunScheduledVerify(jobID, "quick") // should log + return cleanly
}

// TestRunScheduledVerifyInvalidMode covers the RunVerify-dispatch failure
// branch when an invalid mode is supplied. We have to seed a restore point
// first so we reach the RunVerify call.
func TestRunScheduledVerifyInvalidMode(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "vm-invalid", Enabled: true, BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "vm-invalid/1_run", Metadata: "{}", SizeBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	r.RunScheduledVerify(jobID, "totally-bogus-mode")
}

// TestVerifyOneFileMissingFile exercises the Stat-failure error path.
func TestVerifyOneFileMissingFile(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	storageDir := t.TempDir()
	adapter := storage.NewLocalAdapter(storageDir)

	res := r.verifyOneFile(adapter, "no-such-file", recordedChecksum{SHA256: "abc"}, VerifyModeQuick)
	if res.Err == nil {
		t.Error("expected error on missing file")
	}
	if !strings.Contains(res.Err.Error(), "stat") {
		t.Errorf("expected stat error, got %v", res.Err)
	}
}

// TestVerifyOneFileSizeMismatch exercises the size-mismatch error path.
func TestVerifyOneFileSizeMismatch(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	storageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(storageDir, "data.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := storage.NewLocalAdapter(storageDir)

	res := r.verifyOneFile(adapter, "data.bin", recordedChecksum{SHA256: "ignored", Size: 99999}, VerifyModeQuick)
	if res.Err == nil {
		t.Fatal("expected size-mismatch error")
	}
	if !strings.Contains(res.Err.Error(), "size mismatch") {
		t.Errorf("expected size mismatch error, got %v", res.Err)
	}
}

// TestVerifyOneFileQuickHappy exercises the quick-mode happy path: Stat
// succeeds, size matches or is unknown, no read is performed.
func TestVerifyOneFileQuickHappy(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	storageDir := t.TempDir()
	payload := []byte("payload-payload")
	if err := os.WriteFile(filepath.Join(storageDir, "data.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := storage.NewLocalAdapter(storageDir)

	// Size: 0 in the recorded checksum disables the size check.
	res := r.verifyOneFile(adapter, "data.bin", recordedChecksum{SHA256: ""}, VerifyModeQuick)
	if res.Err != nil {
		t.Errorf("quick happy path returned err: %v", res.Err)
	}
	if res.Size != 0 {
		t.Errorf("quick mode should report Size=0 (no transfer); got %d", res.Size)
	}
}

// TestVerifyOneFileDeepChecksumMatch exercises deep mode with a SHA-256
// that matches the on-disk content.
func TestVerifyOneFileDeepChecksumMatch(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	storageDir := t.TempDir()
	payload := []byte("hello vault deep verify")
	if err := os.WriteFile(filepath.Join(storageDir, "f.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(payload)
	want := hex.EncodeToString(h[:])

	adapter := storage.NewLocalAdapter(storageDir)
	res := r.verifyOneFile(adapter, "f.bin", recordedChecksum{SHA256: want}, VerifyModeDeep)
	if res.Err != nil {
		t.Errorf("expected no err on matching checksum, got %v", res.Err)
	}
	if res.Actual != want {
		t.Errorf("Actual SHA = %q, want %q", res.Actual, want)
	}
	if res.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", res.Size, len(payload))
	}
}

// TestVerifyOneFileDeepChecksumMismatch exercises deep mode with a
// recorded checksum that does NOT match the on-disk content.
func TestVerifyOneFileDeepChecksumMismatch(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	storageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(storageDir, "f.bin"), []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := storage.NewLocalAdapter(storageDir)
	// SHA of "bad" — won't match contents.
	bad := sha256.Sum256([]byte("bad"))
	wrongHex := hex.EncodeToString(bad[:])

	res := r.verifyOneFile(adapter, "f.bin", recordedChecksum{SHA256: wrongHex}, VerifyModeDeep)
	if res.Err == nil {
		t.Fatal("expected checksum-mismatch error")
	}
	if !strings.Contains(res.Err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum-mismatch error, got %v", res.Err)
	}
	if res.Expected != wrongHex {
		t.Errorf("Expected = %q, want %q", res.Expected, wrongHex)
	}
	if res.Actual == wrongHex {
		t.Errorf("Actual unexpectedly matched recorded value")
	}
}

// TestVerifyOneFileDeepReadError exercises the Read-failure error branch:
// a directory cannot be Read as a file.
type readErrorAdapter struct {
	storage.Adapter
	statSize int64
}

func (a *readErrorAdapter) Stat(p string) (storage.FileInfo, error) {
	return storage.FileInfo{Path: p, Size: a.statSize}, nil
}
func (a *readErrorAdapter) Read(string) (io.ReadCloser, error) {
	return nil, os.ErrPermission
}

func TestVerifyOneFileDeepReadError(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	a := &readErrorAdapter{statSize: 10}
	res := r.verifyOneFile(a, "x", recordedChecksum{SHA256: "deadbeef"}, VerifyModeDeep)
	if res.Err == nil {
		t.Fatal("expected error from failing Read")
	}
	if !strings.Contains(res.Err.Error(), "read") {
		t.Errorf("expected read error, got %v", res.Err)
	}
}
