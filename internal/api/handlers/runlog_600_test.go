package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/db"
)

// TestRunLogHandlerGet600 covers GET /runs/{runId}, which a replicating peer
// reads for a run outside the newest history page. A run that was partial
// only because files were skipped has no failed items, so the status itself
// must come back.
func TestRunLogHandlerGet600(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	jobID, err := database.CreateJob(db.Job{Name: "runs-api"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateJobRun(db.JobRun{ID: runID, Status: "partial", Log: "skipped unreadable files", ItemsDone: 3, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Get("/runs/{runId}", NewRunLogHandler(database).Get)
	get := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/runs/"+id, nil))
		return w
	}

	w := get(fmt.Sprint(runID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var run db.JobRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != runID || run.JobID != jobID || run.Status != "partial" || run.ItemsFailed != 0 || run.ItemsDone != 3 {
		t.Errorf("run = %+v", run)
	}

	for id, want := range map[string]int{"999999": http.StatusNotFound, "0": http.StatusBadRequest, "abc": http.StatusBadRequest} {
		if w := get(id); w.Code != want {
			t.Errorf("GET /runs/%s = %d, want %d", id, w.Code, want)
		}
	}

	_ = database.Close()
	if w := get(fmt.Sprint(runID)); w.Code != http.StatusInternalServerError {
		t.Errorf("GET with a closed database = %d, want 500", w.Code)
	}
}
