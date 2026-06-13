package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunJob_AllItemsMissingFails verifies issue #138: when every configured
// backup target no longer exists on the system (here a folder whose path is
// gone), the job must record a "failed" run with a descriptive error instead
// of silently "completing" with zero items backed up.
func TestRunJob_AllItemsMissingFails(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	storageDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "missing-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "missing-folder-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
		Compression:     "none",
		Encryption:      "none",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Folder item pointing at a path that does not exist → StatusMissing.
	gonePath := filepath.Join(t.TempDir(), "does-not-exist")
	itemSettings, _ := json.Marshal(map[string]any{"path": gonePath})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "ghost-folder",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("list job runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 job run, got %d", len(runs))
	}
	run := runs[0]
	if run.Status != "failed" {
		t.Errorf("expected status %q, got %q", "failed", run.Status)
	}
	if run.ItemsTotal != 1 {
		t.Errorf("expected ItemsTotal 1, got %d", run.ItemsTotal)
	}
	if run.ItemsFailed != 1 {
		t.Errorf("expected ItemsFailed 1, got %d", run.ItemsFailed)
	}
	if run.Log == "" {
		t.Errorf("expected a descriptive error log, got empty")
	}
	if !strings.Contains(run.Log, "ghost-folder") {
		t.Errorf("expected error log to name the missing item %q, got: %s", "ghost-folder", run.Log)
	}

	// No restore point should be produced for a fully-missing job.
	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 0 {
		t.Errorf("expected 0 restore points, got %d", len(rps))
	}
}
