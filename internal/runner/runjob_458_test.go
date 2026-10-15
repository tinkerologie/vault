package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

// d458Job describes a folder-backed job for the runJobInternal path tests.
// Every job points at a local destination under t.TempDir so no network or
// real workload is touched.
type d458Job struct {
	job   db.Job
	items []db.JobItem
}

// d458Seed creates the destination, job and items, returning the job ID.
// When destPath is empty a fresh temp directory is used.
func d458Seed(t *testing.T, d *db.DB, destPath string, spec d458Job) int64 {
	t.Helper()
	if destPath == "" {
		destPath = t.TempDir()
	}
	cfg, _ := json.Marshal(map[string]string{"path": destPath})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "d458-dest-" + nextUniqueRunner(t), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	job := spec.job
	if job.Name == "" {
		job.Name = "d458-job-" + nextUniqueRunner(t)
	}
	if job.BackupTypeChain == "" {
		job.BackupTypeChain = "full"
	}
	job.StorageDestID = destID
	job.Enabled = true
	jobID, err := d.CreateJob(job)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for _, it := range spec.items {
		it.JobID = jobID
		if it.ItemType == "" {
			it.ItemType = "folder"
		}
		if _, err := d.AddJobItem(it); err != nil {
			t.Fatalf("add item: %v", err)
		}
	}
	return jobID
}

// d458FolderItem returns a folder item backed by a real directory holding
// one file.
func d458FolderItem(t *testing.T, name string) db.JobItem {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte("payload-"+name), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(map[string]any{"path": src})
	return db.JobItem{ItemType: "folder", ItemName: name, Settings: string(s)}
}

// d458BrokenItem returns a folder item whose empty path makes the engine fail.
func d458BrokenItem(name string) db.JobItem {
	s, _ := json.Marshal(map[string]any{"path": ""})
	return db.JobItem{ItemType: "folder", ItemName: name, Settings: string(s)}
}

func d458LatestRun(t *testing.T, d *db.DB, jobID int64) db.JobRun {
	t.Helper()
	runs, err := d.GetJobRuns(jobID, 50)
	if err != nil || len(runs) == 0 {
		t.Fatalf("GetJobRuns(%d): %v (n=%d)", jobID, err, len(runs))
	}
	// started_at has one-second resolution, so pick the newest by ID.
	latest := runs[0]
	for _, run := range runs[1:] {
		if run.ID > latest.ID {
			latest = run
		}
	}
	return latest
}

func d458RunLogText(t *testing.T, d *db.DB, runID int64) string {
	t.Helper()
	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 500)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func d458ActivityText(t *testing.T, d *db.DB) string {
	t.Helper()
	logs, err := d.ListActivityLogs(500, "", 0)
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l.Message)
		b.WriteString(" | ")
		b.WriteString(l.Details)
		b.WriteString("\n")
	}
	return b.String()
}

// TestRunJob458PreflightFailureRecordsSkippedRun covers the pre-flight branch:
// an unreachable local destination produces a "skipped" run, a scheduled
// retry and a breaker failure, without ever creating a running run.
func TestRunJob458PreflightFailureRecordsSkippedRun(t *testing.T) {
	r, d := newTestRunner(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	jobID := d458Seed(t, d, missing, d458Job{
		job:   db.Job{RetryMaxOverride: new(int64(3))},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", run.Status)
	}
	if !strings.Contains(run.Log, "Pre-flight check failed") {
		t.Errorf("log = %q, want pre-flight failure", run.Log)
	}
	if run.RetryNextAt == nil {
		t.Error("preflight failure should schedule a retry")
	}
	job, _ := d.GetJob(jobID)
	dest, _ := d.GetStorageDestination(job.StorageDestID)
	if dest.ConsecutiveFailures < 1 {
		t.Errorf("breaker failures = %d, want >= 1", dest.ConsecutiveFailures)
	}
}

// TestRunJob458EncryptionWithoutPassphraseFails covers the age-encryption
// guard: no passphrase configured fails the run before any item is touched.
func TestRunJob458EncryptionWithoutPassphraseFails(t *testing.T) {
	r, d := newTestRunner(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{Encryption: "age", Schedule: "0 3 * * *"},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "failed" {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	if !strings.Contains(run.Log, "no passphrase configured") {
		t.Errorf("log = %q", run.Log)
	}
	if run.ItemsDone != 0 {
		t.Errorf("items done = %d, want 0", run.ItemsDone)
	}
}

// TestRunJob458PreScriptFailureFailsRun covers the pre-script error branch.
func TestRunJob458PreScriptFailureFailsRun(t *testing.T) {
	r, d := newTestRunner(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{PreScript: filepath.Join(t.TempDir(), "missing-pre.sh")},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "failed" || !strings.Contains(run.Log, "Pre-script failed") {
		t.Fatalf("run = %q / %q, want failed pre-script", run.Status, run.Log)
	}
	if !strings.Contains(d458ActivityText(t, d), "Pre-script failed") {
		t.Error("expected a Pre-script failed activity entry")
	}
}

// TestRunJob458ScriptsRunAroundBackup covers a successful pre-script with
// output and a post-script that runs and reports output.
func TestRunJob458ScriptsRunAroundBackup(t *testing.T) {
	r, d := newTestRunner(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "post-ran")
	pre := filepath.Join(dir, "pre.sh")
	post := filepath.Join(dir, "post.sh")
	if err := writeExecutableScript(pre, "#!/bin/sh\necho pre-output\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableScript(post, "#!/bin/sh\necho \"$VAULT_STATUS\" > '"+marker+"'\necho post-output\n"); err != nil {
		t.Fatal(err)
	}
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{PreScript: pre, PostScript: post},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "completed" {
		t.Fatalf("status = %q, want completed (log %q)", run.Status, run.Log)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("post-script did not run: %v", err)
	}
	if strings.TrimSpace(string(got)) != "completed" {
		t.Errorf("post-script saw VAULT_STATUS=%q, want completed", got)
	}
}

// TestRunJob458PostScriptFailureIsLogged covers the post-script error branch,
// which must not change the run status but must leave an activity entry.
func TestRunJob458PostScriptFailureIsLogged(t *testing.T) {
	r, d := newTestRunner(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{PostScript: filepath.Join(t.TempDir(), "missing-post.sh")},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "completed" {
		t.Fatalf("status = %q, want completed", run.Status)
	}
	if !strings.Contains(d458ActivityText(t, d), "Post-script failed") {
		t.Error("expected a Post-script failed activity entry")
	}
}

// TestRunJob458PartialRunWithVerifyAndRetention covers a two-item run where
// one item fails: status partial, per-item checksums recorded for the
// verified item, the per-item narration line, and the legacy count retention
// finalization step.
func TestRunJob458PartialRunWithVerifyAndRetention(t *testing.T) {
	r, d := newTestRunner(t)
	enq := &fakeEnqueuer{}
	r.SetEvaluator(enq)
	jobID := d458Seed(t, d, "", d458Job{
		job: db.Job{VerifyBackup: true, RetentionCount: 1},
		items: []db.JobItem{
			d458FolderItem(t, "good"),
			d458BrokenItem("bad"),
		},
	})

	r.RunJob(jobID)
	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial (log %q)", run.Status, run.Log)
	}
	if run.ItemsDone != 1 || run.ItemsFailed != 1 {
		t.Errorf("done/failed = %d/%d, want 1/1", run.ItemsDone, run.ItemsFailed)
	}
	if run.RetryNextAt != nil {
		t.Error("a partial run must not schedule a retry")
	}
	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rps) != 1 {
		t.Fatalf("restore points = %d, want 1 after count retention", len(rps))
	}
	if !strings.Contains(rps[0].Metadata, `"checksums"`) {
		t.Errorf("restore point metadata missing checksums: %s", rps[0].Metadata)
	}
	if !strings.Contains(d458RunLogText(t, d, run.ID), "Backed up good") {
		t.Error("multi-item run should narrate per-item completion")
	}
	if len(enq.enqueuedIDs()) != 2 {
		t.Errorf("evaluator enqueued %d runs, want 2", len(enq.enqueuedIDs()))
	}
}

// TestRunJob458LTRRetentionFinalization covers the LTR finalization branch.
func TestRunJob458LTRRetentionFinalization(t *testing.T) {
	r, d := newTestRunner(t)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{KeepLatest: 1},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)
	r.RunJobFull(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rps) != 1 {
		t.Fatalf("restore points = %d, want 1 with keep_latest=1", len(rps))
	}
}

// TestRunJob458SnapshotManagerFinalization covers the snapshot/USB
// finalization step that runs after a successful backup.
func TestRunJob458SnapshotManagerFinalization(t *testing.T) {
	r, d := newTestRunner(t)
	snapDir := t.TempDir()
	r.SetSnapshotManager(db.NewSnapshotManager(d, filepath.Join(snapDir, "vault.db"), ""))
	jobID := d458Seed(t, d, "", d458Job{items: []db.JobItem{d458FolderItem(t, "a")}})

	r.RunJob(jobID)

	if run := d458LatestRun(t, d, jobID); run.Status != "completed" {
		t.Fatalf("status = %q, want completed", run.Status)
	}
}

// TestRunJob458MalformedSettingsAndReappearedItem covers the malformed
// settings JSON branches (stale detection log and per-item fallback) and the
// clearing of a stale flag on an item whose folder exists again.
func TestRunJob458MalformedSettingsAndReappearedItem(t *testing.T) {
	r, d := newTestRunner(t)
	good := d458FolderItem(t, "back-again")
	jobID := d458Seed(t, d, "", d458Job{
		items: []db.JobItem{
			good,
			{ItemType: "folder", ItemName: "garbled", Settings: "{not json"},
		},
	})
	items, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatal(err)
	}
	var goodID int64
	for _, it := range items {
		if it.ItemName == "back-again" {
			goodID = it.ID
		}
	}
	if err := d.MarkJobItemsMissing([]int64{goodID}, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial (log %q)", run.Status, run.Log)
	}
	items, _ = d.GetJobItems(jobID)
	for _, it := range items {
		if it.ID == goodID && it.MissingSince != nil {
			t.Error("reappeared item should have its missing flag cleared")
		}
	}
}

// TestRunJob458IncrementalVMAndZFSSettingsFromParent covers the per-type
// settings assembly for VM, ZFS and plugin items on an incremental run: the
// parent restore point's recorded checkpoint/snapshot is handed to the
// engine and plugin exclusions are merged. Inventory is pinned through
// containerLister (failing) so VM/ZFS presence is "unknown" and the items
// reach their handlers, which fail without libvirt/zfs — no workload is
// touched.
func TestRunJob458IncrementalVMAndZFSSettingsFromParent(t *testing.T) {
	r, d := newTestRunner(t)
	r.containerLister = func() ([]engine.BackupItem, error) { return nil, errors.New("inventory pinned") }
	if err := d.SetSetting("global_exclude_paths", `["*.tmp"]`); err != nil {
		t.Fatal(err)
	}

	plgDir := t.TempDir()
	plg := filepath.Join(plgDir, "vault458.plg")
	if err := os.WriteFile(plg, []byte("<PLUGIN/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.cfg"), []byte("a=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	plgSettings, _ := json.Marshal(map[string]any{"plg_path": plg, "path": cfgDir, "exclude_paths": []string{"cache"}})
	vmSettings, _ := json.Marshal(map[string]any{"disks": []string{}})
	zfsSettings, _ := json.Marshal(map[string]any{"dataset": "vault458pool/ds"})

	// A folder the parent never captured, with its own exclusions.
	newFolder := d458FolderItem(t, "vault458-new-folder")
	newFolder.Settings = strings.TrimSuffix(newFolder.Settings, "}") + `,"exclude_paths":["skip"]}`

	jobID := d458Seed(t, d, "", d458Job{
		job: db.Job{BackupTypeChain: "incremental", VMMode: "snapshot"},
		items: []db.JobItem{
			{ItemType: "vm", ItemName: "vault458-vm", ItemID: "vault458-vm-uuid", Settings: string(vmSettings)},
			{ItemType: "zfs", ItemName: "vault458-ds", Settings: string(zfsSettings)},
			{ItemType: "plugin", ItemName: "vault458", Settings: string(plgSettings)},
			newFolder,
		},
	})
	// The parent records the VM, ZFS and plugin items (not the new folder)
	// with a checkpoint and a snapshot.
	parentRun, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: parentRun, JobID: jobID, BackupType: "full", StoragePath: "parent/run",
		Metadata: `{"item_sizes":{"vault458-vm":1,"vault458-ds":1,"vault458":1},"vm_checkpoints":{"vault458-vm":"cp-1"},"zfs_snapshots":{"vault458-ds":"vault458pool/ds@s1"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.BackupType != "incremental" {
		t.Errorf("backup type = %q, want incremental", run.BackupType)
	}
	if run.ItemsFailed < 2 {
		t.Fatalf("items failed = %d, want the VM and ZFS items to fail (log %q)", run.ItemsFailed, run.Log)
	}
	if run.Status != "partial" {
		t.Fatalf("status = %q, want partial (the new folder succeeds)", run.Status)
	}
	// The plugin handler is Linux-only, so only the folder is guaranteed.
	if run.ItemsDone < 1 {
		t.Errorf("items done = %d, want >= 1 (log %q)", run.ItemsDone, run.Log)
	}
}

// d458PanicOnceEnqueuer panics on its first EnqueueRun call and records the
// later ones, standing in for a defect anywhere on the run path.
type d458PanicOnceEnqueuer struct {
	calls int
	ids   []int64
}

func (e *d458PanicOnceEnqueuer) EnqueueRun(runID int64) {
	e.calls++
	if e.calls == 1 {
		panic("d458 evaluator exploded")
	}
	e.ids = append(e.ids, runID)
}

// TestRunJob458PanicIsRecoveredAndRunMarkedFailed covers the panic recovery
// defer: the run is marked failed with the panic text, a retry is
// scheduled, the breaker records a failure and the evaluator still hears
// about the run.
func TestRunJob458PanicIsRecoveredAndRunMarkedFailed(t *testing.T) {
	r, d := newTestRunner(t)
	enq := &d458PanicOnceEnqueuer{}
	r.SetEvaluator(enq)
	jobID := d458Seed(t, d, "", d458Job{
		job:   db.Job{RetryMaxOverride: new(int64(2))},
		items: []db.JobItem{d458FolderItem(t, "a")},
	})

	r.RunJob(jobID)

	run := d458LatestRun(t, d, jobID)
	if run.Status != "failed" || !strings.Contains(run.Log, "Internal error (panic)") {
		t.Fatalf("run = %q / %q, want failed with panic text", run.Status, run.Log)
	}
	if run.RetryNextAt == nil {
		t.Error("a recovered panic should schedule a retry")
	}
	if len(enq.ids) != 1 || enq.ids[0] != run.ID {
		t.Errorf("evaluator ids after recovery = %v, want [%d]", enq.ids, run.ID)
	}
	if st := r.Status(); st.Active {
		t.Error("live status should be cleared after a recovered panic")
	}
}
