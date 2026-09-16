package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestCancelQueuedJob verifies that CancelJob removes a job that is queued
// behind the runner lock and its goroutine skips the run once it wakes up
// (issue #238: "Run Now" on a scheduled job while another run is active
// could not be cancelled).
func TestCancelQueuedJob(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cancel-queued-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	jobID, err := d.CreateJob(db.Job{
		Name: "cancel-queued", StorageDestID: destID, BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	// Hold the runner lock so the run queues instead of starting.
	r.mu.Lock()
	done := make(chan struct{})
	go func() {
		r.RunJob(jobID)
		close(done)
	}()

	// Wait for the goroutine to appear in the queue.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.queueMu.Lock()
		queued := len(r.queue)
		r.queueMu.Unlock()
		if queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			r.mu.Unlock()
			t.Fatal("job never appeared in the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.CancelJob(jobID); err != nil {
		r.mu.Unlock()
		t.Fatalf("CancelJob(queued) = %v, want nil", err)
	}
	r.queueMu.Lock()
	queuedAfter := len(r.queue)
	r.queueMu.Unlock()
	if queuedAfter != 0 {
		r.mu.Unlock()
		t.Fatalf("queue length after cancel = %d, want 0", queuedAfter)
	}

	// Release the lock; the goroutine must abort without running the job.
	r.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued goroutine did not exit")
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no run records for cancelled queued job, got %d (%+v)", len(runs), runs)
	}
}

// TestCancelTwoQueuedRunsOfSameJob verifies that cancelling two queued
// invocations of the same job aborts both goroutines (per-job cancellation
// count, not a collapsing set).
func TestCancelTwoQueuedRunsOfSameJob(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cancel-two-queued-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	jobID, err := d.CreateJob(db.Job{
		Name: "cancel-two-queued", StorageDestID: destID, BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.mu.Lock()
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			r.RunJob(jobID)
			done <- struct{}{}
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		r.queueMu.Lock()
		queued := len(r.queue)
		r.queueMu.Unlock()
		if queued == 2 {
			break
		}
		if time.Now().After(deadline) {
			r.mu.Unlock()
			t.Fatal("both runs never appeared in the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < 2; i++ {
		if err := r.CancelJob(jobID); err != nil {
			r.mu.Unlock()
			t.Fatalf("CancelJob #%d = %v, want nil", i+1, err)
		}
	}

	r.mu.Unlock()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("queued goroutine did not exit")
		}
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no run records after cancelling both queued runs, got %d", len(runs))
	}
}

// TestCancelJobNothingRunning preserves the existing error contract when
// neither an active nor a queued run matches.
func TestCancelJobNothingRunning(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.CancelJob(42); err == nil {
		t.Fatal("CancelJob with no active or queued run should error")
	}
}

// TestGuardPanicRecovers verifies the fire-and-forget goroutine guard
// swallows panics (issue #239 — background task panics crashed the daemon).
func TestGuardPanicRecovers(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer guardPanic("test worker")
		panic("boom")
	}()
	select {
	case <-done: // recovered — the goroutine exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}
}

// TestCancelQueueEntry_Backup verifies cancelling a queued backup by entry ID.
func TestCancelQueueEntry_Backup(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cancel-entry-backup-dest", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	jobID, err := d.CreateJob(db.Job{
		Name: "cancel-entry-backup", StorageDestID: destID, BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	r.mu.Lock()
	done := make(chan struct{})
	go func() {
		r.RunJob(jobID)
		close(done)
	}()

	var entryID string
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := r.Status()
		if len(s.Queue) == 1 {
			entryID = s.Queue[0].ID
			if s.Queue[0].Kind != QueueKindBackup || s.Queue[0].Status != QueueStatusQueued {
				r.mu.Unlock()
				t.Fatalf("unexpected queue entry metadata: %+v", s.Queue[0])
			}
			break
		}
		if time.Now().After(deadline) {
			r.mu.Unlock()
			t.Fatal("backup job never appeared in queue")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.CancelQueueEntry(entryID); err != nil {
		r.mu.Unlock()
		t.Fatalf("CancelQueueEntry(%s) = %v, want nil", entryID, err)
	}

	if qLen := len(r.Status().Queue); qLen != 0 {
		r.mu.Unlock()
		t.Fatalf("queue length after cancel = %d, want 0", qLen)
	}

	r.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued backup goroutine did not exit")
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs for cancelled queued job, got %d", len(runs))
	}
}

// TestCancelQueueEntry_Restore verifies that a queued restore can be cancelled by entry ID
// before it begins execution (issue #302).
func TestCancelQueueEntry_Restore(t *testing.T) {
	r, d := newTestRunner(t)
	jobID, err := d.CreateJob(db.Job{
		Name: "cancel-entry-restore", BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobID: jobID, JobRunID: runID, BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}
	rp, err := d.GetRestorePoint(rpID)
	if err != nil {
		t.Fatalf("get rp: %v", err)
	}

	r.mu.Lock()
	done := make(chan struct{})
	go func() {
		r.RunRestore(rp, []RestoreTarget{{Name: "test-target", Type: "folder"}}, t.TempDir(), "")
		close(done)
	}()

	var entryID string
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := r.Status()
		if len(s.Queue) == 1 {
			entryID = s.Queue[0].ID
			if s.Queue[0].Kind != QueueKindRestore || s.Queue[0].Status != QueueStatusQueued {
				r.mu.Unlock()
				t.Fatalf("unexpected queue entry metadata: %+v", s.Queue[0])
			}
			break
		}
		if time.Now().After(deadline) {
			r.mu.Unlock()
			t.Fatal("restore never appeared in queue")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.CancelQueueEntry(entryID); err != nil {
		r.mu.Unlock()
		t.Fatalf("CancelQueueEntry(%s) = %v, want nil", entryID, err)
	}

	if qLen := len(r.Status().Queue); qLen != 0 {
		r.mu.Unlock()
		t.Fatalf("queue length after cancel = %d, want 0", qLen)
	}

	r.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued restore goroutine did not exit")
	}

	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	for _, run := range runs {
		if run.RunType == "restore" {
			t.Fatalf("expected no restore runs, found: %+v", run)
		}
	}
}

// TestCancelQueueEntry_RefusalsAndNotFound verifies edge cases for CancelQueueEntry.
func TestCancelQueueEntry_RefusalsAndNotFound(t *testing.T) {
	r, _ := newTestRunner(t)

	// Not found
	if err := r.CancelQueueEntry("q-doesnotexist"); err == nil {
		t.Error("CancelQueueEntry(not-found) should error")
	}

	// Refusal for delete kind
	r.queueMu.Lock()
	r.queue = []QueueEntry{
		{ID: "q-delete-1", JobID: 10, JobName: "del", Kind: QueueKindDelete, Status: QueueStatusRunning},
		{ID: "q-running-1", JobID: 20, JobName: "run", Kind: QueueKindBackup, Status: QueueStatusRunning},
	}
	r.queueMu.Unlock()

	if err := r.CancelQueueEntry("q-delete-1"); err == nil {
		t.Error("CancelQueueEntry(delete) should error")
	}

	if err := r.CancelQueueEntry("q-running-1"); err == nil {
		t.Error("CancelQueueEntry(running) should error")
	}
}
