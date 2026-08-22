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

// TestRunJob_PerItemBackedUpLineSuppressedForSingleItem verifies QA
// requirement #2 (duplicate logs): a single-item run must NOT emit the
// per-item "Backed up ... item=1/1" line (the terminal "Backup finished"
// summary already carries status/size/duration/items), while a multi-item
// run must still emit each per-item completion line (#328).
func TestRunJob_PerItemBackedUpLineSuppressedForSingleItem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		itemCount    int
		wantBackedUp []string // expected "item=N/M" suffixes on "Backed up" lines
	}{
		{
			name:         "single item suppresses per-item line",
			itemCount:    1,
			wantBackedUp: nil,
		},
		{
			name:         "two items keep both per-item lines",
			itemCount:    2,
			wantBackedUp: []string{"item=1/2", "item=2/2"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, d := newTestRunner(t)

			storageDir := filepath.Join(t.TempDir(), "store")
			if err := os.MkdirAll(storageDir, 0o755); err != nil {
				t.Fatalf("mkdir storage: %v", err)
			}
			destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
			destID, err := d.CreateStorageDestination(db.StorageDestination{
				Name:   "single-" + nextUniqueRunner(t),
				Type:   "local",
				Config: string(destCfg),
			})
			if err != nil {
				t.Fatalf("create dest: %v", err)
			}

			jobID, err := d.CreateJob(db.Job{
				Name:            "single-item-" + nextUniqueRunner(t),
				StorageDestID:   destID,
				BackupTypeChain: "full",
				Enabled:         true,
				Compression:     "none",
				Encryption:      "none",
			})
			if err != nil {
				t.Fatalf("create job: %v", err)
			}

			for i := 0; i < tc.itemCount; i++ {
				src := t.TempDir()
				if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("content"), 0o644); err != nil {
					t.Fatalf("setup source: %v", err)
				}
				itemSettings, _ := json.Marshal(map[string]any{"path": src})
				if _, err := d.AddJobItem(db.JobItem{
					JobID:    jobID,
					ItemType: "folder",
					ItemName: "src-" + string(rune('a'+i)),
					Settings: string(itemSettings),
				}); err != nil {
					t.Fatalf("add job item %d: %v", i, err)
				}
			}

			// RunJob is synchronous.
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

			var backedUp []string
			var hasSummary bool
			for _, e := range entries {
				if strings.Contains(e.Message, "Backup finished, job=") {
					hasSummary = true
				}
				if strings.HasPrefix(e.Message, "Backed up ") {
					backedUp = append(backedUp, e.Message)
				}
			}

			if !hasSummary {
				t.Errorf("missing terminal \"Backup finished\" summary; got %d entries", len(entries))
			}

			if len(tc.wantBackedUp) == 0 {
				if len(backedUp) != 0 {
					t.Errorf("single-item run emitted per-item line(s): %q", backedUp)
				}
				return
			}

			if len(backedUp) != len(tc.wantBackedUp) {
				t.Fatalf("expected %d per-item line(s), got %d: %q", len(tc.wantBackedUp), len(backedUp), backedUp)
			}
			for _, want := range tc.wantBackedUp {
				found := false
				for _, got := range backedUp {
					if strings.Contains(got, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing per-item line containing %q; got: %q", want, backedUp)
				}
			}
		})
	}
}
