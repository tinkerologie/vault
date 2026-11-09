package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestRunAcknowledgement(t *testing.T) {
	d := newTestDB(t)
	jobID, err := d.CreateJob(db.Job{Name: "VMs", BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "failed", RunType: "restore", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	b := &mockBroadcaster{}
	h := NewRunAckHandler(d, b)
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.Acknowledge(w, newReq(http.MethodPost, "/api/v1/runs/ack", []byte(body)))
		return w
	}

	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"malformed", `{"ids":[`, http.StatusBadRequest},
		{"trailing JSON", fmt.Sprintf(`{"ids":[%d]} {}`, runID), http.StatusBadRequest},
		{"empty IDs", `{"ids":[]}`, http.StatusUnprocessableEntity},
		{"nonpositive ID", fmt.Sprintf(`{"ids":[%d,0]}`, runID), http.StatusUnprocessableEntity},
		{"too many IDs", `{"ids":[` + strings.Repeat("1,", 1000) + "1]}", http.StatusUnprocessableEntity},
		{"reason too long", fmt.Sprintf(`{"ids":[%d],"reason":%q}`, runID, strings.Repeat("é", 501)), http.StatusUnprocessableEntity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if w := request(tt.body); w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}

	body := fmt.Sprintf(`{"ids":[%d,%d,999],"reason":%q}`, runID, runID, strings.Repeat("é", 500))
	for _, want := range []map[string]int{
		{"acknowledged": 1, "skipped": 1},
		{"acknowledged": 0, "skipped": 2},
	} {
		w := request(body)
		var response map[string]int
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil || w.Code != http.StatusOK || !reflect.DeepEqual(response, want) {
			t.Fatalf("status = %d, response = %v, error = %v, want %v", w.Code, response, err, want)
		}
	}
	wantEvent := map[string]any{"type": "job_runs_acknowledged", "acknowledged": 1, "ids": []int64{runID, runID, 999}}
	if len(b.messages) != 1 || !reflect.DeepEqual(b.messages[0], wantEvent) {
		t.Fatalf("events = %v, want [%v]", b.messages, wantEvent)
	}
	logs, err := d.ListActivityLogs(10, "", 0)
	if err != nil || len(logs) != 1 || !strings.Contains(logs[0].Message, "Acknowledged 1") {
		t.Fatalf("activity log = %v, error = %v", logs, err)
	}
	run, err := d.GetJobRun(runID)
	if err != nil || run.Status != "failed" || run.AcknowledgedBy == nil || *run.AcknowledgedBy != "api" {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
}

func TestRunAcknowledgementDatabaseError(t *testing.T) {
	d := newTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	h := NewRunAckHandler(d, nil)
	w := httptest.NewRecorder()
	h.Acknowledge(w, newReq(http.MethodPost, "/api/v1/runs/ack", []byte(`{"ids":[1]}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body)
	}
}
