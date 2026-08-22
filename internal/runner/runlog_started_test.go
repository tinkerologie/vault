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

// TestBackupStartedNarrationSeparator asserts the activity feed narrates a
// run's start with the comma-separated phrase "Backup started, job=…" (never
// gluing job= directly onto "started"), and that the run-log stream does NOT
// emit its own duplicate "Backup started" line (#328 QA round 8 #1; the
// run-log "started" line was removed as a duplicate of the activity entry).
func TestBackupStartedNarrationSeparator(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	storageDir := t.TempDir()
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "started-" + nextUniqueRunner(t), Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "started-job-" + nextUniqueRunner(t), StorageDestID: destID,
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

	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil {
		t.Fatalf("GetJobRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	runID := runs[0].ID

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var runLogMsgs []string
	for _, e := range entries {
		runLogMsgs = append(runLogMsgs, e.Message)
	}

	activities, err := d.ListActivityLogs(200, "backup", 0)
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	var activityMsgs []string
	for _, a := range activities {
		activityMsgs = append(activityMsgs, a.Message)
	}

	// One row per stream under test: fragments the stream must contain and
	// fragments it must not. The activity feed narrates the start with the
	// comma-separated phrase; the run-log stream must NOT carry its own
	// duplicate "Backup started" line (the activity feed is the single
	// source of the start narration).
	cases := []struct {
		name   string
		msgs   []string
		want   []string
		banned []string
	}{
		{
			name:   "activity feed narrates the start with a comma",
			msgs:   activityMsgs,
			want:   []string{"Backup started, job="},
			banned: []string{"Backup started job="},
		},
		{
			name:   "run-log stream has no duplicate started line",
			msgs:   runLogMsgs,
			banned: []string{"Backup started"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(tc.msgs, "\n")
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q; got:\n%s", w, joined)
				}
			}
			for _, b := range tc.banned {
				if strings.Contains(joined, b) {
					t.Errorf("banned fragment %q present; got:\n%s", b, joined)
				}
			}
		})
	}
}
