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

// TestChainPruneRefusesSymlinkEscape verifies the prune pass cannot be
// steered outside the restore destination by a tampered index sidecar whose
// entries route through a symlinked directory (CodeQL go/path-injection
// hardening: all prune I/O is os.Root-scoped).
func TestChainPruneRefusesSymlinkEscape(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "prune-escape-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	jobID, err := d.CreateJob(db.Job{
		Name: "prune-escape", StorageDestID: destID, BackupTypeChain: "incremental", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.RunJob(jobID) // full
	if err := os.WriteFile(filepath.Join(sourceDir, "keep.txt"), []byte("keep2"), 0o644); err != nil {
		t.Fatalf("touch: %v", err)
	}
	r.RunJob(jobID) // incremental

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list rps: %v", err)
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
	chain, err := r.BuildRestoreChain(inc)
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}

	// Tamper the base full's index sidecar: add an entry that routes through
	// a symlinked directory inside the restore destination.
	fullRP := chain[0]
	sidecars, _ := filepath.Glob(filepath.Join(storageDir, fullRP.StoragePath, "src", "*.index.json"))
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 index sidecar for the full, got %v", sidecars)
	}
	raw, err := os.ReadFile(sidecars[0])
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var idx map[string]any
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("unmarshal sidecar: %v", err)
	}
	files, _ := idx["files"].([]any)
	files = append(files, map[string]any{"path": "link/pwn.txt", "size": 4, "mode": "0644", "modtime": "2026-01-01T00:00:00Z"})
	idx["files"] = files
	tampered, _ := json.Marshal(idx)
	if err := os.WriteFile(sidecars[0], tampered, 0o644); err != nil {
		t.Fatalf("write tampered sidecar: %v", err)
	}

	// Restore destination containing a symlink that points OUTSIDE, with a
	// victim file matching the tampered entry's size and a fresh mtime.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "pwn.txt"), []byte("pwnd"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	restoreDir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(restoreDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := r.RestoreItem(inc, "src", "folder", restoreDir, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); err != nil {
		t.Fatalf("prune escaped through the symlink and removed the outside file: %v", err)
	}
}
