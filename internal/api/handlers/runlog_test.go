package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ruaan-deysel/vault/internal/db"
)

// newRunLogTestServer builds a chi router with only the run-log route,
// plus a seeded job/run pair.
func newRunLogTestServer(t *testing.T, seedEntries bool) (*chi.Mux, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	jobID, err := database.CreateJob(db.Job{Name: "runlog-api"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if seedEntries {
		for _, m := range []string{"Backup started", "Backed up web (container)"} {
			if _, err := database.AppendRunLog(context.Background(), db.RunLogEntry{RunID: runID, Level: "info", Message: m}); err != nil {
				t.Fatalf("seed entry: %v", err)
			}
		}
	}

	h := NewRunLogHandler(database)
	r := chi.NewRouter()
	r.Get("/runs/{runId}/logs", h.List)
	return r, runID
}

func TestRunLogHandlerList(t *testing.T) {
	tests := []struct {
		name       string
		path       string // %d replaced with seeded run ID when present
		seed       bool
		wantStatus int
		wantCount  int
		wantLastID int64
	}{
		{name: "seeded run returns entries oldest first with last id", path: "/runs/%d/logs", seed: true, wantStatus: http.StatusOK, wantCount: 2, wantLastID: 2},
		{name: "after tails only newer entries", path: "/runs/%d/logs?after=1", seed: true, wantStatus: http.StatusOK, wantCount: 1, wantLastID: 2},
		{name: "unseeded run returns empty entries and zero last id", path: "/runs/%d/logs", seed: false, wantStatus: http.StatusOK, wantCount: 0, wantLastID: 0},
		{name: "unknown run returns 404", path: "/runs/999999/logs", seed: false, wantStatus: http.StatusNotFound},
		{name: "non-numeric run id returns 400", path: "/runs/abc/logs", seed: false, wantStatus: http.StatusBadRequest},
		{name: "zero limit returns 400", path: "/runs/%d/logs?limit=0", seed: true, wantStatus: http.StatusBadRequest},
		{name: "negative after returns 400", path: "/runs/%d/logs?after=-1", seed: true, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, runID := newRunLogTestServer(t, tt.seed)
			urlPath := tt.path
			if strings.Contains(tt.path, "%d") {
				urlPath = fmt.Sprintf(tt.path, runID)
			}
			req := httptest.NewRequest(http.MethodGet, urlPath, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var body struct {
				RunID   int64            `json:"run_id"`
				Entries []db.RunLogEntry `json:"entries"`
				LastID  int64            `json:"last_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if len(body.Entries) != tt.wantCount {
				t.Fatalf("entries = %d, want %d", len(body.Entries), tt.wantCount)
			}
			if body.LastID != tt.wantLastID {
				t.Fatalf("last_id = %d, want %d", body.LastID, tt.wantLastID)
			}
			if body.RunID != runID {
				t.Fatalf("run_id = %d, want %d", body.RunID, runID)
			}
			for i := 1; i < len(body.Entries); i++ {
				if body.Entries[i].ID <= body.Entries[i-1].ID {
					t.Fatalf("entries not ascending by id: %+v", body.Entries)
				}
			}
		})
	}
}
