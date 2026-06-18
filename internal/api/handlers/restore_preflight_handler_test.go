package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRestorePointPreflight_Handler exercises the preflight endpoint end-to-end
// against a real local destination so the runner wiring is covered.
func TestRestorePointPreflight_Handler(t *testing.T) {
	h, d := newJobHandlerDB(t)

	// A local destination whose path actually contains the restore point's data.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run1", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "pf-" + nextUnique(), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "pfjob-" + nextUnique(), Schedule: "0 * * * *", Compression: "none",
		Encryption: "none", StorageDestID: destID,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "run1", SizeBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Happy path: 200 with all four checks, OK true.
	req := withURLParams(newReq(http.MethodPost, "/preflight", []byte(`{}`)),
		"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointPreflight(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var res struct {
		OK     bool `json:"ok"`
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Errorf("preflight OK = false, want true; checks: %+v", res.Checks)
	}
	got := map[string]bool{}
	for _, c := range res.Checks {
		got[c.ID] = true
	}
	for _, id := range []string{"reachable", "present", "decryptable", "space"} {
		if !got[id] {
			t.Errorf("missing preflight check %q", id)
		}
	}

	// Unknown restore point -> 404.
	req2 := withURLParams(newReq(http.MethodPost, "/preflight", []byte(`{}`)),
		"id", strconv.FormatInt(jobID, 10), "rpid", "999999")
	w2 := httptest.NewRecorder()
	h.RestorePointPreflight(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("unknown restore point: status = %d, want 404", w2.Code)
	}
}
