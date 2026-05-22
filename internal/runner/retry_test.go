package runner

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

func TestResolveRetryPolicyGlobal(t *testing.T) {
	job := db.Job{}
	p := resolveRetryPolicy(job, 2, []int{900, 3600, 14400})
	if p.Max != 2 {
		t.Errorf("Max = %d, want 2", p.Max)
	}
	if len(p.Delays) != 3 || p.Delays[0] != 900 {
		t.Errorf("Delays = %v, want [900 3600 14400]", p.Delays)
	}
}

func TestResolveRetryPolicyOverride(t *testing.T) {
	job := db.Job{
		RetryMaxOverride:    sql.NullInt64{Valid: true, Int64: 1},
		RetryDelaysOverride: sql.NullString{Valid: true, String: "[60,120]"},
	}
	p := resolveRetryPolicy(job, 2, []int{900, 3600, 14400})
	if p.Max != 1 {
		t.Errorf("Max = %d, want 1 (override)", p.Max)
	}
	if len(p.Delays) != 2 || p.Delays[0] != 60 {
		t.Errorf("Delays = %v, want [60 120]", p.Delays)
	}
}

func TestResolveRetryPolicyInvalidJSONFallsBack(t *testing.T) {
	job := db.Job{
		RetryDelaysOverride: sql.NullString{Valid: true, String: "not json"},
	}
	p := resolveRetryPolicy(job, 2, []int{900})
	if len(p.Delays) != 1 || p.Delays[0] != 900 {
		t.Errorf("invalid JSON should fall back to global, got %v", p.Delays)
	}
}

func TestRetryNextDelay(t *testing.T) {
	p := RetryPolicy{Max: 3, Delays: []int{60, 300, 900}}
	if d := p.NextDelay(0); d != 60 {
		t.Errorf("attempt 0 delay = %d, want 60", d)
	}
	if d := p.NextDelay(2); d != 900 {
		t.Errorf("attempt 2 delay = %d, want 900", d)
	}
	if d := p.NextDelay(5); d != 900 {
		t.Errorf("attempt 5 delay = %d, want 900 (clamp)", d)
	}
	if d := p.NextDelay(-1); d != 60 {
		t.Errorf("attempt -1 delay = %d, want 60 (clamp)", d)
	}
	empty := RetryPolicy{Max: 3, Delays: nil}
	if d := empty.NextDelay(0); d != 0 {
		t.Errorf("empty delays should return 0, got %d", d)
	}
}

func TestParseGlobalDelays(t *testing.T) {
	if d := parseGlobalDelays("[1,2,3]"); len(d) != 3 || d[1] != 2 {
		t.Errorf("parseGlobalDelays([1,2,3]) = %v", d)
	}
	if d := parseGlobalDelays(""); d != nil {
		t.Errorf("empty string should return nil, got %v", d)
	}
	if d := parseGlobalDelays("garbage"); d != nil {
		t.Errorf("garbage should return nil, got %v", d)
	}
}

// newTestRunner spins up a fresh Runner backed by an on-disk SQLite DB.
// Caller is responsible for stopping the ws hub goroutine by relying on
// t.TempDir cleanup; the hub's Run loop has no shutdown contract.
func newTestRunner(t *testing.T) (*Runner, *db.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	hub := ws.NewHub()
	go hub.Run()
	return New(database, hub, nil), database
}

func TestScheduleRetryIfDueSetsRetryNextAt(t *testing.T) {
	r, _ := newTestRunner(t)
	job := db.Job{ID: 1}
	dest := db.StorageDestination{ID: 1, BreakerState: "closed"}
	run := db.JobRun{RetryAttempt: 0}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if !run.RetryNextAt.Valid {
		t.Fatalf("RetryNextAt was not set when retries are due")
	}
	if !run.RetryNextAt.Time.After(time.Now()) {
		t.Errorf("RetryNextAt should be in the future, got %v", run.RetryNextAt.Time)
	}
}

func TestScheduleRetryIfDueManualSuppresses(t *testing.T) {
	r, _ := newTestRunner(t)
	job := db.Job{ID: 1}
	dest := db.StorageDestination{ID: 1, BreakerState: "closed"}
	run := db.JobRun{RetryAttempt: 0}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{manual: true})
	if run.RetryNextAt.Valid {
		t.Errorf("manual run should not schedule retry, got %v", run.RetryNextAt.Time)
	}
}

func TestScheduleRetryIfDueBreakerOpenSuppresses(t *testing.T) {
	r, _ := newTestRunner(t)
	job := db.Job{ID: 1}
	dest := db.StorageDestination{ID: 1, BreakerState: "open"}
	run := db.JobRun{RetryAttempt: 0}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if run.RetryNextAt.Valid {
		t.Errorf("breaker-open run should not schedule retry, got %v", run.RetryNextAt.Time)
	}
}

func TestScheduleRetryIfDueExhaustsAtMax(t *testing.T) {
	r, database := newTestRunner(t)
	// Pin the global policy to Max=2, Delays=[10].
	if err := database.SetSetting("retry_max_default", "2"); err != nil {
		t.Fatalf("set retry_max_default: %v", err)
	}
	if err := database.SetSetting("retry_delays_default", "[10]"); err != nil {
		t.Fatalf("set retry_delays_default: %v", err)
	}
	job := db.Job{ID: 1}
	dest := db.StorageDestination{ID: 1, BreakerState: "closed"}

	// attempt=0 → eligible
	run := db.JobRun{RetryAttempt: 0}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if !run.RetryNextAt.Valid {
		t.Fatalf("attempt=0 should schedule retry")
	}
	// attempt=1 → eligible (< Max=2)
	run = db.JobRun{RetryAttempt: 1}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if !run.RetryNextAt.Valid {
		t.Fatalf("attempt=1 should schedule retry")
	}
	// attempt=2 → exhausted (>= Max=2)
	run = db.JobRun{RetryAttempt: 2}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if run.RetryNextAt.Valid {
		t.Errorf("attempt=2 should NOT schedule retry (max reached)")
	}
}

func TestScheduleRetryIfDuePersistsViaUpdateJobRun(t *testing.T) {
	r, database := newTestRunner(t)
	if err := database.SetSetting("retry_max_default", "2"); err != nil {
		t.Fatalf("set retry_max_default: %v", err)
	}
	if err := database.SetSetting("retry_delays_default", "[5]"); err != nil {
		t.Fatalf("set retry_delays_default: %v", err)
	}
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "retry-dest", Type: "local", Config: "{}",
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{
		Name:            "retry-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "running",
		BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create job run: %v", err)
	}

	job, _ := database.GetJob(jobID)
	dest, _ := database.GetStorageDestination(destID)

	run := db.JobRun{ID: runID, JobID: jobID, Status: "failed", RetryAttempt: 0}
	r.scheduleRetryIfDue(&run, job, dest, runOptions{})
	if err := database.UpdateJobRun(run); err != nil {
		t.Fatalf("update job run: %v", err)
	}

	runs, err := database.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if !runs[0].RetryNextAt.Valid {
		t.Fatalf("retry_next_at was not persisted")
	}
	if !runs[0].RetryNextAt.Time.After(time.Now()) {
		t.Errorf("retry_next_at should be in the future, got %v", runs[0].RetryNextAt.Time)
	}
}

func TestCreateJobRunPersistsRetryFields(t *testing.T) {
	_, database := newTestRunner(t)
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "retry-dest", Type: "local", Config: "{}",
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := database.CreateJob(db.Job{
		Name:            "retry-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	parentRunID, err := database.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "failed",
		BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create parent run: %v", err)
	}
	retryRunID, err := database.CreateJobRun(db.JobRun{
		JobID:        jobID,
		Status:       "running",
		BackupType:   "full",
		RetryOfRunID: sql.NullInt64{Valid: true, Int64: parentRunID},
		RetryAttempt: 1,
	})
	if err != nil {
		t.Fatalf("create retry run: %v", err)
	}

	runs, err := database.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get job runs: %v", err)
	}
	var retry db.JobRun
	for _, rn := range runs {
		if rn.ID == retryRunID {
			retry = rn
			break
		}
	}
	if retry.ID == 0 {
		t.Fatalf("retry run not found in listing")
	}
	if !retry.RetryOfRunID.Valid || retry.RetryOfRunID.Int64 != parentRunID {
		t.Errorf("RetryOfRunID = %+v, want valid=%d", retry.RetryOfRunID, parentRunID)
	}
	if retry.RetryAttempt != 1 {
		t.Errorf("RetryAttempt = %d, want 1", retry.RetryAttempt)
	}
}
