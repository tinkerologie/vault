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

// TestRunLogItemProgressIsFractionOnly drives a classic folder backup and
// asserts the per-item run-log narration reports position as an "item X/Y"
// fraction only — the redundant "progress=Z%" suffix must be gone (#328 QA
// round 6 #5).
func TestRunLogItemProgressIsFractionOnly(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	// Two items: the single-item per-item "Backed up" line is suppressed
	// (QA #2 — the terminal summary already carries status/size/duration),
	// so the fraction narration is only observable on a multi-item run.
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("setup source: %v", err)
	}
	sourceDir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir2, "b.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("setup source 2: %v", err)
	}
	storageDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "progress-frac-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:            "progress-frac-job-" + nextUniqueRunner(t),
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
	itemSettings2, _ := json.Marshal(map[string]any{"path": sourceDir2})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "src-classic-2",
		Settings: string(itemSettings2),
	}); err != nil {
		t.Fatalf("add job item 2: %v", err)
	}

	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil {
		t.Fatalf("GetJobRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("expected 1 run, got 0")
	}
	runID := runs[0].ID

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")

	// Each row is one assertion about the joined run-log output: the item
	// fractions must be present, the redundant progress percentage must be
	// gone (#328 QA round 6 #5).
	cases := []struct {
		name     string
		fragment string
		want     bool
	}{
		{name: "no progress percentage suffix", fragment: "progress=", want: false},
		{name: "first item fraction present", fragment: "item=1/2", want: true},
		{name: "second item fraction present", fragment: "item=2/2", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Contains(joined, tc.fragment)
			if got != tc.want {
				t.Errorf("run log contains %q = %v, want %v; got:\n%s", tc.fragment, got, tc.want, joined)
			}
		})
	}
}
