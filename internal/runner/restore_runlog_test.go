package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunRestore_RunLogLines drives a real backup + restore cycle end to end
// (classic tar, folder item, incremental chain) and asserts the restore run
// log narrates its phases: engine restore milestones mirrored from the
// handler, chain replay steps, and the download phase (issue #328 QA).
func TestRunRestore_RunLogLines(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	storageDir := t.TempDir()
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "restore-rl", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:            "restore-rl-job",
		StorageDestID:   destID,
		BackupTypeChain: "incremental",
		Compression:     "none",
		Encryption:      "none",
		Enabled:         true,
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

	r.RunJob(jobID) // base full
	r.RunJob(jobID) // incremental

	rps, err := d.ListRestorePoints(jobID) // newest-first (created_at DESC)
	if err != nil || len(rps) < 2 {
		t.Fatalf("expected >= 2 restore points, got %d (err=%v)", len(rps), err)
	}
	var inc db.RestorePoint
	for _, rp := range rps {
		if rp.BackupType == "incremental" {
			inc = rp
		}
	}
	if inc.ID == 0 {
		t.Fatal("no incremental restore point")
	}

	destPath := t.TempDir()
	r.RunRestore(inc, []RestoreTarget{{Name: "src", Type: "folder"}}, destPath, "")
	if _, err := os.Stat(filepath.Join(destPath, "keep.txt")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("GetJobRuns: %v", err)
	}
	var restoreRunID int64
	for _, run := range runs {
		if run.RunType == "restore" {
			restoreRunID = run.ID
		}
	}
	if restoreRunID == 0 {
		t.Fatal("no restore run found")
	}

	entries, err := d.ListRunLogEntries(context.Background(), restoreRunID, 0, 500)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")
	cases := []struct {
		name string
		want string
	}{
		{name: "per-item start narrated", want: "Restoring src (folder) — item 1 of 1"},
		{name: "metadata read milestone narrated", want: "src: reading metadata"},                  // FolderHandler.Restore milestone (Task 6)
		{name: "completion milestone narrated", want: "src: restore complete"},                     // FolderHandler.Restore milestone (Task 6)
		{name: "chain step 1 narrated", want: "Restore chain step 1/2"},                            // Task 7
		{name: "chain step 2 narrated", want: "Restore chain step 2/2"},                            // Task 7
		{name: "chain replay summary narrated", want: "Restore chain replayed for src: 2 step(s)"}, // Task 7
		{name: "download phase start narrated", want: "Downloading src:"},                          // Task 8
		{name: "download phase per-file narrated", want: "Downloaded src, file="},                  // Task 8
		{name: "download phase ready narrated", want: "Restore data ready for src:"},               // Task 8
		{name: "per-item completion narrated", want: "Restored src (folder) in"},
		{name: "terminal summary narrated", want: "Restore finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("run log missing %q; got:\n%s", tc.want, joined)
			}
		})
	}
}
