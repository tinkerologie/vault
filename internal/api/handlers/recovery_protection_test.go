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

// TestRecoveryPlan_SharesTheHealthClassification checks Disaster Recovery uses
// the health summary's per-item rule (#708): membership decides protection,
// and an old Restore Point is out of date rather than ready.
func TestRecoveryPlan_SharesTheHealthClassification(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "daily", Enabled: true, Schedule: "0 2 * * *"})
	seedProtectedItem(t, d, jobID, "fresh", time.Hour)
	seedProtectedItem(t, d, jobID, "stale", 9*healthDay)
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: "new"}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	w := httptest.NewRecorder()
	NewRecoveryHandler(d, "v").GetPlan(w, httptest.NewRequest(http.MethodGet, "/api/v1/recovery/plan", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Steps []struct {
			Status string `json:"status"`
			Items  []struct {
				Name            string     `json:"name"`
				HasRestorePoint bool       `json:"has_restore_point"`
				OutOfDate       bool       `json:"out_of_date"`
				LastBackup      *time.Time `json:"last_backup"`
			} `json:"items"`
		} `json:"steps"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 2 {
		t.Fatalf("steps = %+v, want install + containers", resp.Steps)
	}
	containers := resp.Steps[1]
	if containers.Status != "warning" {
		t.Errorf("step status = %q, want warning", containers.Status)
	}
	got := map[string][2]bool{}
	for _, item := range containers.Items {
		got[item.Name] = [2]bool{item.HasRestorePoint, item.OutOfDate}
		if item.Name == "new" && item.LastBackup != nil {
			t.Errorf("new has last_backup %v, want none", item.LastBackup)
		}
	}
	want := map[string][2]bool{"fresh": {true, false}, "stale": {true, true}, "new": {false, false}}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = (has_restore_point, out_of_date) %v, want %v", name, got[name], w)
		}
	}
	if !slices.Equal(resp.Warnings, []string{"new has no restore points", "stale is out of date: last backed up " + time.Now().Add(-9*healthDay).Format("Jan 2")}) &&
		!slices.Equal(resp.Warnings, []string{"stale is out of date: last backed up " + time.Now().Add(-9*healthDay).Format("Jan 2"), "new has no restore points"}) {
		t.Errorf("warnings = %v, want one for the pending item and one for the out-of-date item", resp.Warnings)
	}
}
