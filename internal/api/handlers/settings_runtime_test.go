package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/diagnostics"
	"github.com/tinkerologie/vault/internal/memlimit"
)

func TestGetRuntime(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	h.SetDiagnosticsCollector(diagnostics.NewCollector(h.db, func() diagnostics.RunnerStatus { return diagnostics.RunnerStatus{} }, "test", nil))

	w := httptest.NewRecorder()
	h.GetRuntime(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings/runtime", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp RuntimeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Runtime.HeapLiveBytes == 0 || resp.Runtime.NumGoroutine == 0 {
		t.Fatalf("runtime not populated: %+v", resp.Runtime)
	}
	if resp.MemoryLimit.Source == "" || resp.SuggestedLimitMB < 512 {
		t.Fatalf("limit state %+v, suggested %d", resp.MemoryLimit, resp.SuggestedLimitMB)
	}
	if resp.UploadWarnings == nil {
		t.Fatal("upload_warnings must be an array, not null")
	}
}

func TestGetRuntimeNotConfigured(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	w := httptest.NewRecorder()
	h.GetRuntime(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings/runtime", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestUploadBudgetWarnings(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// No S3 destinations: nothing to check.
	if got, err := h.uploadBudgetWarnings(512 << 20); err != nil || len(got) != 0 {
		t.Fatalf("no S3: got %v, %v", got, err)
	}

	s3ID, err := h.db.CreateStorageDestination(db.StorageDestination{Name: "b2", Type: "s3", Config: `{"bucket":"x","part_size_mb":64}`})
	if err != nil {
		t.Fatal(err)
	}
	localID, err := h.db.CreateStorageDestination(db.StorageDestination{Name: "disk", Type: "local", Config: `{"path":"/mnt/x"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []db.Job{
		{Name: "big", Enabled: true, StorageDestID: s3ID, MaxParallelUploads: 4},   // 1280 MiB
		{Name: "small", Enabled: true, StorageDestID: s3ID, MaxParallelUploads: 1}, // 320 MiB
		{Name: "off", Enabled: false, StorageDestID: s3ID, MaxParallelUploads: 16},
		{Name: "local", Enabled: true, StorageDestID: localID, MaxParallelUploads: 16},
	} {
		if _, err := h.db.CreateJob(j); err != nil {
			t.Fatal(err)
		}
	}

	if got, _ := h.uploadBudgetWarnings(0); len(got) != 0 {
		t.Fatalf("limit off: got %v", got)
	}
	got, err := h.uploadBudgetWarnings(512 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].JobName != "big" || got[0].Destination != "b2" || got[0].EstimateBytes != 1280<<20 || got[0].LimitBytes != 512<<20 {
		t.Fatalf("warnings = %+v, want only job big at 1280 MiB", got)
	}
}

func TestUpdate_MemoryLimit(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	var calls []int64
	h.SetMemoryLimitChangeHook(func(mb int64) { calls = append(calls, mb) })

	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.Update(w, httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body)))
		return w
	}

	for _, v := range []string{"511", "-1", "abc", "1048577", "1.5"} {
		if w := put(`{"memory_limit_mb":"` + v + `"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("memory_limit_mb=%q: status %d, want 400", v, w.Code)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("hook fired for invalid values: %v", calls)
	}

	w := put(`{"memory_limit_mb":" 2048 "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["memory_limit_mb"] != "2048" {
		t.Fatalf("response memory_limit_mb = %q, want 2048", resp["memory_limit_mb"])
	}
	if stored, _ := h.db.GetSetting("memory_limit_mb", ""); stored != "2048" {
		t.Fatalf("stored = %q", stored)
	}

	if w := put(`{"memory_limit_mb":"0"}`); w.Code != http.StatusOK {
		t.Fatalf("off: status %d", w.Code)
	}
	if len(calls) != 2 || calls[0] != 2048 || calls[1] != 0 {
		t.Fatalf("hook calls = %v, want [2048 0]", calls)
	}

	// Unrelated updates leave the hook alone.
	if w := put(`{"log_level":"info"}`); w.Code != http.StatusOK || len(calls) != 2 {
		t.Fatalf("unrelated update: status %d, calls %v", w.Code, calls)
	}
}

func TestList_MemoryLimitDefault(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["memory_limit_mb"] != "0" {
		t.Fatalf("memory_limit_mb default = %q, want 0", resp["memory_limit_mb"])
	}
}

// TestRuntimeDatabaseErrors covers the paths where the budget check cannot
// read jobs or destinations. Not parallel: it applies a real memory limit.
func TestRuntimeDatabaseErrors(t *testing.T) {
	h := newTestSettingsHandler(t)
	if _, err := h.db.CreateStorageDestination(db.StorageDestination{Name: "b2", Type: "s3", Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	h.SetDiagnosticsCollector(diagnostics.NewCollector(h.db, func() diagnostics.RunnerStatus { return diagnostics.RunnerStatus{} }, "test", nil))
	memlimit.Apply(1 << 20)
	t.Cleanup(func() { memlimit.Apply(0) })

	// Jobs table gone: ListJobs fails after destinations load.
	if _, err := h.db.Exec(`DROP TABLE job_items`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DROP TABLE jobs`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uploadBudgetWarnings(512 << 20); err == nil || !strings.Contains(err.Error(), "listing jobs") {
		t.Fatalf("want a listing jobs error, got %v", err)
	}

	// Database closed: destinations fail too, and the endpoint reports 500.
	_ = h.db.Close()
	if _, err := h.uploadBudgetWarnings(512 << 20); err == nil || !strings.Contains(err.Error(), "listing storage destinations") {
		t.Fatalf("want a listing destinations error, got %v", err)
	}
	w := httptest.NewRecorder()
	h.GetRuntime(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings/runtime", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}
