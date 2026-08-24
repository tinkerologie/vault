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

// TestMirrorEngineMilestone_ContainerAndFolderPersisted: engine progress
// milestones for container and folder items are persisted to the run log
// (this is what makes a container backup's run log narrate its phases),
// while per-file heartbeats (pct -1) and non-narrated item types (vm, zfs,
// plugin) are dropped (#328 QA).
func TestMirrorEngineMilestone_ContainerAndFolderPersisted(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	jobID, err := d.CreateJob(db.Job{Name: "mirror-runlog"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	r.mirrorEngineMilestone(runID, "container", "Plex", 60, "backing up volumes")
	r.mirrorEngineMilestone(runID, "folder", "Flash Drive", 10, "archiving /boot")
	// Dropped: per-file heartbeats and non-narrated item types.
	r.mirrorEngineMilestone(runID, "container", "Plex", -1, "chunked var/lib/foo.db")
	r.mirrorEngineMilestone(runID, "vm", "Windows", 50, "snapshotting")
	r.mirrorEngineMilestone(runID, "zfs", "pool", 50, "snapshotting")
	r.mirrorEngineMilestone(runID, "plugin", "Fix Common Problems", 50, "archiving")

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")

	// Each row is one assertion about the joined run-log output: a narrated
	// fragment must be present, a dropped fragment must be absent. The
	// entry-count check below is the harness-level guard that only the two
	// narrated milestones were persisted.
	cases := []struct {
		name     string
		fragment string
		want     bool
	}{
		{name: "container milestone narrated", fragment: "Plex: backing up volumes", want: true},
		{name: "folder milestone narrated", fragment: "Flash Drive: archiving /boot", want: true},
		{name: "per-file heartbeat dropped", fragment: "chunked var/lib", want: false},
		{name: "non-narrated item type dropped", fragment: "snapshotting", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Contains(joined, tc.fragment)
			if got != tc.want {
				t.Errorf("run log contains %q = %v, want %v; got:\n%s", tc.fragment, got, tc.want, joined)
			}
		})
	}
	if len(entries) != 2 {
		t.Errorf("run log entries = %d, want 2", len(entries))
	}
}

// TestUploadStagedFiles_RunLogLines: the classic upload phase narrates
// start, per-file completion, and the verify step in the run log (#328 QA).
func TestUploadStagedFiles_RunLogLines(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	jobID, err := d.CreateJob(db.Job{Name: "upload-runlog"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "volume_0.tar"), []byte("payload-A"), 0600); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "store")})
	dest := db.StorageDestination{Type: "local", Config: string(cfg)}

	if _, err := r.uploadStagedFilesN(context.Background(), runID, tmpDir, dest, "rp", true, "", "none", "container", "Plex", 1); err != nil {
		t.Fatalf("uploadStagedFilesN: %v", err)
	}

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
		{name: "upload start narrated", want: "Uploading Plex (container): 2 file(s)"},
		{name: "per-file completion narrated", want: "Uploaded Plex, file=volume_0.tar"},
		{name: "per-file completion narrated (config)", want: "Uploaded Plex, file=config.json"},
		{name: "verify step narrated", want: "Verifying Plex (container): 2 file(s)"},
		{name: "verified step narrated", want: "Verified Plex (container): 2 file(s) checksum OK"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("run log missing %q; got:\n%s", tc.want, joined)
			}
		})
	}
}

// TestBackupItem_FolderDedupPhaseAndStatsLog: the dedup path narrates the
// folder walk phases (Task 2) AND logs per-item session stats, so a
// flash-drive backup on a dedup destination produces an ample run log
// (#328 QA).
func TestBackupItem_FolderDedupPhaseAndStatsLog(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	r.serverKey = testServerKey()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "vault.cfg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("hello"), 0o644); err != nil {
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

	jobID, err := d.CreateJob(db.Job{Name: "folder-dedup-phases"})
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

	// Each row is one assertion about the joined run-log output: a phase
	// narration must be present, a per-file -1 heartbeat must be absent.
	cases := []struct {
		name     string
		fragment string
		want     bool
	}{
		{name: "folder walk phase narrated", fragment: "Flash Drive: walking source tree", want: true},
		{name: "chunking phase narrated", fragment: "Flash Drive: chunking complete", want: true},
		{name: "manifest phase narrated", fragment: "Flash Drive: manifest written", want: true},
		{name: "session stats logged", fragment: "Dedup Flash Drive: chunks=", want: true},
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
