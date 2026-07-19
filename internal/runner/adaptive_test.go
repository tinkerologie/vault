package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestAdaptiveGatePostponesBusyFolder verifies the issue #240 pre-run idle
// gate: a scheduled run of an adaptive job whose folder was just written to
// is recorded as "postponed" and does not back up; once the workload counts
// as idle the next run completes normally.
func TestAdaptiveGatePostponesBusyFolder(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "hot.txt"), []byte("busy"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "adaptive-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	jobID, err := d.CreateJob(db.Job{
		Name: "adaptive-job", StorageDestID: destID,
		BackupTypeChain: "full", Enabled: true, AdaptiveEnabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	// The file was written milliseconds ago — well inside the default
	// 5-minute folder idle window, so the run must be postponed.
	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil {
		t.Fatalf("get runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "postponed" {
		t.Fatalf("expected 1 postponed run, got %+v", runs)
	}
	if runs[0].Log == "" {
		t.Fatal("postponed run should carry a human-readable reason")
	}

	// Declare "changed within the last 0 minutes" as the busy window — the
	// file no longer counts as recent, so the run proceeds.
	if err := d.SetSetting("adaptive_folder_idle_minutes", "0"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	r.RunJob(jobID)

	runs, err = d.GetJobRuns(jobID, 5)
	if err != nil {
		t.Fatalf("get runs: %v", err)
	}
	completed := false
	for _, run := range runs {
		if run.Status == "completed" {
			completed = true
		}
	}
	if len(runs) < 2 || !completed {
		t.Fatalf("expected a completed run after workload went idle, got %+v", runs)
	}
}
