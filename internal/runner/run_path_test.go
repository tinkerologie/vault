package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// Dropping the run ID from the run folder (issue #319) removed the only thing
// that made the name unique. Two back-to-back manual runs of a fast job share
// a second, and the storage adapters overwrite silently — so a collision would
// destroy the earlier run's data with no error anywhere.
func TestUniqueRunPath(t *testing.T) {
	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	dest := db.StorageDestination{Name: "local", Type: "local", Config: string(cfg)}

	base := "nightly/2026-09-15_143000"

	if got := uniqueRunPath(dest, base, 7); got != base {
		t.Errorf("a free path should be used as-is, got %q", got)
	}

	// Materialise the first run, then ask again.
	occupy := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(storageDir, p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storageDir, p, "manifest.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	occupy(base)
	second := uniqueRunPath(dest, base, 7)
	if second == base {
		t.Fatal("a taken path must not be handed out again — the earlier run would be overwritten")
	}
	if second != base+"-2" {
		t.Errorf("got %q, want %q", second, base+"-2")
	}

	occupy(second)
	third := uniqueRunPath(dest, base, 7)
	if third != base+"-3" {
		t.Errorf("got %q, want %q", third, base+"-3")
	}

	// An unusable destination must not block the backup — but it must not
	// hand out the plain name either, because nothing has proven that name
	// free. The run ID is unique by construction, so it becomes the suffix.
	broken := db.StorageDestination{Name: "broken", Type: "definitely-not-a-provider", Config: "{}"}
	if got := uniqueRunPath(broken, base, 42); got != base+"-r42" {
		t.Errorf("got %q, want the run-ID fallback %q", got, base+"-r42")
	}

	// Same rule when the destination exists but cannot be listed at all: a
	// local destination whose root has been removed underneath it.
	goneDir := filepath.Join(t.TempDir(), "gone")
	goneCfg, _ := json.Marshal(map[string]string{"path": goneDir})
	gone := db.StorageDestination{Name: "gone", Type: "local", Config: string(goneCfg)}
	if got := uniqueRunPath(gone, base, 43); got != base+"-r43" {
		t.Errorf("got %q, want the run-ID fallback %q", got, base+"-r43")
	}

	// And when the same-second collisions never stop, the run ID breaks the
	// tie rather than the search silently reusing a taken folder.
	occupy(base + "-3")
	for attempt := 4; attempt <= maxRunPathAttempts; attempt++ {
		occupy(fmt.Sprintf("%s-%d", base, attempt))
	}
	// An ambiguous error (e.g. permission error rather than NotExist) on candidate
	// must fall back to the run ID instead of assuming the candidate is free.
	unreadableParent := t.TempDir()
	unreadableDir := filepath.Join(unreadableParent, "unreadable")
	if err := os.MkdirAll(unreadableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	candPath := filepath.Join(unreadableDir, "nightly", "2026-09-15_143000")
	if err := os.MkdirAll(candPath, 0o000); err == nil {
		t.Cleanup(func() { _ = os.Chmod(candPath, 0o755) })
		unreadableCfg, _ := json.Marshal(map[string]string{"path": unreadableDir})
		unreadableDest := db.StorageDestination{Name: "unreadable", Type: "local", Config: string(unreadableCfg)}
		if got := uniqueRunPath(unreadableDest, base, 45); got != base+"-r45" {
			t.Errorf("got %q, want fallback %q on ambiguous error", got, base+"-r45")
		}
	}
}

// The end-to-end shape: a completed run's storage path names the job and the
// time it ran, and nothing else.
func TestRunJobStoragePathHasNoRunIDPrefix(t *testing.T) {
	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{
		Name: "nightly", StorageDestID: dest.ID, BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	settings, _ := json.Marshal(map[string]any{"path": source})
	if _, err := database.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings),
	}); err != nil {
		t.Fatalf("AddJobItem: %v", err)
	}

	r.RunJob(jobID)

	rps, err := database.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("ListRestorePoints: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}
	path := rps[0].StoragePath
	prefix := "nightly/"
	if !strings.HasPrefix(path, prefix) {
		t.Fatalf("storage path %q should start with the job name", path)
	}
	folder := strings.TrimPrefix(path, prefix)
	// "2026-09-15_143000" — a date, not "<id>_<date>".
	if !strings.HasPrefix(folder, "20") || strings.Count(folder, "_") != 1 {
		t.Errorf("run folder %q should be a bare timestamp, with no run ID prefix", folder)
	}
}
