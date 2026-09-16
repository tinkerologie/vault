package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestChainRestoreClearsDestinationOnce is the chain half of issue #321. The
// destination must be cleared before the chain starts replaying — but exactly
// once. Clearing before every step would delete the base full's files as soon
// as the increment replayed on top, leaving only whatever the last step
// happened to contain.
func TestChainRestoreClearsDestinationOnce(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "from-full.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "clean-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "clean-job", StorageDestID: destID,
		BackupTypeChain: "incremental", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatal(err)
	}

	r.RunJob(jobID) // base full

	if err := os.WriteFile(filepath.Join(sourceDir, "from-increment.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.RunJob(jobID) // incremental

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatal(err)
	}
	var inc db.RestorePoint
	for _, rp := range rps {
		if rp.BackupType == "incremental" {
			inc = rp
		}
	}
	if inc.ID == 0 {
		t.Fatalf("no incremental restore point; got %+v", rps)
	}

	// A file that was never in any backup — exactly what issue #321 reported
	// surviving a restore that promised to overwrite.
	restoreDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(restoreDir, "pre-existing.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.RestoreItem(inc, "src", "folder", restoreDir, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, err := os.Stat(filepath.Join(restoreDir, "pre-existing.txt")); !os.IsNotExist(err) {
		t.Error("a file that was never backed up survived a clean restore")
	}
	for _, want := range []string{"from-full.txt", "from-increment.txt"} {
		if _, err := os.Stat(filepath.Join(restoreDir, want)); err != nil {
			t.Errorf("%s is missing — the clearing ran on more than the first chain step: %v", want, err)
		}
	}
}

// TestRestoreItemHonoursCleanDestinationOption pins the scripted/MCP opt-out.
// RestoreItem replaces the destination by default, matching the HTTP API, but
// an automation that relied on the old additive merge must be able to ask for
// it rather than discovering the change by losing files.
func TestRestoreItemHonoursCleanDestinationOption(t *testing.T) {
	merge := false
	cases := []struct {
		name          string
		opts          []RestoreItemOptions
		wantStrayGone bool
	}{
		{name: "no options replaces the destination", wantStrayGone: true},
		{name: "a nil field keeps the default", opts: []RestoreItemOptions{{}}, wantStrayGone: true},
		{name: "an explicit opt-out merges", opts: []RestoreItemOptions{{CleanDestination: &merge}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, database, storageDir := setupTestRunner(t)
			dest := createLocalDest(t, database, storageDir)

			sourceDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(sourceDir, "kept.txt"), []byte("backed up"), 0o644); err != nil {
				t.Fatal(err)
			}

			jobID, err := database.CreateJob(db.Job{
				Name: "clean-option-job", StorageDestID: dest.ID,
				BackupTypeChain: "full", Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := json.Marshal(map[string]any{"path": sourceDir})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.AddJobItem(db.JobItem{
				JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings),
			}); err != nil {
				t.Fatal(err)
			}
			r.RunJob(jobID)

			rps, err := database.ListRestorePoints(jobID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rps) != 1 {
				t.Fatalf("expected 1 restore point, got %d", len(rps))
			}

			// A file the backup knows nothing about, sitting in the target.
			restoreDir := t.TempDir()
			stray := filepath.Join(restoreDir, "stray.txt")
			if err := os.WriteFile(stray, []byte("was already here"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := r.RestoreItem(rps[0], "src", "folder", restoreDir, "", tc.opts...); err != nil {
				t.Fatalf("RestoreItem: %v", err)
			}

			if _, err := os.Stat(filepath.Join(restoreDir, "kept.txt")); err != nil {
				t.Errorf("the restored file should be present: %v", err)
			}
			_, strayErr := os.Stat(stray)
			if tc.wantStrayGone && strayErr == nil {
				t.Error("the destination should have been replaced, but the pre-existing file survived")
			}
			if !tc.wantStrayGone && strayErr != nil {
				t.Errorf("the opt-out should have merged, but the pre-existing file was deleted: %v", strayErr)
			}
		})
	}
}
