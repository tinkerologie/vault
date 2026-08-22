package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestTerminalDetails_RunLogFlag: terminalDetails always stamps the run_log
// flag that gates run-log expansion in the UI (#328 r3).
func TestTerminalDetails_RunLogFlag(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
	}{
		{"empty", map[string]any{}},
		{"with fields", map[string]any{"job_id": 1, "run_id": 2, "status": "completed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := terminalDetails(tc.in)
			if !strings.Contains(got, `"run_log":true`) {
				t.Errorf("terminalDetails(%v) = %q, want it to contain %q", tc.in, got, `"run_log":true`)
			}
		})
	}
}

// TestBackupRun_WritesTerminalActivityEntry: a completed backup must write a
// terminal activity row (category backup) whose details carry run_log:true,
// so the UI expands the run-log stream on reload (#328 r3 regression).
func TestBackupRun_WritesTerminalActivityEntry(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	storageDir := t.TempDir()
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "terminal-rl", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "terminal-rl-job", StorageDestID: destID,
		BackupTypeChain: "full", Compression: "none", Encryption: "none", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.RunJob(jobID)

	logs, err := d.ListActivityLogs(200, "backup", 0)
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}

	// Single-scenario integration test: a completed backup must write a
	// terminal activity row whose details carry run_log:true, so the UI
	// expands the run-log stream on reload (#328 r3 regression).
	cases := []struct {
		name string
	}{
		{name: "completed backup writes a terminal (run_log:true) activity entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, l := range logs {
				if strings.Contains(l.Details, `"run_log":true`) {
					return // terminal marker present
				}
			}
			t.Fatal("completed backup wrote no terminal (run_log:true) activity entry")
		})
	}
}
