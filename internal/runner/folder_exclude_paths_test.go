package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

type folderExclusionCase struct {
	name         string
	dedup        bool
	files        map[string]string // relative path -> content
	excludePaths []string
	wantPresent  []string
	wantAbsent   []string
	omitKey      bool // if true, do not set exclude_paths in settings at all
}

// TestFolderJobPropagatesExcludePaths verifies that a folder job's
// exclude_paths setting is copied from the stored item settings into the
// engine BackupItem, so exclusions are actually applied during the run.
// Regression test for the runner gap where only containers had this wiring.
func TestFolderJobPropagatesExcludePaths(t *testing.T) {
	cases := []folderExclusionCase{
		{
			name: "classic tar root globs",
			files: map[string]string{
				"keep.txt":  "keep",
				"app.log":   "noise",
				"debug.log": "noise",
			},
			excludePaths: []string{"*.log"},
			wantPresent:  []string{"keep.txt"},
			wantAbsent:   []string{"app.log", "debug.log"},
		},
		{
			name:  "dedup chunked root globs",
			dedup: true,
			files: map[string]string{
				"keep.txt":  "keep",
				"app.log":   "noise",
				"debug.log": "noise",
			},
			excludePaths: []string{"*.log"},
			wantPresent:  []string{"keep.txt"},
			wantAbsent:   []string{"app.log", "debug.log"},
		},
		{
			name: "sub-directory exclusion",
			files: map[string]string{
				"keep.txt":       "keep",
				"data/file.txt":  "keep",
				"logs/app.log":   "noise",
				"logs/debug.log": "noise",
			},
			excludePaths: []string{"logs"},
			wantPresent:  []string{"keep.txt", "data/file.txt"},
			wantAbsent:   []string{"logs"},
		},
		{
			name: "no exclusions preserves all files",
			files: map[string]string{
				"keep.txt": "keep",
				"app.log":  "noise",
			},
			omitKey:     true,
			wantPresent: []string{"keep.txt", "app.log"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runFolderExclusionCase(t, tc)
		})
	}
}

func runFolderExclusionCase(t *testing.T, tc folderExclusionCase) {
	t.Helper()
	storageDir := t.TempDir()
	sourceDir := t.TempDir()

	// Build source tree.
	for relPath, content := range tc.files {
		fullPath := filepath.Join(sourceDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("create dir %s: %v", filepath.Dir(fullPath), err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	r, d := newTestRunner(t)
	if tc.dedup {
		r.serverKey = testServerKey()
	}

	destCfg, err := json.Marshal(map[string]string{"path": storageDir})
	if err != nil {
		t.Fatalf("marshal dest config: %v", err)
	}
	dest := db.StorageDestination{
		Name: "test-local", Type: "local", Config: string(destCfg),
	}
	if tc.dedup {
		dest.DedupEnabled = true
	}
	destID, err := d.CreateStorageDestination(dest)
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "test-folder-exclusions", StorageDestID: destID,
		BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettingsMap := map[string]any{"path": sourceDir}
	if !tc.omitKey {
		itemSettingsMap["exclude_paths"] = tc.excludePaths
	}
	itemSettings, err := json.Marshal(itemSettingsMap)
	if err != nil {
		t.Fatalf("marshal item settings: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("no run record created")
	}
	if runs[0].Status != "completed" {
		t.Fatalf("expected run status completed, got %q", runs[0].Status)
	}

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("get restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}

	restoreDir := t.TempDir()
	if err := r.RestoreItem(rps[0], "src", "folder", restoreDir, ""); err != nil {
		t.Fatalf("restore item: %v", err)
	}

	for _, relPath := range tc.wantPresent {
		if _, err := os.Stat(filepath.Join(restoreDir, relPath)); err != nil {
			t.Errorf("expected %s to be present in restore, got: %v", relPath, err)
		}
	}
	for _, relPath := range tc.wantAbsent {
		if _, err := os.Stat(filepath.Join(restoreDir, relPath)); err == nil {
			t.Errorf("expected %s to be absent from restore, but it was present", relPath)
		} else if !os.IsNotExist(err) {
			t.Errorf("expected %s to be absent from restore, but stat returned unexpected error: %v", relPath, err)
		}
	}
}
