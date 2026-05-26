package scheduler

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestSchedulerSetters covers the four small hook setters in one place.
// They are simple field writes but they're each on the public surface
// the daemon wires into, so verify the field actually mutates.
func TestSchedulerSetters(t *testing.T) {
	d := testDB(t)
	s := New(d, func(int64) {})

	var (
		replCalled   atomic.Int32
		healthCalled atomic.Int32
		verifyCalled atomic.Int32
		retryCalled  atomic.Int32
	)

	s.SetReplicationRunner(func(int64) { replCalled.Add(1) })
	s.SetHealthChecker(func() { healthCalled.Add(1) })
	s.SetVerifyRunner(func(int64, string) { verifyCalled.Add(1) })
	s.SetRetryDispatcher(func(int64, int64, int) { retryCalled.Add(1) })

	// Invoke through the stored field references to confirm assignment.
	s.replicationRunner(1)
	s.healthChecker()
	s.verifyRunner(2, "deep")
	s.retryDispatcher(3, 4, 1)

	if replCalled.Load() != 1 || healthCalled.Load() != 1 || verifyCalled.Load() != 1 || retryCalled.Load() != 1 {
		t.Fatalf("setters did not wire callbacks: repl=%d health=%d verify=%d retry=%d",
			replCalled.Load(), healthCalled.Load(), verifyCalled.Load(), retryCalled.Load())
	}
}

// TestSchedulerVerifyJob exercises addVerifyJob through Reload with a
// job that has a VerifySchedule. Covers both the explicit-mode and the
// empty-mode default branch ("" -> "quick").
func TestSchedulerVerifyJob(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	// Explicit mode.
	_, err := d.CreateJob(db.Job{
		Name: "verify-deep", Enabled: true, Schedule: "0 2 * * *",
		StorageDestID: destID, VerifySchedule: "0 4 * * *", VerifyMode: "deep",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// Default ("" -> quick) mode.
	_, err = d.CreateJob(db.Job{
		Name: "verify-default", Enabled: true, Schedule: "0 3 * * *",
		StorageDestID: destID, VerifySchedule: "0 5 * * *",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	var verifyHits atomic.Int32
	s := New(d, func(int64) {})
	s.SetVerifyRunner(func(int64, string) { verifyHits.Add(1) })
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if len(s.verifyEntries) != 2 {
		t.Errorf("verifyEntries = %d, want 2", len(s.verifyEntries))
	}
}

// TestSchedulerVerifyJobBadCron exercises the error branch in addVerifyJob.
func TestSchedulerVerifyJobBadCron(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	_, err := d.CreateJob(db.Job{
		Name: "bad-verify", Enabled: true, Schedule: "0 2 * * *",
		StorageDestID: destID, VerifySchedule: "not-a-cron",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	s := New(d, func(int64) {})
	s.SetVerifyRunner(func(int64, string) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	// Bad cron -> no entry registered.
	if len(s.verifyEntries) != 0 {
		t.Errorf("verifyEntries on bad cron = %d, want 0", len(s.verifyEntries))
	}
}

// TestSchedulerReplicationSources covers addReplicationSource + the
// loadReplicationSources happy path, plus the bad-cron skip.
func TestSchedulerReplicationSources(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	// Good cron, enabled.
	_, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "src-good", Type: "remote_vault", URL: "http://r:24085",
		StorageDestID: destID, Schedule: "0 * * * *", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	// Disabled (skipped).
	_, err = d.CreateReplicationSource(db.ReplicationSource{
		Name: "src-disabled", Type: "remote_vault", URL: "http://r:24085",
		StorageDestID: destID, Schedule: "0 * * * *", Enabled: false,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	// Enabled but bad cron — addReplicationSource swallows the error.
	_, err = d.CreateReplicationSource(db.ReplicationSource{
		Name: "src-bad-cron", Type: "remote_vault", URL: "http://r:24085",
		StorageDestID: destID, Schedule: "totally invalid", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	s := New(d, func(int64) {})
	s.SetReplicationRunner(func(int64) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if len(s.replEntries) != 1 {
		t.Errorf("replEntries = %d, want 1 (only good source)", len(s.replEntries))
	}
}

// TestSchedulerLoadReplicationNilRunner covers the early-return when the
// daemon never wires SetReplicationRunner.
func TestSchedulerLoadReplicationNilRunner(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	_, _ = d.CreateReplicationSource(db.ReplicationSource{
		Name: "src", Type: "remote_vault", URL: "http://r:24085",
		StorageDestID: destID, Schedule: "0 * * * *", Enabled: true,
	})
	s := New(d, func(int64) {})
	// Deliberately no SetReplicationRunner — loadReplicationSources must no-op.
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	if len(s.replEntries) != 0 {
		t.Errorf("replEntries without runner = %d, want 0", len(s.replEntries))
	}
}

// TestSchedulerReloadWithReplication ensures Reload tears down and
// re-installs replication entries.
func TestSchedulerReloadWithReplication(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})

	s := New(d, func(int64) {})
	s.SetReplicationRunner(func(int64) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	if len(s.replEntries) != 0 {
		t.Fatalf("replEntries on empty start = %d", len(s.replEntries))
	}

	_, _ = d.CreateReplicationSource(db.ReplicationSource{
		Name: "src", Type: "remote_vault", URL: "http://r:24085",
		StorageDestID: destID, Schedule: "0 * * * *", Enabled: true,
	})
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(s.replEntries) != 1 {
		t.Errorf("after reload replEntries = %d, want 1", len(s.replEntries))
	}
}

// TestSchedulerAddJobBadCron exercises the cron.AddFunc error branch
// in addJob (the L-prefix branch is already covered separately).
func TestSchedulerAddJobBadCron(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	_, err := d.CreateJob(db.Job{
		Name: "bad", Enabled: true, Schedule: "not a cron expr", StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	s := New(d, func(int64) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	if len(s.entries) != 0 {
		t.Errorf("entries on bad cron = %d, want 0", len(s.entries))
	}
}

// TestSchedulerStartWithHooks ensures the daily health check and retry
// watcher branches in Start() get exercised (otherwise they're skipped).
func TestSchedulerStartWithHooks(t *testing.T) {
	d := testDB(t)
	s := New(d, func(int64) {})
	s.SetHealthChecker(func() {})
	s.SetRetryDispatcher(func(int64, int64, int) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	// The scheduler does not track these entries by ID; just confirm the
	// cron has at least the two extra entries beyond user jobs.
	entries := s.cron.Entries()
	if len(entries) < 2 {
		t.Errorf("expected >=2 cron entries (health + retry), got %d", len(entries))
	}
}

// TestFireDueRetriesEmpty covers fireDueRetries when the DB has no due
// retries (the common no-op branch).
func TestFireDueRetriesEmpty(t *testing.T) {
	d := testDB(t)
	s := New(d, func(int64) {})
	var called atomic.Int32
	s.SetRetryDispatcher(func(int64, int64, int) { called.Add(1) })
	// fireDueRetries should be a no-op with an empty DB.
	s.fireDueRetries()
	if called.Load() != 0 {
		t.Errorf("dispatcher called %d times on empty DB, want 0", called.Load())
	}
}

// TestFireDueRetriesWithDueRow inserts a failed run with a past
// retry_next_at and verifies the dispatcher is invoked.
func TestFireDueRetriesWithDueRow(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{Name: "j", StorageDestID: destID})

	// Insert a failed run row whose retry_next_at is in the past. Use
	// SQLite's datetime() so the comparison against CURRENT_TIMESTAMP
	// uses the same text format the schema expects (see
	// db/retry_claim_test.go for the canonical pattern).
	res, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, retry_next_at, retry_attempt)
		 VALUES (?, 'failed', 'full', datetime('now', '-1 hour'), 0)`,
		jobID,
	)
	if err != nil {
		t.Fatalf("insert failed run: %v", err)
	}
	runID, _ := res.LastInsertId()

	var dispatched atomic.Int32
	var gotJobID, gotOriginalRun atomic.Int64
	var gotAttempt atomic.Int32
	done := make(chan struct{}, 1)
	s := New(d, func(int64) {})
	s.SetRetryDispatcher(func(jID, origID int64, attempt int) {
		gotJobID.Store(jID)
		gotOriginalRun.Store(origID)
		gotAttempt.Store(int32(attempt))
		dispatched.Add(1)
		select {
		case done <- struct{}{}:
		default:
		}
	})
	s.fireDueRetries()

	// Dispatcher is called in a goroutine — wait briefly.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("retry dispatcher was not invoked within timeout")
	}
	if gotJobID.Load() != jobID || gotOriginalRun.Load() != runID || gotAttempt.Load() != 1 {
		t.Errorf("dispatcher got (jobID=%d originalRun=%d attempt=%d) want (%d %d 1)",
			gotJobID.Load(), gotOriginalRun.Load(), gotAttempt.Load(), jobID, runID)
	}
	if dispatched.Load() != 1 {
		t.Errorf("dispatcher called %d times, want 1", dispatched.Load())
	}

	// The retry_next_at must now be NULL (claimed exactly once).
	var nextNull bool
	row := d.QueryRow(`SELECT retry_next_at IS NULL FROM job_runs WHERE id = ?`, runID)
	if err := row.Scan(&nextNull); err != nil {
		t.Fatalf("re-read row: %v", err)
	}
	if !nextNull {
		t.Error("retry_next_at was not cleared after claim")
	}
}

// TestNextRun covers the three branches in NextRun: hit in standard
// entries, hit in last-day entries, and miss.
func TestNextRun(t *testing.T) {
	d := testDB(t)
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{
		Name: "n", Enabled: true, Schedule: "0 2 * * *", StorageDestID: destID,
	})
	lastDayID, _ := d.CreateJob(db.Job{
		Name: "n-last", Enabled: true, Schedule: "0 2 L * *", StorageDestID: destID,
	})

	s := New(d, func(int64) {})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if got, ok := s.NextRun(jobID); !ok || got == "" {
		t.Errorf("NextRun(%d) standard = (%q, %v), want non-empty true", jobID, got, ok)
	}
	if got, ok := s.NextRun(lastDayID); !ok || got == "" {
		t.Errorf("NextRun(%d) last-day = (%q, %v), want non-empty true", lastDayID, got, ok)
	}
	if _, ok := s.NextRun(999_999); ok {
		t.Error("NextRun for missing job id should return ok=false")
	}
}
