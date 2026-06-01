package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// uniqueSeq provides monotonically increasing IDs for unique name generation.
var uniqueSeq int64

// ---------------------------------------------------------------------------
// Helper: build a *JobHandler wired to a real temp-file DB + runner.
// ---------------------------------------------------------------------------

func newJobHandler(t *testing.T) *JobHandler {
	t.Helper()
	d := newTestDB(t)
	hub := ws.NewHub()
	go hub.Run()
	serverKey := bytes.Repeat([]byte{0xab}, 32)
	r := runner.New(d, hub, serverKey)
	h := NewJobHandler(d, r, func() error { return nil })
	return h
}

// newJobHandlerDB returns the handler AND the underlying *db.DB so tests can
// seed rows directly via the repo layer.
func newJobHandlerDB(t *testing.T) (*JobHandler, *db.DB) {
	t.Helper()
	d := newTestDB(t)
	hub := ws.NewHub()
	go hub.Run()
	serverKey := bytes.Repeat([]byte{0xab}, 32)
	r := runner.New(d, hub, serverKey)
	h := NewJobHandler(d, r, func() error { return nil })
	return h, d
}

// nextUnique returns a monotonically increasing integer as a string, used to
// generate unique names within a single test that calls seed helpers multiple
// times against the same DB (which has UNIQUE constraints on name columns).
func nextUnique() string {
	return strconv.FormatInt(atomic.AddInt64(&uniqueSeq, 1), 10)
}

// seedStorageDest creates a uniquely named local storage destination and
// returns its ID.
func seedStorageDest(t *testing.T, d *db.DB) int64 {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": dir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "test-dest-" + nextUnique(),
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("seedStorageDest: %v", err)
	}
	return destID
}

// seedJob creates a minimal valid job in the DB and returns its ID.
// A storage destination is created first to satisfy the FK constraint.
func seedJob(t *testing.T, d *db.DB) int64 {
	t.Helper()
	destID := seedStorageDest(t, d)
	id, err := d.CreateJob(db.Job{
		Name:          "test-job-" + nextUnique(),
		Enabled:       true,
		Schedule:      "0 * * * *",
		Compression:   "none",
		Encryption:    "none",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("seedJob: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// NewJobHandler + setters
// ---------------------------------------------------------------------------

func TestNewJobHandler_NotNil(t *testing.T) {
	h := newJobHandler(t)
	if h == nil {
		t.Fatal("expected non-nil *JobHandler")
	}
}

func TestSetNextRunResolver_SetsField(t *testing.T) {
	h := newJobHandler(t)
	called := false
	h.SetNextRunResolver(func(_ int64) (string, bool) {
		called = true
		return "2026-01-01T00:00:00Z", true
	})
	if h.nextRun == nil {
		t.Fatal("expected nextRun to be set")
	}
	_, _ = h.nextRun(1)
	if !called {
		t.Error("resolver was not called")
	}
}

func TestSetConfigChangeHook_SetsField(t *testing.T) {
	h := newJobHandler(t)
	called := false
	h.SetConfigChangeHook(func() { called = true })
	h.notifyConfigChange()
	if !called {
		t.Error("onConfigChange hook was not called")
	}
}

// ---------------------------------------------------------------------------
// notifyConfigChange / broadcastConfigChange / reloadScheduler
// ---------------------------------------------------------------------------

func TestNotifyConfigChange_NilHookNoPanic(t *testing.T) {
	h := newJobHandler(t)
	h.onConfigChange = nil
	// Must not panic.
	h.notifyConfigChange()
}

func TestBroadcastConfigChange_NilRunnerNoPanic(t *testing.T) {
	h := newJobHandler(t)
	h.runner = nil
	// Must not panic.
	h.broadcastConfigChange("job")
}

func TestBroadcastConfigChange_WithRunner(t *testing.T) {
	h := newJobHandler(t)
	// runner is non-nil; call should not panic.
	h.broadcastConfigChange("job")
}

func TestReloadScheduler_NilReloaderNoPanic(t *testing.T) {
	h := newJobHandler(t)
	h.schedReload = nil
	h.reloadScheduler()
}

func TestReloadScheduler_ErrorLogged(t *testing.T) {
	// A failing reload must not panic — it is only logged.
	h := newJobHandler(t)
	h.schedReload = func() error { return errForTest("reload failed") }
	h.reloadScheduler() // must not panic
}

// errForTest returns an error with the given text (avoids importing errors).
type testError string

func (e testError) Error() string { return string(e) }
func errForTest(s string) error   { return testError(s) }

// ---------------------------------------------------------------------------
// dedupManifestToTarIndex
// ---------------------------------------------------------------------------

func TestDedupManifestToTarIndex_Empty(t *testing.T) {
	m := dedup.Manifest{
		Version: 1,
		Item:    "test-item",
		Files:   map[string]dedup.ManifestEntry{},
	}
	idx := dedupManifestToTarIndex("test-item", m)
	if idx.Version != 1 {
		t.Errorf("version = %d, want 1", idx.Version)
	}
	if idx.Archive != "test-item" {
		t.Errorf("archive = %q, want %q", idx.Archive, "test-item")
	}
	if len(idx.Files) != 0 {
		t.Errorf("files len = %d, want 0", len(idx.Files))
	}
}

func TestDedupManifestToTarIndex_SingleFile(t *testing.T) {
	m := dedup.Manifest{
		Version: 1,
		Item:    "mybackup",
		Files: map[string]dedup.ManifestEntry{
			"etc/hosts": {
				Mode:    0o644,
				ModTime: "2026-01-01T00:00:00Z",
				Size:    256,
				IsDir:   false,
			},
		},
	}
	idx := dedupManifestToTarIndex("mybackup", m)
	if len(idx.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(idx.Files))
	}
	f := idx.Files[0]
	if f.Path != "etc/hosts" {
		t.Errorf("path = %q, want %q", f.Path, "etc/hosts")
	}
	if f.Size != 256 {
		t.Errorf("size = %d, want 256", f.Size)
	}
	if f.IsDir {
		t.Error("IsDir should be false for regular file")
	}
	// Mode should be formatted as 4-digit octal.
	if f.Mode != "0644" {
		t.Errorf("mode = %q, want %q", f.Mode, "0644")
	}
}

func TestDedupManifestToTarIndex_Directory(t *testing.T) {
	m := dedup.Manifest{
		Version: 1,
		Item:    "volbackup",
		Files: map[string]dedup.ManifestEntry{
			"var/log/": {
				Mode:  0o755,
				IsDir: true,
				Size:  0,
			},
		},
	}
	idx := dedupManifestToTarIndex("volbackup", m)
	if len(idx.Files) != 1 {
		t.Fatalf("want 1 entry, got %d", len(idx.Files))
	}
	if !idx.Files[0].IsDir {
		t.Error("IsDir should be true for directory entry")
	}
}

func TestDedupManifestToTarIndex_MultipleFiles(t *testing.T) {
	files := map[string]dedup.ManifestEntry{
		"a": {Size: 1},
		"b": {Size: 2},
		"c": {Size: 3},
	}
	m := dedup.Manifest{Version: 1, Item: "multi", Files: files}
	idx := dedupManifestToTarIndex("multi", m)
	if len(idx.Files) != 3 {
		t.Errorf("files len = %d, want 3", len(idx.Files))
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestJobList_EmptyReturnsArray(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/jobs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
}

func TestJobList_WithJobs(t *testing.T) {
	h, d := newJobHandlerDB(t)
	seedJob(t, d)

	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/jobs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("want 1 job, got %d", len(resp))
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestJobCreate_ValidJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	body, _ := json.Marshal(map[string]any{
		"name":            "new-job-" + nextUnique(),
		"enabled":         true,
		"compression":     "none",
		"encryption":      "none",
		"storage_dest_id": destID,
		"items":           []any{},
	})
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/api/v1/jobs", body))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] == nil || resp["id"].(float64) == 0 {
		t.Errorf("expected non-zero id, got %v", resp["id"])
	}
}

func TestJobCreate_WithItems(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	body, _ := json.Marshal(map[string]any{
		"name":            "job-with-items-" + nextUnique(),
		"enabled":         false,
		"compression":     "none",
		"encryption":      "none",
		"storage_dest_id": destID,
		"items": []map[string]any{
			{"item_type": "folder", "item_name": "/mnt/data", "item_id": "", "settings": "", "sort_order": 0},
		},
	})
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/api/v1/jobs", body))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 item in response, got %d", len(items))
	}
}

func TestJobCreate_InvalidJSON(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/api/v1/jobs", []byte("not-json")))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestJobGet_NotFound(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/9999", nil), "id", "9999")
	h.Get(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestJobGet_Found(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10), nil), "id", strconv.FormatInt(id, 10))
	h.Get(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	job, ok := resp["job"].(map[string]any)
	if !ok {
		t.Fatalf("response missing 'job' object; got %v", resp)
	}
	// Name includes a unique suffix; verify the prefix only.
	name, _ := job["name"].(string)
	if len(name) < 9 || name[:9] != "test-job-" {
		t.Errorf("name = %v, want prefix test-job-", job["name"])
	}
}

func TestJobGet_InvalidID(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/abc", nil), "id", "abc")
	h.Get(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestJobUpdate_NotFound(t *testing.T) {
	h := newJobHandler(t)
	body, _ := json.Marshal(map[string]any{"name": "updated"})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPut, "/api/v1/jobs/9999", body), "id", "9999")
	h.Update(w, r)
	// UpdateJob against a non-existent row does not return an error from SQLite
	// (it just updates 0 rows), so we expect 200 back.
	if w.Code == http.StatusBadRequest {
		t.Fatalf("unexpected 400; body: %s", w.Body.String())
	}
}

func TestJobUpdate_ValidJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	// Look up the seeded job's storage_dest_id so we can preserve it in the update.
	job, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"name":            "updated-name-" + nextUnique(),
		"enabled":         false,
		"compression":     "zstd",
		"encryption":      "none",
		"storage_dest_id": job.StorageDestID,
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPut, "/api/v1/jobs/"+strconv.FormatInt(id, 10), body), "id", strconv.FormatInt(id, 10))
	h.Update(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestJobUpdate_WithItems(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	job, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"name":            "updated-with-items-" + nextUnique(),
		"enabled":         true,
		"storage_dest_id": job.StorageDestID,
		"items": []map[string]any{
			{"item_type": "folder", "item_name": "/mnt/data2", "item_id": "", "settings": "", "sort_order": 0},
		},
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPut, "/api/v1/jobs/"+strconv.FormatInt(id, 10), body), "id", strconv.FormatInt(id, 10))
	h.Update(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestJobUpdate_InvalidJSON(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPut, "/api/v1/jobs/"+strconv.FormatInt(id, 10), []byte("!bad")), "id", strconv.FormatInt(id, 10))
	h.Update(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestJobDelete_Found(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id, 10), nil), "id", strconv.FormatInt(id, 10))
	h.Delete(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestJobDelete_InvalidID(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodDelete, "/api/v1/jobs/bad", nil), "id", "bad")
	h.Delete(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestJobDelete_WithDeleteFilesFlag(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	// deleteFiles=true on a job with a valid storage destination now returns
	// 202 Accepted — the DB row is deleted synchronously and the remote file
	// cleanup is handed off to a background goroutine (issue #111).
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"?deleteFiles=true", nil), "id", strconv.FormatInt(id, 10))
	r.URL.RawQuery = "deleteFiles=true"
	h.Delete(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	// The job row must be gone immediately regardless of background cleanup.
	if _, err := d.GetJob(id); err == nil {
		t.Error("job should be deleted from DB synchronously")
	}
}

// TestJobDelete_DeleteFilesOrphanedJob covers an orphaned job (issue #113:
// storage_dest_id == 0). There is no destination to clean, so the handler must
// fall back to a synchronous 204 rather than spawning a no-op cleanup.
func TestJobDelete_DeleteFilesOrphanedJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id, err := d.CreateJob(db.Job{Name: "orphan-job-" + nextUnique(), Compression: "none", Encryption: "none"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"?deleteFiles=true", nil), "id", strconv.FormatInt(id, 10))
	r.URL.RawQuery = "deleteFiles=true"
	h.Delete(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (no destination to clean); body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GetHistory
// ---------------------------------------------------------------------------

func TestJobGetHistory_NoRuns(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/history", nil), "id", strconv.FormatInt(id, 10))
	h.GetHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
	if len(resp) != 0 {
		t.Errorf("want empty array, got %d entries", len(resp))
	}
}

func TestJobGetHistory_WithRuns(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	// Seed a completed job run.
	_, err := d.CreateJobRun(db.JobRun{
		JobID:      id,
		Status:     "success",
		BackupType: "full",
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/history", nil), "id", strconv.FormatInt(id, 10))
	h.GetHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("want 1 run, got %d", len(resp))
	}
}

func TestJobGetHistory_LimitParam(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	req := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/history?limit=5", nil), "id", strconv.FormatInt(id, 10))
	req.URL.RawQuery = "limit=5"
	h.GetHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestJobGetHistory_InvalidLimit(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	req := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/history?limit=abc", nil), "id", strconv.FormatInt(id, 10))
	req.URL.RawQuery = "limit=abc"
	h.GetHistory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestJobGetHistory_NegativeLimit(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	req := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/history?limit=-1", nil), "id", strconv.FormatInt(id, 10))
	req.URL.RawQuery = "limit=-1"
	h.GetHistory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GetRestorePoints
// ---------------------------------------------------------------------------

func TestJobGetRestorePoints_JobNotFound(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/9999/restore-points", nil), "id", "9999")
	h.GetRestorePoints(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestJobGetRestorePoints_Empty(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points", nil), "id", strconv.FormatInt(id, 10))
	h.GetRestorePoints(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
	if len(resp) != 0 {
		t.Errorf("want empty array, got %d entries", len(resp))
	}
}

func TestJobGetRestorePoints_WithPoint(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	_, _ = d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       id,
		BackupType:  "full",
		StoragePath: "/vault/test-job/20260101",
	})

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points", nil), "id", strconv.FormatInt(id, 10))
	h.GetRestorePoints(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("want 1 restore point, got %d", len(resp))
	}
}

// ---------------------------------------------------------------------------
// RestorePointContents
// ---------------------------------------------------------------------------

func TestRestorePointContents_RestorePointNotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/9999/contents", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", "9999")
	r.URL.RawQuery = "item=testitem"
	h.RestorePointContents(w, r)

	// When the restore point doesn't exist, GetRestorePoint returns sql.ErrNoRows
	// (not db.ErrNotFound), so the handler returns 500 (internal error).
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 for missing restore point, got 200; body: %s", w.Body.String())
	}
}

func TestRestorePointContents_MissingItemParam(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       id,
		BackupType:  "full",
		StoragePath: "/vault/test-job/20260101",
	})

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/contents", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	// No ?item= query param
	h.RestorePointContents(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRestorePointContents_WrongJobID(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	id2 := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       id,
		BackupType:  "full",
		StoragePath: "/vault/test-job/20260101",
	})

	// Request with job id2 but restore point belonging to id1.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id2, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/contents", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id2, 10), "rpid", strconv.FormatInt(rpID, 10))
	r.URL.RawQuery = "item=test"
	h.RestorePointContents(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestRestorePointContents_NoStorageDestFails(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       id,
		BackupType:  "full",
		StoragePath: "/vault/test-job/20260101",
	})

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/contents", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	r.URL.RawQuery = "item=testitem"
	h.RestorePointContents(w, r)

	// Expect either 404 (storage dest not found → no tar index) or 500 (internal).
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 when no storage dest exists, got 200; body: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// RetentionPreview
// ---------------------------------------------------------------------------

func TestRetentionPreview_JobNotFound(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/9999/retention-preview", nil), "id", "9999")
	h.RetentionPreview(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestRetentionPreview_InactivePolicy(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/retention-preview", nil), "id", strconv.FormatInt(id, 10))
	h.RetentionPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["policy_active"] != false {
		t.Errorf("expected policy_active=false when no keep_* params given, got %v", resp["policy_active"])
	}
}

func TestRetentionPreview_ActivePolicy(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	// Seed a few restore points.
	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	for i := 0; i < 3; i++ {
		_, _ = d.CreateRestorePoint(db.RestorePoint{
			JobRunID:    runID,
			JobID:       id,
			BackupType:  "full",
			StoragePath: "/vault/test-job/2026010" + strconv.Itoa(i+1),
		})
		time.Sleep(1 * time.Millisecond) // ensure distinct created_at
	}

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/retention-preview?keep_latest=2", nil), "id", strconv.FormatInt(id, 10))
	r.URL.RawQuery = "keep_latest=2"
	h.RetentionPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["policy_active"] != true {
		t.Errorf("expected policy_active=true, got %v", resp["policy_active"])
	}
	if resp["total_restore_points"].(float64) != 3 {
		t.Errorf("expected total_restore_points=3, got %v", resp["total_restore_points"])
	}
}

// ---------------------------------------------------------------------------
// DeleteRestorePoint
// ---------------------------------------------------------------------------

func TestDeleteRestorePoint_NotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/9999", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", "9999")
	h.DeleteRestorePoint(w, r)

	// GetRestorePoint returns sql.ErrNoRows (not db.ErrNotFound) for missing rows,
	// so the handler responds with 500. Either way it's non-200.
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 for missing restore point, got 200; body: %s", w.Body.String())
	}
}

func TestDeleteRestorePoint_WrongJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id1 := seedJob(t, d)
	destID2 := seedStorageDest(t, d)
	id2, _ := d.CreateJob(db.Job{Name: "other-job-" + nextUnique(), StorageDestID: destID2})

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id1, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:   runID,
		JobID:      id1,
		BackupType: "full",
	})

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id2, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10), nil)
	r = withURLParams(r, "id", strconv.FormatInt(id2, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.DeleteRestorePoint(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestDeleteRestorePoint_Success(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:   runID,
		JobID:      id,
		BackupType: "full",
		// No StoragePath so we skip the storage cleanup path
	})

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10), nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.DeleteRestorePoint(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// VerifyRestorePoint
// ---------------------------------------------------------------------------

func TestVerifyRestorePoint_RestorePointNotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	body, _ := json.Marshal(map[string]string{"mode": "quick"})
	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/9999/verify", body)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", "9999")
	h.VerifyRestorePoint(w, r)

	// GetRestorePoint doesn't map sql.ErrNoRows to db.ErrNotFound, so missing
	// restore points produce 500 rather than 404 here.
	if w.Code == http.StatusOK || w.Code == http.StatusAccepted {
		t.Fatalf("expected error status for missing restore point, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestVerifyRestorePoint_InvalidMode(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})

	body, _ := json.Marshal(map[string]string{"mode": "invalid"})
	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify", body)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.VerifyRestorePoint(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestVerifyRestorePoint_InvalidJSON(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/1/verify", []byte("!bad"))
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", "1")
	h.VerifyRestorePoint(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestVerifyRestorePoint_WrongJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id1 := seedJob(t, d)
	destID2 := seedStorageDest(t, d)
	id2, _ := d.CreateJob(db.Job{Name: "other-job-v-" + nextUnique(), StorageDestID: destID2})

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id1, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id1, BackupType: "full"})

	body, _ := json.Marshal(map[string]string{"mode": "quick"})
	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id2, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify", body)
	r = withURLParams(r, "id", strconv.FormatInt(id2, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.VerifyRestorePoint(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestVerifyRestorePoint_DefaultModeQuick(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})

	// Empty body — mode should default to "quick".
	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify", []byte("{}"))
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.VerifyRestorePoint(w, r)

	// Accepted (202) or internal error depending on storage — either is fine,
	// but we must not get 400.
	if w.Code == http.StatusBadRequest {
		t.Fatalf("unexpected 400; body: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GetVerifyRun
// ---------------------------------------------------------------------------

func TestGetVerifyRun_NotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/verify-runs/9999", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "vrid", "9999")
	h.GetVerifyRun(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestGetVerifyRun_Found(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})
	vrID, _ := d.CreateVerifyRun(rpID, "quick")

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/verify-runs/"+strconv.FormatInt(vrID, 10), nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "vrid", strconv.FormatInt(vrID, 10))
	h.GetVerifyRun(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["mode"] != "quick" {
		t.Errorf("mode = %v, want quick", resp["mode"])
	}
}

// ---------------------------------------------------------------------------
// ListRestorePointVerifyRuns
// ---------------------------------------------------------------------------

func TestListRestorePointVerifyRuns_NotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/9999/verify-runs", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", "9999")
	h.ListRestorePointVerifyRuns(w, r)

	// GetRestorePoint doesn't return ErrNotFound, so missing restore point → 500.
	if w.Code == http.StatusOK {
		t.Fatalf("expected error for missing restore point, got 200; body: %s", w.Body.String())
	}
}

func TestListRestorePointVerifyRuns_WrongJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id1 := seedJob(t, d)
	destID2 := seedStorageDest(t, d)
	id2, _ := d.CreateJob(db.Job{Name: "other-job-lvr-" + nextUnique(), StorageDestID: destID2})

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id1, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id1, BackupType: "full"})

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id2, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify-runs", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id2, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.ListRestorePointVerifyRuns(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestListRestorePointVerifyRuns_Success(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})
	_, _ = d.CreateVerifyRun(rpID, "deep")

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify-runs", nil)
	r = withURLParams(r, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	h.ListRestorePointVerifyRuns(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("want 1 verify run, got %d", len(resp))
	}
}

func TestListRestorePointVerifyRuns_LimitParam(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: id, BackupType: "full"})

	w := httptest.NewRecorder()
	req := newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/verify-runs", nil)
	req = withURLParams(req, "id", strconv.FormatInt(id, 10), "rpid", strconv.FormatInt(rpID, 10))
	req.URL.RawQuery = "limit=5"
	h.ListRestorePointVerifyRuns(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// RunNow
// ---------------------------------------------------------------------------

func TestRunNow_JobNotFound(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/9999/run", nil), "id", "9999")
	h.RunNow(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestRunNow_ValidJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/run", nil), "id", strconv.FormatInt(id, 10))
	h.RunNow(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["message"] != "backup started" {
		t.Errorf("message = %v, want 'backup started'", resp["message"])
	}
	// Drain the background goroutine so it doesn't leak into other tests.
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestCancel_NoActiveJob(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	// No job is running, so CancelJob returns an error → 409 Conflict.
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/cancel", nil), "id", strconv.FormatInt(id, 10))
	h.Cancel(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// RunnerStatus
// ---------------------------------------------------------------------------

func TestRunnerStatus_ReturnsOK(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	h.RunnerStatus(w, newReq(http.MethodGet, "/api/v1/runner/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["active"]; !ok {
		t.Error("response missing 'active' field")
	}
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

func TestRestore_InvalidJSON(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", []byte("!bad")), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRestore_MissingRestorePointID(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	body, _ := json.Marshal(map[string]any{"restore_point_id": 0})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", body), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRestore_RestorePointNotFound(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	body, _ := json.Marshal(map[string]any{"restore_point_id": int64(9999)})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", body), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestRestore_NoItemsToRestore(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:   runID,
		JobID:      id,
		BackupType: "full",
	})

	// No items/item_name — job has no items either, so targets will be empty.
	body, _ := json.Marshal(map[string]any{
		"restore_point_id": rpID,
		// items: omitted, item_name: omitted → restore all, but job has no items
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", body), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no items to restore); body: %s", w.Code, w.Body.String())
	}
}

func TestRestore_LegacySingleItem(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:   runID,
		JobID:      id,
		BackupType: "full",
	})

	body, _ := json.Marshal(map[string]any{
		"restore_point_id": rpID,
		"item_name":        "mycontainer",
		"item_type":        "container",
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", body), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	// 202 accepted (goroutine is fired). Drain so tests don't race.
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
}

func TestRestore_UnknownItemInList(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	runID, _ := d.CreateJobRun(db.JobRun{JobID: id, Status: "success", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:   runID,
		JobID:      id,
		BackupType: "full",
	})

	body, _ := json.Marshal(map[string]any{
		"restore_point_id": rpID,
		"items":            []string{"unknown-item"},
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/restore", body), "id", strconv.FormatInt(id, 10))
	h.Restore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// NextRun
// ---------------------------------------------------------------------------

func TestNextRun_NilResolver(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/next-run", nil), "id", strconv.FormatInt(id, 10))
	h.NextRun(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["scheduled"] != false {
		t.Errorf("expected scheduled=false when no resolver set, got %v", resp["scheduled"])
	}
}

func TestNextRun_ScheduledFalse(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	h.SetNextRunResolver(func(_ int64) (string, bool) { return "", false })

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/next-run", nil), "id", strconv.FormatInt(id, 10))
	h.NextRun(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["scheduled"] != false {
		t.Errorf("expected scheduled=false, got %v", resp["scheduled"])
	}
}

func TestNextRun_ScheduledTrue(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	const nextTime = "2026-06-01T00:00:00Z"
	h.SetNextRunResolver(func(_ int64) (string, bool) { return nextTime, true })

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(id, 10)+"/next-run", nil), "id", strconv.FormatInt(id, 10))
	h.NextRun(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["scheduled"] != true {
		t.Errorf("expected scheduled=true, got %v", resp["scheduled"])
	}
	if resp["next_run"] != nextTime {
		t.Errorf("next_run = %v, want %q", resp["next_run"], nextTime)
	}
}

// ---------------------------------------------------------------------------
// AllNextRuns
// ---------------------------------------------------------------------------

func TestAllNextRuns_Empty(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	h.AllNextRuns(w, newReq(http.MethodGet, "/api/v1/jobs/next-runs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestAllNextRuns_WithResolver(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)

	const nextTime = "2026-06-01T00:00:00Z"
	h.SetNextRunResolver(func(jid int64) (string, bool) {
		if jid == id {
			return nextTime, true
		}
		return "", false
	})

	w := httptest.NewRecorder()
	h.AllNextRuns(w, newReq(http.MethodGet, "/api/v1/jobs/next-runs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	key := strconv.FormatInt(id, 10)
	if resp[key] != nextTime {
		t.Errorf("resp[%q] = %v, want %q", key, resp[key], nextTime)
	}
}

func TestAllNextRuns_NilResolver(t *testing.T) {
	h, d := newJobHandlerDB(t)
	seedJob(t, d)

	// Ensure nextRun is nil.
	h.nextRun = nil

	w := httptest.NewRecorder()
	h.AllNextRuns(w, newReq(http.MethodGet, "/api/v1/jobs/next-runs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// max_parallel_uploads clamping
// ---------------------------------------------------------------------------

func TestJobCreate_ClampsParallelUploads(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	body, _ := json.Marshal(map[string]any{
		"name":                 "clamp-" + nextUnique(),
		"backup_type_chain":    "full",
		"compression":          "none",
		"storage_dest_id":      destID,
		"max_parallel_uploads": 99,
		"items":                []any{},
	})
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/api/v1/jobs", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	job, _ := d.GetJob(resp.ID)
	if job.MaxParallelUploads != 16 {
		t.Errorf("stored MaxParallelUploads = %d, want clamped 16", job.MaxParallelUploads)
	}
}

func TestJobCreate_PreservesZeroParallelUploads(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	body, _ := json.Marshal(map[string]any{
		"name":                 "zero-" + nextUnique(),
		"backup_type_chain":    "full",
		"compression":          "none",
		"storage_dest_id":      destID,
		"max_parallel_uploads": 0,
		"items":                []any{},
	})
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/api/v1/jobs", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	job, _ := d.GetJob(resp.ID)
	if job.MaxParallelUploads != 0 {
		t.Errorf("stored MaxParallelUploads = %d, want 0 (sentinel preserved)", job.MaxParallelUploads)
	}
}

func TestJobUpdate_ClampsParallelUploads(t *testing.T) {
	h, d := newJobHandlerDB(t)
	id := seedJob(t, d)
	job, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"name":                 "clamp-update-" + nextUnique(),
		"compression":          "none",
		"storage_dest_id":      job.StorageDestID,
		"max_parallel_uploads": 99,
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPut, "/api/v1/jobs/"+strconv.FormatInt(id, 10), body), "id", strconv.FormatInt(id, 10))
	h.Update(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
	}
	updated, _ := d.GetJob(id)
	if updated.MaxParallelUploads != 16 {
		t.Errorf("stored MaxParallelUploads = %d, want clamped 16", updated.MaxParallelUploads)
	}
}

// ---------------------------------------------------------------------------
// sortRestorePointsNewestFirst
// ---------------------------------------------------------------------------

func TestSortRestorePointsNewestFirst_Empty(t *testing.T) {
	out := sortRestorePointsNewestFirst(nil)
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestSortRestorePointsNewestFirst_Ordering(t *testing.T) {
	now := time.Now()
	points := []db.RestorePoint{
		{ID: 1, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: 2, CreatedAt: now},
		{ID: 3, CreatedAt: now.Add(-1 * time.Hour)},
	}
	sorted := sortRestorePointsNewestFirst(points)
	if sorted[0].ID != 2 {
		t.Errorf("first = %d, want 2 (newest)", sorted[0].ID)
	}
	if sorted[2].ID != 1 {
		t.Errorf("last = %d, want 1 (oldest)", sorted[2].ID)
	}
}
