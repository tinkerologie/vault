package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestRunAcknowledgementUpdatesDashboardHealth(t *testing.T) {
	d := testDB(t)
	jobID, err := d.CreateJob(db.Job{Name: "VMs", BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	cancelledID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "cancelled", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(d, ServerConfig{Addr: ":0"})
	request := func(method, path, body string, dest any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = "127.0.0.1:24085"
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d, body %s", method, path, w.Code, w.Body)
		}
		if err := json.NewDecoder(w.Body).Decode(dest); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	checkHealth := func(wantOutstanding, wantScore int) {
		t.Helper()
		var health struct {
			RecentCancelled      int `json:"recent_cancelled"`
			OutstandingCancelled int `json:"outstanding_cancelled"`
			Score                int `json:"health_score"`
			SuccessRate          int `json:"success_rate"`
		}
		request(http.MethodGet, "/api/v1/health/summary", "", &health)
		if health.RecentCancelled != 1 || health.OutstandingCancelled != wantOutstanding ||
			health.Score != wantScore || health.SuccessRate != 50 {
			t.Fatalf("health = %+v, want 1 historical cancellation, %d outstanding, score %d and 50%% historical success", health, wantOutstanding, wantScore)
		}
	}

	checkHealth(1, 30)
	var acknowledgement map[string]int
	request(http.MethodPost, "/api/v1/runs/ack", fmt.Sprintf(`{"ids":[%d]}`, cancelledID), &acknowledgement)
	if acknowledgement["acknowledged"] != 1 || acknowledgement["skipped"] != 0 {
		t.Fatalf("acknowledgement = %v", acknowledgement)
	}
	checkHealth(0, 60)

	var runs []db.JobRun
	request(http.MethodGet, "/api/v1/history", "", &runs)
	if len(runs) != 2 {
		t.Fatalf("history has %d runs, want 2", len(runs))
	}
	for _, run := range runs {
		if run.ID == cancelledID && run.Status == "cancelled" && run.AcknowledgedAt != nil {
			return
		}
	}
	t.Fatalf("original cancellation and acknowledgement not found in history: %+v", runs)
}
