package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// A ZFS restore's destination is a dataset name, which is not a filesystem
// path; every other item keeps the absolute-path rules.
func TestRestoreDestinationByItemType(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	if _, err := d.AddJobItem(db.JobItem{JobID: id, ItemType: "zfs", ItemName: "tank/data", Settings: `{}`}); err != nil {
		t.Fatal(err)
	}
	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})

	restore := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body["restore_point_id"] = rpID
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", raw), "id", strconv.FormatInt(id, 10))
		h.Restore(w, r)
		return w
	}
	invalid := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "invalid destination")
	}
	for name, body := range map[string]map[string]any{
		"zfs dataset (single)": {"item_name": "tank/data", "item_type": "zfs", "destination": "tank/chosen"},
		"zfs dataset (named)":  {"items": []string{"tank/data"}, "destination": "tank/chosen"},
		"folder path":          {"item_name": "docs", "item_type": "folder", "destination": "/mnt/user/restore"},
	} {
		if w := restore(body); invalid(w) {
			t.Errorf("%s rejected: %s", name, w.Body.String())
		}
	}
	for name, body := range map[string]map[string]any{
		"dataset for a folder": {"item_name": "docs", "item_type": "folder", "destination": "tank/chosen"},
		"path for zfs":         {"item_name": "tank/data", "item_type": "zfs", "destination": "/mnt/user/restore"},
		"bad dataset":          {"item_name": "tank/data", "item_type": "zfs", "destination": "tank/../etc"},
		"neither":              {"item_name": "tank/data", "item_type": "zfs", "destination": "-o/x"},
	} {
		if w := restore(body); !invalid(w) {
			t.Errorf("%s accepted: %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestPreflightAcceptsZFSDatasetDestination(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})
	for dest, wantInvalid := range map[string]bool{"tank/chosen": false, "/mnt/user/appdata": false, "tank/../etc": true} {
		raw, _ := json.Marshal(map[string]any{"destination": dest})
		w := httptest.NewRecorder()
		r := withURLParams(newReq(http.MethodPost, fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/preflight", id, rpID), raw),
			"id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
		h.RestorePointPreflight(w, r)
		if got := strings.Contains(w.Body.String(), "invalid destination"); got != wantInvalid {
			t.Errorf("preflight %q: invalid=%v, want %v (%d %s)", dest, got, wantInvalid, w.Code, w.Body.String())
		}
	}
}
