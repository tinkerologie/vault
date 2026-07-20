package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestHistoryList(t *testing.T) {
	d := newTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "history-list", Type: "local", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		jobID, createErr := d.CreateJob(db.Job{Name: fmt.Sprintf("job-%d", i), StorageDestID: destID})
		if createErr != nil {
			t.Fatal(createErr)
		}
		for j := 0; j < 2; j++ {
			if _, createErr = d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"}); createErr != nil {
				t.Fatal(createErr)
			}
		}
	}

	h := NewHistoryHandler(d)
	for _, tc := range []struct {
		name       string
		query      string
		wantStatus int
		wantRuns   int
		wantPerJob int
	}{
		{name: "default", wantStatus: http.StatusOK, wantRuns: 4, wantPerJob: 2},
		{name: "bounded", query: "?limit_per_job=1", wantStatus: http.StatusOK, wantRuns: 2, wantPerJob: 1},
		{name: "capped", query: "?limit_per_job=1001", wantStatus: http.StatusOK, wantRuns: 4, wantPerJob: 2},
		{name: "invalid", query: "?limit_per_job=zero", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.List(w, newReq(http.MethodGet, "/api/v1/history"+tc.query, nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var runs []db.JobRun
			if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
				t.Fatal(err)
			}
			if len(runs) != tc.wantRuns {
				t.Fatalf("returned %d runs, want %d", len(runs), tc.wantRuns)
			}
			perJob := map[int64]int{}
			for _, run := range runs {
				perJob[run.JobID]++
			}
			if len(perJob) != 2 {
				t.Fatalf("returned runs for %d jobs, want 2: %#v", len(perJob), perJob)
			}
			for jobID, count := range perJob {
				if count != tc.wantPerJob {
					t.Errorf("job %d returned %d runs, want %d", jobID, count, tc.wantPerJob)
				}
			}
		})
	}
}

func TestHistoryListCapsLimitPerJob(t *testing.T) {
	d := newTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "history-cap", Type: "local", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "capped-job", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if _, err = d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"}); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	NewHistoryHandler(d).List(w, newReq(http.MethodGet, "/api/v1/history?limit_per_job=1001", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var runs []db.JobRun
	if err = json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1000 {
		t.Fatalf("returned %d runs, want capped 1000", len(runs))
	}
}

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
