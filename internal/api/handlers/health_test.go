package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

func TestNewHealthHandler(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestHealthSummary_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/health/summary", nil)
	h.Summary(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, key := range []string{"health_score", "total_items", "protected_items",
		"protection_pct", "success_rate", "recent_success", "recent_failed"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("response missing key %q", key)
		}
	}

	// Empty DB should yield zeros.
	assertFloat(t, resp, "total_items", 0)
	assertFloat(t, resp, "protected_items", 0)
	assertFloat(t, resp, "health_score", 0)
	assertFloat(t, resp, "success_rate", 0)
}

func TestHealthSummary_WithSuccessfulRun(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	// Create a storage destination.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "test-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// Create a job.
	jobID, err := d.CreateJob(db.Job{
		Name: "test-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Add a job item.
	_, err = d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "mycontainer",
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	// Create a successful job run.
	runID, err := d.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "running", BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	now := time.Now().UTC()
	run := db.JobRun{
		ID:          runID,
		JobID:       jobID,
		Status:      "success",
		BackupType:  "full",
		CompletedAt: &now,
	}
	if err := d.UpdateJobRun(run); err != nil {
		t.Fatalf("update run: %v", err)
	}

	// Create a restore point so item is "protected".
	_, err = d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "/backups/mycontainer",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/health/summary", nil)
	h.Summary(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertFloat(t, resp, "total_items", 1)
	assertFloat(t, resp, "protected_items", 1)
	assertFloat(t, resp, "recent_success", 1)
	assertFloat(t, resp, "recent_failed", 0)

	// success_rate should be 100%.
	if rate, ok := resp["success_rate"].(float64); !ok || rate != 100 {
		t.Errorf("success_rate = %v, want 100", resp["success_rate"])
	}

	// health_score must be > 0.
	if hs, ok := resp["health_score"].(float64); !ok || hs <= 0 {
		t.Errorf("health_score = %v, want > 0", resp["health_score"])
	}
}

// TestHealthSummary_PendingItemNotInRestorePoint covers the case the user hit:
// an item added to a job whose existing restore points do not contain it must
// count as pending (not protected), and must appear in pending_keys.
func TestHealthSummary_PendingItemNotInRestorePoint(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "pending-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "pending-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// Two items configured; the restore point only ever contained "plex".
	for _, name := range []string{"plex", "privoxyvpn"} {
		if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: name}); err != nil {
			t.Fatalf("add item %s: %v", name, err)
		}
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "run1", Metadata: `{"item_sizes":{"plex":12345}}`,
	}); err != nil {
		t.Fatalf("create rp: %v", err)
	}

	w := httptest.NewRecorder()
	h.Summary(w, newReq(http.MethodGet, "/api/v1/health/summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		TotalItems     int      `json:"total_items"`
		ProtectedItems int      `json:"protected_items"`
		ProtectedKeys  []string `json:"protected_keys"`
		PendingKeys    []string `json:"pending_keys"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalItems != 2 || resp.ProtectedItems != 1 {
		t.Fatalf("total=%d protected=%d, want 2/1", resp.TotalItems, resp.ProtectedItems)
	}
	if !contains(resp.ProtectedKeys, "container:plex") {
		t.Errorf("protected_keys %v should contain container:plex", resp.ProtectedKeys)
	}
	if !contains(resp.PendingKeys, "container:privoxyvpn") {
		t.Errorf("pending_keys %v should contain container:privoxyvpn", resp.PendingKeys)
	}
	if contains(resp.ProtectedKeys, "container:privoxyvpn") {
		t.Errorf("privoxyvpn must not be protected; protected_keys=%v", resp.ProtectedKeys)
	}
}

func contains(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

func TestHealthSummary_WithFailedRun(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "test-dest2", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "fail-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	_, err = d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "failcontainer",
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	runID, err := d.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "running", BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	now := time.Now().UTC()
	if err := d.UpdateJobRun(db.JobRun{
		ID:          runID,
		JobID:       jobID,
		Status:      "failed",
		BackupType:  "full",
		CompletedAt: &now,
	}); err != nil {
		t.Fatalf("update run: %v", err)
	}

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/health/summary", nil)
	h.Summary(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	assertFloat(t, resp, "recent_failed", 1)
	assertFloat(t, resp, "recent_success", 0)
	assertFloat(t, resp, "success_rate", 0)
}

// assertFloat checks that a map value matches the expected float64.
func assertFloat(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Errorf("key %q: type %T, want float64", key, m[key])
		return
	}
	if v != want {
		t.Errorf("key %q = %v, want %v", key, v, want)
	}
}

// TestHealthSummary_ItemInTwoJobsCountedOnce is the regression test for the QA
// finding that /health/summary counted job-item PAIRS rather than distinct
// items: a VM backed up by two jobs appeared twice in protected_keys and
// inflated protected_items/total_items.
func TestHealthSummary_ItemInTwoJobsCountedOnce(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "dup-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// Two jobs that both back up the same VM, each with its own restore point.
	for _, jobName := range []string{"vm-job-a", "vm-job-b"} {
		jobID, err := d.CreateJob(db.Job{
			Name: jobName, Enabled: true, StorageDestID: destID,
			BackupTypeChain: "full", Schedule: "@daily",
		})
		if err != nil {
			t.Fatalf("create job %s: %v", jobName, err)
		}
		if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "vm", ItemName: "Fedora"}); err != nil {
			t.Fatalf("add item to %s: %v", jobName, err)
		}
		runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
		if err != nil {
			t.Fatalf("create run for %s: %v", jobName, err)
		}
		if _, err := d.CreateRestorePoint(db.RestorePoint{
			JobRunID: runID, JobID: jobID, BackupType: "full",
			StoragePath: jobName, Metadata: `{"item_sizes":{"Fedora":999}}`,
		}); err != nil {
			t.Fatalf("create rp for %s: %v", jobName, err)
		}
	}

	w := httptest.NewRecorder()
	h.Summary(w, newReq(http.MethodGet, "/api/v1/health/summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		TotalItems     int      `json:"total_items"`
		ProtectedItems int      `json:"protected_items"`
		ProtectedKeys  []string `json:"protected_keys"`
		PendingKeys    []string `json:"pending_keys"`
		ProtectionPct  int      `json:"protection_pct"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.TotalItems != 1 || resp.ProtectedItems != 1 {
		t.Errorf("total=%d protected=%d, want 1/1 (one distinct VM across two jobs)",
			resp.TotalItems, resp.ProtectedItems)
	}
	if got := countOccurrences(resp.ProtectedKeys, "vm:Fedora"); got != 1 {
		t.Errorf("vm:Fedora appears %d times in protected_keys %v, want exactly 1", got, resp.ProtectedKeys)
	}
	if resp.ProtectionPct != 100 {
		t.Errorf("protection_pct = %d, want 100", resp.ProtectionPct)
	}
}

// TestHealthSummary_ProtectedByOneJobNotAlsoPending covers the contradictory
// state the pair-counting bug produced: when an item belongs to two jobs and
// only one has a restore point, the item must be reported protected ONCE and
// must NOT also appear in pending_keys.
func TestHealthSummary_ProtectedByOneJobNotAlsoPending(t *testing.T) {
	d := newTestDB(t)
	h := NewHealthHandler(d)

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "mixed-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// Job A has backed the item up.
	jobA, err := d.CreateJob(db.Job{
		Name: "backed-up-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job a: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{JobID: jobA, ItemType: "container", ItemName: "grafana"}); err != nil {
		t.Fatalf("add item a: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobA, Status: "success", BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobA, BackupType: "full",
		StoragePath: "a", Metadata: `{"item_sizes":{"grafana":42}}`,
	}); err != nil {
		t.Fatalf("create rp: %v", err)
	}

	// Job B holds the same item but has never run.
	jobB, err := d.CreateJob(db.Job{
		Name: "never-run-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job b: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{JobID: jobB, ItemType: "container", ItemName: "grafana"}); err != nil {
		t.Fatalf("add item b: %v", err)
	}

	w := httptest.NewRecorder()
	h.Summary(w, newReq(http.MethodGet, "/api/v1/health/summary", nil))
	var resp struct {
		TotalItems     int      `json:"total_items"`
		ProtectedItems int      `json:"protected_items"`
		ProtectedKeys  []string `json:"protected_keys"`
		PendingKeys    []string `json:"pending_keys"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !contains(resp.ProtectedKeys, "container:grafana") {
		t.Errorf("protected_keys %v must contain container:grafana", resp.ProtectedKeys)
	}
	if contains(resp.PendingKeys, "container:grafana") {
		t.Errorf("container:grafana must not be in BOTH protected and pending; pending_keys=%v", resp.PendingKeys)
	}
	if resp.TotalItems != 1 || resp.ProtectedItems != 1 {
		t.Errorf("total=%d protected=%d, want 1/1", resp.TotalItems, resp.ProtectedItems)
	}
}

func countOccurrences(xs []string, want string) int {
	n := 0
	for _, x := range xs {
		if x == want {
			n++
		}
	}
	return n
}

// seedHealthRun records a finished run with a chosen age.
func seedHealthRun(t *testing.T, d *db.DB, jobID int64, status, runType string, age time.Duration) {
	t.Helper()
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full", RunType: runType})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	completed := time.Now().UTC().Add(-age)
	if err := d.UpdateJobRun(db.JobRun{ID: runID, JobID: jobID, Status: status, BackupType: "full", CompletedAt: &completed}); err != nil {
		t.Fatalf("update run: %v", err)
	}
	if _, err := d.Exec(`UPDATE job_runs SET started_at = ?, completed_at = ?, run_type = ? WHERE id = ?`, completed, completed, runType, runID); err != nil {
		t.Fatalf("age run: %v", err)
	}
}

func seedHealthJob(t *testing.T, d *db.DB, job db.Job) int64 {
	t.Helper()
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: job.Name + "-dest", Type: "local", Config: `{"path":"/tmp"}`})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	job.StorageDestID = destID
	job.BackupTypeChain = "full"
	jobID, err := d.CreateJob(job)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !job.Enabled {
		if _, err := d.Exec(`UPDATE jobs SET enabled = 0 WHERE id = ?`, jobID); err != nil {
			t.Fatalf("disable job: %v", err)
		}
	}
	return jobID
}

func fetchHealthSummary(t *testing.T, d *db.DB) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	NewHealthHandler(d).Summary(w, newReq(http.MethodGet, "/api/v1/health/summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

const healthHour = time.Hour
const healthDay = 24 * time.Hour

func TestHealthSummary_CountsUnfinishedBackupsAcrossEveryJob(t *testing.T) {
	d := newTestDB(t)
	// More jobs than the Dashboard samples: only the last has anything to report.
	var last int64
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		last = seedHealthJob(t, d, db.Job{Name: name, Enabled: true, Schedule: "0 2 * * *"})
	}
	seedHealthRun(t, d, last, "completed", "backup", 5*healthHour)
	seedHealthRun(t, d, last, "partial", "backup", 4*healthHour)
	seedHealthRun(t, d, last, "skipped", "backup", 3*healthHour)
	seedHealthRun(t, d, last, "skipped", "", 2*healthHour)
	seedHealthRun(t, d, last, "cancelled", "backup", healthHour)

	resp := fetchHealthSummary(t, d)
	assertFloat(t, resp, "recent_partial", 1)
	assertFloat(t, resp, "recent_skipped", 2)
	assertFloat(t, resp, "recent_cancelled", 1)
	// One success in four scored runs: the partial is a warning, not a score.
	assertFloat(t, resp, "success_rate", 25)
}

func TestHealthSummary_RestoreRunsDoNotCountAsBackups(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "restored", Enabled: true, Schedule: "0 2 * * *"})
	seedHealthRun(t, d, jobID, "completed", "restore", healthHour)
	seedHealthRun(t, d, jobID, "cancelled", "restore", healthHour)
	seedHealthRun(t, d, jobID, "partial", "restore", healthHour)

	resp := fetchHealthSummary(t, d)
	assertFloat(t, resp, "recent_partial", 0)
	assertFloat(t, resp, "recent_cancelled", 0)
	if resp["last_success_at"] != nil {
		t.Errorf("last_success_at = %v, a restore is not a backup", resp["last_success_at"])
	}
}

func TestHealthSummary_LastSuccessSurvivesALongRunOfSkips(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "offline", Enabled: true, Schedule: "0 2 * * *"})
	seedHealthRun(t, d, jobID, "completed", "backup", 400*healthDay)
	for i := range 12 {
		seedHealthRun(t, d, jobID, "skipped", "backup", time.Duration(12-i)*healthDay)
	}

	resp := fetchHealthSummary(t, d)
	if resp["last_success_at"] == nil {
		t.Fatal("last_success_at is nil: the skips hid the last success")
	}
	assertFloat(t, resp, "recent_skipped", 10)
	assertFloat(t, resp, "success_rate", 0)
	stale, _ := resp["stale_jobs"].([]any)
	if len(stale) != 1 {
		t.Fatalf("stale_jobs = %v, want the offline job", resp["stale_jobs"])
	}
	entry := stale[0].(map[string]any)
	if entry["name"] != "offline" || entry["enabled"] != true {
		t.Errorf("stale job = %v", entry)
	}
}

func TestHealthSummary_StaleJobs(t *testing.T) {
	cases := []struct {
		name     string
		job      db.Job
		age      time.Duration
		wantName bool
	}{
		{"daily job within its grace", db.Job{Enabled: true, Schedule: "0 2 * * *"}, 2 * healthDay, false},
		{"daily job past its grace", db.Job{Enabled: true, Schedule: "0 2 * * *"}, 4 * healthDay, true},
		{"monthly job within its grace", db.Job{Enabled: true, Schedule: "0 2 1 * *"}, 45 * healthDay, false},
		{"disabled job, recent backup", db.Job{Schedule: "0 2 * * *"}, 10 * healthDay, false},
		{"disabled job, old backup", db.Job{Schedule: "0 2 * * *"}, 40 * healthDay, true},
		{"manual job, recent backup", db.Job{Enabled: true}, 10 * healthDay, false},
		{"manual job, old backup", db.Job{Enabled: true}, 40 * healthDay, true},
		{"schedule cron can't parse", db.Job{Enabled: true, Schedule: "0 2 L * *"}, 10 * healthDay, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			tc.job.Name = "job"
			seedHealthRun(t, d, seedHealthJob(t, d, tc.job), "completed", "backup", tc.age)
			stale, _ := fetchHealthSummary(t, d)["stale_jobs"].([]any)
			if got := len(stale) == 1; got != tc.wantName {
				t.Errorf("stale_jobs = %v, want stale=%v", stale, tc.wantName)
			}
		})
	}
}

func TestHealthSummary_NeverRunJobIsNotStale(t *testing.T) {
	d := newTestDB(t)
	seedHealthJob(t, d, db.Job{Name: "new", Enabled: true, Schedule: "0 2 * * *"})
	stale, ok := fetchHealthSummary(t, d)["stale_jobs"].([]any)
	if !ok || len(stale) != 0 {
		t.Errorf("stale_jobs = %v, want an empty list", stale)
	}
}

func TestHealthSummary_StaleFromRestorePointWhenHistoryPurged(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "purged", Enabled: true, Schedule: "0 2 * * *"})
	seedHealthRun(t, d, jobID, "completed", "backup", 100*healthDay)
	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("get runs: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{JobRunID: runs[0].ID, JobID: jobID, BackupType: "full", StoragePath: "/tmp/rp"})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}
	if _, err := d.Exec(`UPDATE restore_points SET created_at = ? WHERE id = ?`, time.Now().UTC().Add(-100*healthDay), rpID); err != nil {
		t.Fatalf("age restore point: %v", err)
	}
	if _, err := d.Exec(`UPDATE job_runs SET status = 'failed' WHERE job_id = ?`, jobID); err != nil {
		t.Fatalf("drop success: %v", err)
	}
	stale, _ := fetchHealthSummary(t, d)["stale_jobs"].([]any)
	if len(stale) != 1 {
		t.Errorf("stale_jobs = %v, want the job dated by its restore point", stale)
	}
}

func TestHealthSummary_ReplicatedJobIsNotStale(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "replica", Enabled: true, Schedule: "0 2 * * *"})
	seedHealthRun(t, d, jobID, "completed", "backup", 100*healthDay)
	if _, err := d.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if _, err := d.Exec(`UPDATE jobs SET source_id = 1 WHERE id = ?`, jobID); err != nil {
		t.Fatalf("mark replicated: %v", err)
	}
	stale, _ := fetchHealthSummary(t, d)["stale_jobs"].([]any)
	if len(stale) != 0 {
		t.Errorf("stale_jobs = %v, want none", stale)
	}
}
