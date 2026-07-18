package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestChainRestoreNotifiesOnce verifies that restoring an incremental restore
// point sends exactly one "restore completed" notification/activity entry for
// the item, not one per chain step. Regression test for duplicate
// "Restore of 'X' completed" Unraid notifications during chain restores.
func TestChainRestoreNotifiesOnce(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	r, d := newTestRunner(t)

	destCfg, err := json.Marshal(map[string]string{"path": storageDir})
	if err != nil {
		t.Fatalf("marshal dest config: %v", err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "notify-once-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "notify-once", StorageDestID: destID,
		BackupTypeChain: "incremental", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemSettings, err := json.Marshal(map[string]any{"path": sourceDir})
	if err != nil {
		t.Fatalf("marshal item settings: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.RunJob(jobID) // first run: full
	if err := os.WriteFile(filepath.Join(sourceDir, "b.txt"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	r.RunJob(jobID) // second run: incremental

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	var inc db.RestorePoint
	for _, rp := range rps {
		if rp.BackupType == "incremental" {
			inc = rp
		}
	}
	if inc.ID == 0 {
		t.Fatalf("no incremental restore point created; got %+v", rps)
	}

	if err := r.RestoreItem(inc, "src", "folder", t.TempDir(), ""); err != nil {
		t.Fatalf("restore item: %v", err)
	}

	logs, err := d.ListActivityLogs(100, "restore")
	if err != nil {
		t.Fatalf("list activity logs: %v", err)
	}
	count := 0
	for _, e := range logs {
		if e.Message == "Restore completed: src" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 restore-completed activity entry, got %d", count)
	}
}
