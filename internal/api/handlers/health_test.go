package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
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
