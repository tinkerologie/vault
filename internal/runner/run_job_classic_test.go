package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunJob_ClassicFolderBackup drives RunJob through the non-dedup
// classic-tar pipeline for a folder item. This exercises the bulk of
// runJobInternal: queue management, broadcast, item loop, classic
// stageItemLocally + uploadStagedFiles, restore point creation, and
// run finalisation.
func TestRunJob_ClassicFolderBackup(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	// Source folder with one file.
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("classic content"), 0o644); err != nil {
		t.Fatalf("setup source: %v", err)
	}

	storageDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "classic-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "classic-folder-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
		Compression:     "none",
		Encryption:      "none",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "src-classic",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	// RunJob is synchronous.
	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}
	if rps[0].StoragePath == "" {
		t.Errorf("restore point storage path is empty")
	}
}

// TestRunJob_MissingJobIsNoOp drives the early-return branch when the
// job lookup fails (the row never existed in this test's DB).
func TestRunJob_MissingJobIsNoOp(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	// 9999 doesn't exist. The function logs and returns without panicking.
	r.RunJob(9999)
}
