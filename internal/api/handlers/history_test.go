package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestNewHistoryHandler(t *testing.T) {
	d := newTestDB(t)
	h := NewHistoryHandler(d)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestHistoryPurge_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	h := NewHistoryHandler(d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/history", nil)
	h.Purge(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestHistoryPurge_WithRunsSeeded(t *testing.T) {
	d := newTestDB(t)
	h := NewHistoryHandler(d)

	// Seed a destination + job so we can create runs.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "hist-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "hist-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := d.CreateJobRun(db.JobRun{
			JobID: jobID, Status: "success", BackupType: "full",
		}); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}

	// Verify runs exist before purge.
	runs, err := d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs before purge, got %d", len(runs))
	}

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/history", nil)
	h.Purge(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}

	// Verify runs are gone.
	runs, err = d.GetJobRuns(jobID, 10)
	if err != nil {
		t.Fatalf("get runs after purge: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs after purge, got %d", len(runs))
	}
}

func TestHistoryPurge_LogsActivity(t *testing.T) {
	d := newTestDB(t)
	h := NewHistoryHandler(d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/history", nil)
	h.Purge(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}

	// The Purge method calls LogActivity; at minimum one entry should appear.
	entries, err := d.ListActivityLogs(10, "system")
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	if len(entries) < 1 {
		t.Errorf("expected at least 1 activity entry after purge, got %d", len(entries))
	}
}

func TestHistoryPurge_DBError(t *testing.T) {
	d := newTestDB(t)
	h := NewHistoryHandler(d)

	// Close the DB to force an error path in PurgeJobRuns.
	_ = d.Close()

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/history", nil)
	h.Purge(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}
