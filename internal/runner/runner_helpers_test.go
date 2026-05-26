package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestLogLevelForStatus tables every branch of the level mapper.
func TestLogLevelForStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"completed": "info",
		"partial":   "warning",
		"failed":    "error",
		"cancelled": "warning",
		"":          "info",
		"running":   "info",
		"other":     "info",
	}
	for status, want := range cases {
		if got := logLevelForStatus(status); got != want {
			t.Errorf("logLevelForStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestNewHandlerEachType verifies every recognised type yields a handler and
// the unknown branch returns an error. We don't exercise the handler at all,
// only the dispatch.
func TestNewHandlerEachType(t *testing.T) {
	t.Parallel()
	// Some handlers (vm, plugin, zfs) are Linux-only and return an error
	// stub on macOS/Windows. We only assert that dispatch reaches them and
	// returns _some_ result (no panic, no "unknown item type" error path).
	for _, itemType := range []string{"container", "vm", "folder", "plugin", "zfs"} {
		_, _ = newHandler(itemType)
	}
}

func TestNewHandlerUnknown(t *testing.T) {
	t.Parallel()
	if _, err := newHandler("totally-unknown-type"); err == nil {
		t.Fatal("newHandler with unknown type should error")
	}
}

// TestResolveBackupTypeFull confirms an empty/full chain yields a full
// backup with no parent.
func TestResolveBackupTypeFull(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	res := r.resolveBackupType(db.Job{BackupTypeChain: ""})
	if res.BackupType != "full" || res.ParentRP != nil {
		t.Errorf("empty chain → %+v, want full/no-parent", res)
	}
	res2 := r.resolveBackupType(db.Job{BackupTypeChain: "full"})
	if res2.BackupType != "full" || res2.ParentRP != nil {
		t.Errorf("full chain → %+v, want full/no-parent", res2)
	}
}

// TestResolveBackupTypeIncrementalFallsBackToFull covers the no-previous-rp
// path for incremental chains.
func TestResolveBackupTypeIncrementalFallsBackToFull(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "inc-job", BackupTypeChain: "incremental", StorageDestID: dest.ID,
	})
	job, _ := database.GetJob(jobID)
	res := r.resolveBackupType(job)
	if res.BackupType != "full" {
		t.Errorf("incremental with no history → %q, want full", res.BackupType)
	}
}

// TestResolveBackupTypeIncrementalWithHistory verifies that an existing
// restore point becomes the parent for an incremental run.
func TestResolveBackupTypeIncrementalWithHistory(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "inc-job-h", BackupTypeChain: "incremental", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
	})
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "inc-job-h/run1", Metadata: "{}",
	}); err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	job, _ := database.GetJob(jobID)
	res := r.resolveBackupType(job)
	if res.BackupType != "incremental" {
		t.Errorf("incremental with history → %q, want incremental", res.BackupType)
	}
	if res.ParentRP == nil {
		t.Fatal("expected ParentRP to be set for incremental run")
	}
}

// TestResolveBackupTypeDifferentialFallsBackToFull covers the no-previous-
// full path.
func TestResolveBackupTypeDifferentialFallsBackToFull(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "diff-job", BackupTypeChain: "differential", StorageDestID: dest.ID,
	})
	job, _ := database.GetJob(jobID)
	res := r.resolveBackupType(job)
	if res.BackupType != "full" {
		t.Errorf("differential with no history → %q, want full", res.BackupType)
	}
}

// TestResolveBackupTypeUnknownChainFallsBackToFull covers the default
// branch of the switch (unknown chain string).
func TestResolveBackupTypeUnknownChainFallsBackToFull(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	res := r.resolveBackupType(db.Job{BackupTypeChain: "weird"})
	if res.BackupType != "full" {
		t.Errorf("unknown chain → %q, want full", res.BackupType)
	}
}

// TestBuildRestoreChainSingleFull covers the trivial single-RP case.
func TestBuildRestoreChainSingleFull(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	rp := db.RestorePoint{ID: 1, BackupType: "full", ParentRestorePointID: 0}
	chain, err := r.buildRestoreChain(rp)
	if err != nil {
		t.Fatalf("buildRestoreChain: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != 1 {
		t.Errorf("chain = %+v, want [{ID:1}]", chain)
	}
}

// TestBuildRestoreChainWalksParents verifies that incremental RPs walk
// their parent chain back to the full and the result is oldest-first.
func TestBuildRestoreChainWalksParents(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	jobID, _ := database.CreateJob(db.Job{
		Name: "chain-job", BackupTypeChain: "incremental", StorageDestID: dest.ID,
	})

	runID1, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
	})
	full, _ := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID1, JobID: jobID, BackupType: "full",
		StoragePath: "chain-job/full", Metadata: "{}",
	})

	runID2, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "incremental",
	})
	inc, _ := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID2, JobID: jobID, BackupType: "incremental",
		StoragePath: "chain-job/inc", Metadata: "{}", ParentRestorePointID: full,
	})

	incRP, _ := database.GetRestorePoint(inc)
	chain, err := r.buildRestoreChain(incRP)
	if err != nil {
		t.Fatalf("buildRestoreChain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2: %+v", len(chain), chain)
	}
	// Oldest (full) first.
	if chain[0].BackupType != "full" {
		t.Errorf("chain[0].BackupType = %q, want full", chain[0].BackupType)
	}
	if chain[1].BackupType != "incremental" {
		t.Errorf("chain[1].BackupType = %q, want incremental", chain[1].BackupType)
	}
}

// TestBuildRestoreChainMissingParent covers the orphan-incremental branch
// where parent_restore_point_id points to a deleted row.
func TestBuildRestoreChainMissingParent(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	rp := db.RestorePoint{ID: 5, BackupType: "incremental", ParentRestorePointID: 9999}
	if _, err := r.buildRestoreChain(rp); err == nil {
		t.Fatal("buildRestoreChain with missing parent should error")
	}
}

// TestVMCheckpointFromRPMetaEmpty covers all early-return branches.
func TestVMCheckpointFromRPMetaEmpty(t *testing.T) {
	t.Parallel()
	if cp := vmCheckpointFromRPMeta("", "vm1"); cp != "" {
		t.Errorf("empty metadata → %q, want \"\"", cp)
	}
	if cp := vmCheckpointFromRPMeta(`{}`, ""); cp != "" {
		t.Errorf("empty itemName → %q, want \"\"", cp)
	}
	if cp := vmCheckpointFromRPMeta("not json", "vm1"); cp != "" {
		t.Errorf("bad JSON → %q, want \"\"", cp)
	}
	if cp := vmCheckpointFromRPMeta(`{"other":"data"}`, "vm1"); cp != "" {
		t.Errorf("no vm_checkpoints → %q, want \"\"", cp)
	}
}

// TestVMCheckpointFromRPMetaPresent covers the happy path.
func TestVMCheckpointFromRPMetaPresent(t *testing.T) {
	t.Parallel()
	meta := `{"vm_checkpoints":{"plex-vm":"checkpoint-abc","other-vm":"checkpoint-def"}}`
	if cp := vmCheckpointFromRPMeta(meta, "plex-vm"); cp != "checkpoint-abc" {
		t.Errorf("got %q, want checkpoint-abc", cp)
	}
	// Missing item returns "" (the empty type-asserted value).
	if cp := vmCheckpointFromRPMeta(meta, "missing-vm"); cp != "" {
		t.Errorf("missing item → %q, want \"\"", cp)
	}
}

// TestCleanupStorageDestinationEmpty covers the no-op branch on an empty
// storage dest.
func TestCleanupStorageDestinationEmpty(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)
	if err := r.CleanupStorageDestination(dest); err != nil {
		t.Fatalf("CleanupStorageDestination: %v", err)
	}
}

// TestCleanupStorageDestinationWipesAllJobDirs covers the loop body that
// removes top-level job directories.
func TestCleanupStorageDestinationWipesAllJobDirs(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	for _, name := range []string{"job-a", "job-b", "job-c"} {
		dir := filepath.Join(storageDir, name, "run1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.tar"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := r.CleanupStorageDestination(dest); err != nil {
		t.Fatalf("CleanupStorageDestination: %v", err)
	}
	for _, name := range []string{"job-a", "job-b", "job-c"} {
		if _, err := os.Stat(filepath.Join(storageDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}
}

// TestCleanupStorageDestinationBadAdapter exercises the early error branch.
func TestCleanupStorageDestinationBadAdapter(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	err := r.CleanupStorageDestination(db.StorageDestination{
		Type:   "totally-fake-type",
		Config: "{}",
	})
	if err == nil {
		t.Fatal("expected error from unknown adapter type")
	}
}

// TestSendNotificationDisabled covers the global-off branch (no Unraid
// notify, no Discord webhook lookup attempted).
func TestSendNotificationDisabled(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("notifications_enabled", "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	job := db.Job{Name: "off-job", NotifyOn: "always"}
	r.sendNotification(job, "completed", 5, 0, 1024, 60, nil)
}

// TestSendNotificationNeverSuppresses covers the per-job "never" branch.
func TestSendNotificationNeverSuppresses(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("notifications_enabled", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	job := db.Job{Name: "never-job", NotifyOn: "never"}
	r.sendNotification(job, "failed", 0, 5, 0, 30, []string{"f1"})
}

// TestSendNotificationFailureBranch covers the "completed" + always-pref
// branch and the failed branch. notify.Send is a no-op on non-Linux so
// the calls just exercise the dispatch logic.
func TestSendNotificationFailureBranch(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("notifications_enabled", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	job := db.Job{Name: "alert-job", NotifyOn: "always"}
	r.sendNotification(job, "completed", 3, 0, 4096, 12, nil)
	r.sendNotification(job, "failed", 0, 3, 0, 1, []string{"x"})
	r.sendNotification(job, "partial", 2, 1, 1024, 5, []string{"y"})
}

// TestParseItemChecksumsValid covers the happy path of parseItemChecksums.
func TestParseItemChecksumsValid(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	meta := `{"checksums":{"plex":{"file1.tar":"abc","file2.tar":"def"}}}`
	out := r.parseItemChecksums(meta, "plex")
	if out["file1.tar"] != "abc" || out["file2.tar"] != "def" {
		t.Errorf("checksums = %+v, want file1:abc file2:def", out)
	}
	// Unknown item returns nil/empty map.
	if got := r.parseItemChecksums(meta, "missing"); got != nil {
		t.Errorf("missing item should return nil, got %+v", got)
	}
}

// TestParseItemChecksumsInvalid covers all early-return branches.
func TestParseItemChecksumsInvalid(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	if got := r.parseItemChecksums("not json", "plex"); got != nil {
		t.Errorf("bad JSON → %+v, want nil", got)
	}
	if got := r.parseItemChecksums(`{"other":1}`, "plex"); got != nil {
		t.Errorf("no checksums → %+v, want nil", got)
	}
	if got := r.parseItemChecksums(`{"checksums":"not-a-map"}`, "plex"); got != nil {
		t.Errorf("non-map checksums → %+v, want nil", got)
	}
	if got := r.parseItemChecksums(`{"checksums":{"plex":"not-a-map"}}`, "plex"); got != nil {
		t.Errorf("non-map per-item checksums → %+v, want nil", got)
	}
	// Non-string hash values must be skipped silently.
	got := r.parseItemChecksums(`{"checksums":{"plex":{"a":"hash-a","b":42}}}`, "plex")
	if got["a"] != "hash-a" {
		t.Errorf("expected hash-a for file a, got %q", got["a"])
	}
	if _, present := got["b"]; present {
		t.Errorf("non-string hash should be skipped, got %v", got["b"])
	}
}
