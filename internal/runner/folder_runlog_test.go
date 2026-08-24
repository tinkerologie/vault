package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// TestStageItemLocally_FolderRunLogMilestones: engine progress milestones
// for folder items are mirrored into the run log, so flash-drive backups
// produce ample output instead of two generic lines (#328 QA).
func TestStageItemLocally_FolderRunLogMilestones(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "vault.cfg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "store")})
	dest := db.StorageDestination{Type: "local", Config: string(cfg)}

	var runID int64
	item := engine.BackupItem{
		Name:        "Flash Drive",
		Type:        "folder",
		Settings:    map[string]any{"path": src, "preset": "flash"},
		Compression: "none",
	}

	// runLog persists to run_log_entries, which carries a foreign key on
	// runs — seed a real job + run so the mirrored milestones can be stored.
	jobID, err := d.CreateJob(db.Job{Name: "folder-runlog"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err = d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	_, _, cleanup, err := r.stageItemLocally(context.Background(), runID, item, dest)
	if err != nil {
		t.Fatalf("stageItemLocally: %v", err)
	}
	cleanup()

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
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
		{name: "archiving milestone narrated", want: "archiving"},
		{name: "metadata milestone narrated", want: "writing folder metadata"},
		{name: "completion milestone narrated", want: "backup complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("run log missing milestone %q; got:\n%s", tc.want, joined)
			}
		})
	}
}

// TestBackupItem_FolderDedupRunLogMilestones: the dedup/chunked backup path
// also mirrors folder milestones into the run log, and the -1 per-file
// heartbeats are suppressed (they would flood) (#328 QA).
func TestBackupItem_FolderDedupRunLogMilestones(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	r.serverKey = testServerKey()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "vault.cfg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	dest := db.StorageDestination{Name: "dedup", Type: "local", Config: string(cfg), DedupEnabled: true}
	destID, err := d.CreateStorageDestination(dest)
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	dest.ID = destID

	jobID, err := d.CreateJob(db.Job{Name: "folder-dedup"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	item := engine.BackupItem{
		Name:        "Flash Drive",
		Type:        "folder",
		Settings:    map[string]any{"path": src, "preset": "flash"},
		Compression: "none",
	}
	if _, _, err := r.backupItem(context.Background(), runID, item, dest, "rp", false, "", "none", 1, nil); err != nil {
		t.Fatalf("backupItem (dedup): %v", err)
	}

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 200)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")

	// Each row is one assertion about the joined run-log output: the
	// completion milestone must be present, per-file -1 heartbeats must be
	// absent (they would flood the log) (#328 QA).
	cases := []struct {
		name     string
		fragment string
		want     bool
	}{
		{name: "completion milestone narrated", fragment: "manifest written", want: true},
		{name: "per-file heartbeat dropped", fragment: "chunked ", want: false},
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
