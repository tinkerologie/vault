package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunSummaryMessageSeparator is table-driven over the backup and restore
// kinds: the terminal summary must separate the human-readable verb
// ("Backup"/"Restore" + "finished") from the key=value metadata with a comma,
// never glue "status=" directly onto the verb (#328 QA round 8 #2).
func TestRunSummaryMessageSeparator(t *testing.T) {
	cases := []struct {
		name string
		kind string
	}{
		{name: "backup summary", kind: "Backup"},
		{name: "restore summary", kind: "Restore"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, msg, _ := runSummaryMessage(tc.kind, "", "partial", 1, 1, 4, 1024, time.Minute)

			want := tc.kind + " finished, status=partial, items=1/4, failed=1"
			if !strings.Contains(msg, want) {
				t.Fatalf("summary %q missing %q", msg, want)
			}
			if strings.Contains(msg, tc.kind+" finished status=") {
				t.Fatalf("summary %q still glues status= onto the verb", msg)
			}
		})
	}
}

// TestRunLogNarrationSeparators drives a real two-item folder backup and
// asserts, table-driven, that every run-log narration line separates the
// human-readable message from its key=value metadata with a comma (#328 QA
// round 8 #1 + #2): the per-item "Backed up" and "Uploaded" lines, and the
// terminal "finished" summary. (The run-level "started" line was removed as a
// duplicate of the activity-feed entry and is no longer part of the run-log.)
func TestRunLogNarrationSeparators(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	storageDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "sep-" + nextUniqueRunner(t), Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:            "sep-job-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
		Compression:     "none",
		Encryption:      "none",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for _, name := range []string{"src-a", "src-b"} {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, name+".txt"), []byte("content"), 0o644); err != nil {
			t.Fatalf("setup source %s: %v", name, err)
		}
		itemSettings, _ := json.Marshal(map[string]any{"path": src})
		if _, err := d.AddJobItem(db.JobItem{
			JobID: jobID, ItemType: "folder", ItemName: name, Settings: string(itemSettings),
		}); err != nil {
			t.Fatalf("add job item %s: %v", name, err)
		}
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
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")

	// Each row is one assertion about the joined run-log output: a fragment
	// that must be present (want true) or must be absent (want false).
	cases := []struct {
		name     string
		fragment string
		want     bool
	}{
		{name: "per-item backed up line uses a comma", fragment: "Backed up src-a (folder), size=", want: true},
		{name: "per-item backed up line uses a comma (second item)", fragment: "Backed up src-b (folder), size=", want: true},
		{name: "uploaded line uses a comma", fragment: "Uploaded src-a, file=", want: true},
		{name: "terminal summary uses a comma", fragment: "Backup finished, job=", want: true},
		{name: "backed up line does not glue size= onto the message", fragment: "Backed up src-a (folder) size=", want: false},
		{name: "backed up line does not glue size= onto the message (second item)", fragment: "Backed up src-b (folder) size=", want: false},
		{name: "uploaded line does not glue file= onto the message", fragment: "Uploaded src-a file=", want: false},
		{name: "terminal summary does not glue status= onto the verb", fragment: "Backup finished status=", want: false},
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
