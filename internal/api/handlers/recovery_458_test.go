package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func remapBody458(updates ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"updates": updates})
	return b
}

type remapResp458 struct {
	Results []struct {
		Kind    string `json:"kind"`
		ID      int64  `json:"id"`
		Applied bool   `json:"applied"`
		Error   string `json:"error"`
	} `json:"results"`
}

func doRemap458(t *testing.T, h *RecoveryHandler, body []byte) remapResp458 {
	t.Helper()
	w := httptest.NewRecorder()
	h.PathRemap(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(body))))
	wantStatus458(t, w, http.StatusOK)
	var resp remapResp458
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRecoveryPathRemap458_SharedLockHeld(t *testing.T) {
	t.Parallel()
	h := NewRecoveryHandler(newTestDB(t), "v")
	mu := &sync.Mutex{}
	h.SetMaintenanceLock(mu)
	mu.Lock()
	defer mu.Unlock()
	w := httptest.NewRecorder()
	h.PathRemap(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"updates":[]}`)))
	wantStatus458(t, w, http.StatusConflict)
}

func TestRecoveryPathRemap458_RowErrors(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v")
	newDir := t.TempDir()

	badCfg := seedDest458(t, d, "local", "{not json", false)
	jobID := seedJob(t, d)

	resp := doRemap458(t, h, remapBody458(
		map[string]any{"kind": "storage", "id": 999999, "new_path": newDir},
		map[string]any{"kind": "storage", "id": badCfg, "new_path": newDir},
		map[string]any{"kind": "storage", "id": badCfg, "new_path": "relative/path"},
		map[string]any{"kind": "job_item", "id": 999999, "job_id": jobID, "new_path": newDir},
		map[string]any{"kind": "bogus", "id": 1, "new_path": newDir},
	))
	want := []string{
		"storage destination not found",
		"could not read destination config",
		"doesn't exist",
		"job item not found",
		"unknown kind",
	}
	if len(resp.Results) != len(want) {
		t.Fatalf("results = %+v", resp.Results)
	}
	for i, r := range resp.Results {
		if r.Applied || !strings.Contains(r.Error, want[i]) {
			t.Errorf("result %d = %+v, want error containing %q", i, r, want[i])
		}
	}
}

func TestRecoveryPathRemap458_SaveFailures(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v")
	newDir := t.TempDir()
	destID := seedDest458(t, d, "local", localCfg458("/mnt/gone"), false)
	jobID := seedJob(t, d)
	itemID, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "f", Settings: `{"path":"/mnt/old"}`})
	if err != nil {
		t.Fatal(err)
	}
	failWrites458(t, d, "storage_destinations", "UPDATE")
	failWrites458(t, d, "job_items", "UPDATE")

	resp := doRemap458(t, h, remapBody458(
		map[string]any{"kind": "storage", "id": destID, "new_path": newDir},
		map[string]any{"kind": "job_item", "id": itemID, "job_id": jobID, "new_path": newDir},
	))
	if len(resp.Results) != 2 || resp.Results[0].Error != "saving destination failed" || resp.Results[1].Error != "saving item failed" {
		t.Fatalf("results = %+v", resp.Results)
	}
}

func TestRecoveryPathRemap458_JobItemsQueryError(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v")
	breakTable458(t, d, "job_items")
	resp := doRemap458(t, h, remapBody458(
		map[string]any{"kind": "job_item", "id": 1, "job_id": 1, "new_path": t.TempDir()},
	))
	if len(resp.Results) != 1 || resp.Results[0].Error != "job not found" {
		t.Fatalf("results = %+v", resp.Results)
	}
}

func TestRecoveryPathAudit458(t *testing.T) {
	t.Parallel()

	t.Run("skips malformed and irrelevant rows", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		h := NewRecoveryHandler(d, "v")
		seedDest458(t, d, "local", "{not json", false)
		seedDest458(t, d, "local", `{"path":""}`, false)
		jobID := seedJob(t, d)
		seedJobItem(t, d, jobID, "container", "plex")
		if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "nopath", Settings: `{}`}); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.PathAudit(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusOK)
		var resp struct {
			Entries []pathAuditEntry `json:"entries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		// Only the seedJob destination (a real temp dir) is reported.
		if len(resp.Entries) != 1 || resp.Entries[0].Kind != "storage" || !resp.Entries[0].Exists {
			t.Fatalf("entries = %+v", resp.Entries)
		}
	})

	for _, table := range []string{"storage_destinations", "jobs"} {
		t.Run(table+" error", func(t *testing.T) {
			t.Parallel()
			d := newTestDB(t)
			h := NewRecoveryHandler(d, "v")
			breakTable458(t, d, table)
			w := httptest.NewRecorder()
			h.PathAudit(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			wantStatus458(t, w, http.StatusInternalServerError)
		})
	}

	t.Run("job items error skips job", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		h := NewRecoveryHandler(d, "v")
		seedJob(t, d)
		breakTable458(t, d, "job_items")
		w := httptest.NewRecorder()
		h.PathAudit(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusOK)
	})
}

func TestRecoveryGetPlan458(t *testing.T) {
	t.Parallel()

	t.Run("jobs error", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		_ = d.Close()
		w := httptest.NewRecorder()
		NewRecoveryHandler(d, "v").GetPlan(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("storage error", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		breakTable458(t, d, "storage_destinations")
		w := httptest.NewRecorder()
		NewRecoveryHandler(d, "v").GetPlan(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	for _, table := range []string{"job_items", "restore_points"} {
		t.Run(table+" error tolerated", func(t *testing.T) {
			t.Parallel()
			d := newTestDB(t)
			jobID := seedJob(t, d)
			seedJobItem(t, d, jobID, "container", "plex")
			breakTable458(t, d, table)
			w := httptest.NewRecorder()
			NewRecoveryHandler(d, "v").GetPlan(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			wantStatus458(t, w, http.StatusOK)
		})
	}

	t.Run("stale backup warning", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		jobID := seedJob(t, d)
		seedJobItem(t, d, jobID, "container", "plex")
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		if _, err := d.Exec(`UPDATE restore_points SET created_at = datetime('now', '-30 days') WHERE id = ?`, rpID); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		NewRecoveryHandler(d, "v").GetPlan(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusOK)
		var resp struct {
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], ">7 days ago") {
			t.Errorf("warnings = %v", resp.Warnings)
		}
	})
}
