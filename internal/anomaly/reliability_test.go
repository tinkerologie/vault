package anomaly

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// openReliabilityDB opens a fresh SQLite database for reliability tests.
func openReliabilityDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "rel_test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedReliabilityJob creates the prerequisite storage destination + job,
// returning the jobID. Used by tests that exercise the verify-regression
// signal.
func seedReliabilityJob(t *testing.T, d *db.DB) int64 {
	t.Helper()
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "rel-dest",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:          "rel-job",
		Enabled:       true,
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return jobID
}

// seedJobRunAndRestorePoint creates a job run + restore point and returns
// the restore point ID. Used to link verify runs back to the job.
func seedJobRunAndRestorePoint(t *testing.T, d *db.DB, jobID int64) int64 {
	t.Helper()
	runID, err := d.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "success",
		BackupType: "full",
		RunType:    "backup",
	})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "/tmp/test",
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	return rpID
}

// seedVerifyRun inserts a completed verify run with the given status and
// returns its ID. The started_at is offset by the provided duration so
// callers can control ordering.
func seedVerifyRun(t *testing.T, d *db.DB, rpID int64, status string, offsetFromNow time.Duration) int64 {
	t.Helper()
	vrID, err := d.CreateVerifyRun(rpID, "quick")
	if err != nil {
		t.Fatalf("CreateVerifyRun: %v", err)
	}
	// Override the started_at so we can control ordering when seeding
	// multiple runs. SQLite DEFAULT is CURRENT_TIMESTAMP; we update it
	// explicitly after insert.
	_, execErr := d.Exec(
		`UPDATE verify_runs SET started_at = ?, status = ?, completed_at = ?
		 WHERE id = ?`,
		time.Now().Add(offsetFromNow),
		status,
		time.Now().Add(offsetFromNow+time.Second),
		vrID,
	)
	if execErr != nil {
		t.Fatalf("updating verify_run started_at/status: %v", execErr)
	}
	return vrID
}

// buildReliabilityEC constructs an EvalContext with the given RecentRuns
// slice and sensitivity. The runs slice is expected to be newest-first
// (mirroring GetJobRuns order).
func buildReliabilityEC(jobID int64, runs []db.JobRun, sensitivity string) EvalContext {
	return EvalContext{
		Job:               &db.Job{ID: jobID},
		RecentRuns:        runs,
		GlobalSensitivity: sensitivity,
		Clock:             NewFakeClock(time.Now()),
	}
}

// makeRun is a test helper that constructs a db.JobRun with the given status
// and ItemsFailed. ID is auto-assigned sequentially from 100.
func makeRun(id int64, status string, itemsFailed int) db.JobRun {
	return db.JobRun{
		ID:          id,
		JobID:       1,
		Status:      status,
		ItemsFailed: itemsFailed,
	}
}

// --- TestReliability suite ---

// TestReliability_StreakBelowThreshold verifies that a streak below the
// sensitivity threshold produces no anomaly.
// Balanced sensitivity → Streak() == 2. With only 1 trailing failure, no signal.
func TestReliability_StreakBelowThreshold(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	runs := []db.JobRun{
		makeRun(101, "failed", 0), // newest — 1 failure
		makeRun(100, "success", 0),
	}
	ec := buildReliabilityEC(1, runs, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Streak == 1 < threshold 2 → no failure_streak anomaly.
	for _, a := range got {
		if a.Metric == "failure_streak" {
			t.Errorf("expected no failure_streak anomaly below threshold, got %+v", a)
		}
	}
}

// TestReliability_StreakAtThreshold verifies that a streak exactly at the
// sensitivity threshold fires a CRITICAL "failure_streak" anomaly.
// Balanced sensitivity → threshold == 2.
func TestReliability_StreakAtThreshold(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	threshold := SensBalanced.Streak() // 2
	runs := make([]db.JobRun, threshold+1)
	for i := 0; i < threshold; i++ {
		runs[i] = makeRun(int64(200+i), "failed", 0)
	}
	runs[threshold] = makeRun(int64(200+threshold), "success", 0) // break streak

	ec := buildReliabilityEC(1, runs, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var streakAnomaly *Anomaly
	for i := range got {
		if got[i].Metric == "failure_streak" {
			streakAnomaly = &got[i]
			break
		}
	}
	if streakAnomaly == nil {
		t.Fatalf("expected failure_streak anomaly at threshold %d, got %v", threshold, got)
	}
	if streakAnomaly.Severity != SeverityCritical {
		t.Errorf("Severity: want %q, got %q", SeverityCritical, streakAnomaly.Severity)
	}
	if streakAnomaly.Observed != float64(threshold) {
		t.Errorf("Observed: want %d, got %v", threshold, streakAnomaly.Observed)
	}

	// Details JSON must contain {"streak": <threshold>}.
	var d2 map[string]interface{}
	if err := json.Unmarshal([]byte(streakAnomaly.Details), &d2); err != nil {
		t.Fatalf("Details not valid JSON: %v (got %q)", err, streakAnomaly.Details)
	}
	if v, ok := d2["streak"].(float64); !ok || int(v) != threshold {
		t.Errorf("Details.streak: want %d, got %v", threshold, d2["streak"])
	}
}

// TestReliability_StreakItemsFailed verifies that ItemsFailed>0 counts as a
// failure even when Status is "success" (partial-failure run).
func TestReliability_StreakItemsFailed(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	// Balanced threshold = 2; seed 2 "success" runs with ItemsFailed>0.
	runs := []db.JobRun{
		makeRun(301, "success", 1), // newest — partial failure
		makeRun(300, "success", 2), // partial failure
		makeRun(299, "success", 0), // clean, breaks streak
	}
	ec := buildReliabilityEC(1, runs, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var streakAnomaly *Anomaly
	for i := range got {
		if got[i].Metric == "failure_streak" {
			streakAnomaly = &got[i]
			break
		}
	}
	if streakAnomaly == nil {
		t.Fatalf("expected failure_streak anomaly for ItemsFailed>0, got %v", got)
	}
	if streakAnomaly.Severity != SeverityCritical {
		t.Errorf("Severity: want critical, got %q", streakAnomaly.Severity)
	}
}

// TestReliability_StreakReset verifies that when the newest run is clean
// (Status=="success" and ItemsFailed==0), the streak is 0 and no
// failure_streak anomaly is emitted — even if older runs were failures.
func TestReliability_StreakReset(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	runs := []db.JobRun{
		makeRun(401, "success", 0), // newest — clean recovery
		makeRun(400, "failed", 0),
		makeRun(399, "failed", 0),
		makeRun(398, "failed", 0),
	}
	ec := buildReliabilityEC(1, runs, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, a := range got {
		if a.Metric == "failure_streak" {
			t.Errorf("expected no failure_streak after successful recovery, got %+v", a)
		}
	}
}

// TestReliability_StreakNilJob verifies that a nil Job guard returns nil
// without panicking.
func TestReliability_StreakNilJob(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	ec := EvalContext{
		Job:               nil,
		RecentRuns:        []db.JobRun{makeRun(1, "failed", 0)},
		GlobalSensitivity: "balanced",
		Clock:             NewFakeClock(time.Now()),
	}
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected nil result for nil Job, got %+v", got)
	}
}

// TestReliability_VerifyRegression verifies that when the newest verify run
// is "failed" and the previous was "passed", a CRITICAL "verify_outcome"
// anomaly is emitted.
func TestReliability_VerifyRegression(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	jobID := seedReliabilityJob(t, d)
	rpID := seedJobRunAndRestorePoint(t, d, jobID)

	// Seed two verify runs: older one passed, newer one failed.
	// Use negative offsets so both are in the past, with the "failed" run
	// being more recent.
	seedVerifyRun(t, d, rpID, "passed", -2*time.Hour) // older
	seedVerifyRun(t, d, rpID, "failed", -1*time.Hour) // newest → regression

	ec := buildReliabilityEC(jobID, nil, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var verifyAnomaly *Anomaly
	for i := range got {
		if got[i].Metric == "verify_outcome" {
			verifyAnomaly = &got[i]
			break
		}
	}
	if verifyAnomaly == nil {
		t.Fatalf("expected verify_outcome anomaly on pass→fail regression, got %v", got)
	}
	if verifyAnomaly.Severity != SeverityCritical {
		t.Errorf("Severity: want critical, got %q", verifyAnomaly.Severity)
	}

	// Details must contain newest_status and previous_status.
	var dd map[string]interface{}
	if err := json.Unmarshal([]byte(verifyAnomaly.Details), &dd); err != nil {
		t.Fatalf("Details not valid JSON: %v", err)
	}
	if dd["newest_status"] != "failed" {
		t.Errorf("Details.newest_status: want %q, got %v", "failed", dd["newest_status"])
	}
	if dd["previous_status"] != "passed" {
		t.Errorf("Details.previous_status: want %q, got %v", "passed", dd["previous_status"])
	}
}

// TestReliability_VerifyRegressionSuppressedFewRuns verifies that when fewer
// than 2 verify runs exist for the job, no verify_outcome anomaly is emitted.
func TestReliability_VerifyRegressionSuppressedFewRuns(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	jobID := seedReliabilityJob(t, d)
	rpID := seedJobRunAndRestorePoint(t, d, jobID)

	// Only one verify run.
	seedVerifyRun(t, d, rpID, "failed", -1*time.Hour)

	ec := buildReliabilityEC(jobID, nil, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, a := range got {
		if a.Metric == "verify_outcome" {
			t.Errorf("expected no verify_outcome with <2 verify runs, got %+v", a)
		}
	}
}

// TestReliability_VerifyRegressionSuppressedBothFailed verifies that when
// both the newest and previous verify run are "failed" (not a pass→fail
// transition), no verify_outcome anomaly is emitted.
func TestReliability_VerifyRegressionSuppressedBothFailed(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	jobID := seedReliabilityJob(t, d)
	rpID := seedJobRunAndRestorePoint(t, d, jobID)

	seedVerifyRun(t, d, rpID, "failed", -2*time.Hour) // older
	seedVerifyRun(t, d, rpID, "failed", -1*time.Hour) // newest — both failed → no regression

	ec := buildReliabilityEC(jobID, nil, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, a := range got {
		if a.Metric == "verify_outcome" {
			t.Errorf("expected no verify_outcome when both runs failed (not a regression), got %+v", a)
		}
	}
}

// TestReliability_BothSignalsFire verifies that both failure_streak and
// verify_outcome can fire in the same Evaluate call, returning 2 anomalies.
func TestReliability_BothSignalsFire(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	jobID := seedReliabilityJob(t, d)
	rpID := seedJobRunAndRestorePoint(t, d, jobID)

	// Seed verify regression: older=passed, newer=failed.
	seedVerifyRun(t, d, rpID, "passed", -2*time.Hour)
	seedVerifyRun(t, d, rpID, "failed", -1*time.Hour)

	// Build a run history with streak at threshold (balanced=2).
	threshold := SensBalanced.Streak()
	runs := make([]db.JobRun, threshold+1)
	for i := 0; i < threshold; i++ {
		runs[i] = makeRun(int64(500+i), "failed", 0)
	}
	runs[threshold] = makeRun(int64(500+threshold), "success", 0)

	ec := buildReliabilityEC(jobID, runs, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	seen := map[string]bool{}
	for _, a := range got {
		seen[a.Metric] = true
	}
	if !seen["failure_streak"] {
		t.Errorf("expected failure_streak anomaly in combined result")
	}
	if !seen["verify_outcome"] {
		t.Errorf("expected verify_outcome anomaly in combined result")
	}
}

// TestReliability_StrictSensitivity verifies that with "strict" sensitivity
// (Streak()==1), a single trailing failure is enough to fire.
func TestReliability_StrictSensitivity(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	runs := []db.JobRun{
		makeRun(601, "failed", 0),
		makeRun(600, "success", 0),
	}
	ec := buildReliabilityEC(1, runs, "strict")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var streakAnomaly *Anomaly
	for i := range got {
		if got[i].Metric == "failure_streak" {
			streakAnomaly = &got[i]
			break
		}
	}
	if streakAnomaly == nil {
		t.Fatalf("expected failure_streak for strict sensitivity with 1 failure, got %v", got)
	}
}

// TestReliability_EmptyRecentRuns verifies that an empty RecentRuns slice
// produces no failure_streak anomaly (no panic, returns nil for streak).
func TestReliability_EmptyRecentRuns(t *testing.T) {
	d := openReliabilityDB(t)
	det := NewReliabilityDetector(d)

	ec := buildReliabilityEC(1, nil, "balanced")
	got, err := det.Evaluate(ec)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, a := range got {
		if a.Metric == "failure_streak" {
			t.Errorf("expected no failure_streak with empty RecentRuns, got %+v", a)
		}
	}
}
