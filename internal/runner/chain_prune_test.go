package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestChainRestoreDoesNotResurrect verifies the issue #231 fix: a file
// deleted from the source (or newly added to exclude_paths) after the base
// full backup must NOT reappear when restoring a newer incremental point.
// Classic tar chains prune via the authoritative listing sidecar; dedup
// chains restore the selected point's complete manifest directly.
func TestChainRestoreDoesNotResurrect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dedup bool
	}{
		{name: "classic_tar", dedup: false},
		{name: "dedup_chunked", dedup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageDir := t.TempDir()
			sourceDir := t.TempDir()
			for name, content := range map[string]string{
				"keep.txt":       "keep",
				"delete-me.txt":  "doomed",
				"exclude-me.log": "noisy",
			} {
				if err := os.WriteFile(filepath.Join(sourceDir, name), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			r, d := newTestRunner(t)
			if tc.dedup {
				r.serverKey = testServerKey()
			}
			destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
			dest := db.StorageDestination{
				Name: "prune-local", Type: "local", Config: string(destCfg),
			}
			if tc.dedup {
				dest.DedupEnabled = true
			}
			destID, err := d.CreateStorageDestination(dest)
			if err != nil {
				t.Fatalf("create dest: %v", err)
			}
			jobID, err := d.CreateJob(db.Job{
				Name: "prune-job", StorageDestID: destID,
				BackupTypeChain: "incremental", Enabled: true,
			})
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
			itemID, err := d.AddJobItem(db.JobItem{
				JobID: jobID, ItemType: "folder", ItemName: "src",
				Settings: string(itemSettings),
			})
			if err != nil {
				t.Fatalf("add item: %v", err)
			}

			r.RunJob(jobID) // base full — captures all three files

			// Delete one file, exclude another, touch a third so the
			// increment has content.
			if err := os.Remove(filepath.Join(sourceDir, "delete-me.txt")); err != nil {
				t.Fatalf("delete file: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep-v2"), 0o644); err != nil {
				t.Fatalf("touch keep: %v", err)
			}
			newSettings, _ := json.Marshal(map[string]any{
				"path": sourceDir, "exclude_paths": []string{"*.log"},
			})
			if err := d.UpdateJobItemSettings(itemID, string(newSettings)); err != nil {
				t.Fatalf("update item settings: %v", err)
			}

			r.RunJob(jobID) // incremental

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
				t.Fatalf("no incremental restore point; got %+v", rps)
			}

			restoreDir := t.TempDir()
			if err := r.RestoreItem(inc, "src", "folder", restoreDir, ""); err != nil {
				t.Fatalf("restore: %v", err)
			}

			if _, err := os.Stat(filepath.Join(restoreDir, "keep.txt")); err != nil {
				t.Errorf("keep.txt should be present: %v", err)
			}
			for _, gone := range []string{"delete-me.txt", "exclude-me.log"} {
				if _, err := os.Stat(filepath.Join(restoreDir, gone)); err == nil {
					t.Errorf("%s was resurrected by the chain restore", gone)
				}
			}
		})
	}
}
