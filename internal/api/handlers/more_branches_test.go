package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ---------------------------------------------------------------------------
// storage.go — DownloadFile success path, DependentJobs/ListFiles errors,
// Update error branches.
// ---------------------------------------------------------------------------

// TestDownloadFile_Success drives the io.Copy success branch of DownloadFile
// by writing a real file inside the local storage root.
func TestDownloadFile_Success(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	payload := []byte("dummy backup file content")
	filePath := filepath.Join(storageDir, "test.bin")
	if err := os.WriteFile(filePath, payload, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/storage/x/files?path=test.bin", nil)
	req = withURLParam(req, "id", idStr)
	w := httptest.NewRecorder()
	h.DownloadFile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); string(got) != string(payload) {
		t.Errorf("body = %q, want %q", got, payload)
	}
	if cl := w.Header().Get("Content-Length"); cl == "" {
		t.Error("expected Content-Length header")
	}
}

// TestDependentJobs_InvalidID parseID failure.
func TestDependentJobs_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/bad/jobs", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestDependentJobs_DBClosed forces ListJobsByStorageDestID to fail.
func TestDependentJobs_DBClosed(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	_ = h.db.Close()

	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/x/jobs",
		strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestListFiles_InvalidID parseID failure.
func TestListFiles_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.ListFiles(w, reqWithID(http.MethodGet, "/api/v1/storage/bad/list", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestListFiles_BadAdapterConfig forces the adapter construction to fail by
// poking an invalid blob into the DB row.
func TestListFiles_BadAdapterConfig(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	if _, err := h.db.Exec(
		`UPDATE storage_destinations SET config = ? WHERE id = ?`,
		"not-valid-json", destID,
	); err != nil {
		t.Fatalf("update config: %v", err)
	}

	w := httptest.NewRecorder()
	h.ListFiles(w, reqWithID(http.MethodGet, "/api/v1/storage/x/list",
		strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageList_DBClosed forces the List handler's DB-error branch.
func TestStorageList_DBClosed(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)
	_ = h.db.Close()

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageUpdate_DedupChangeRejected covers the dedup_enabled-immutability
// branch in Update.
func TestStorageUpdate_DedupChangeRejected(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"dedup_enabled":true}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageUpdate_BackupDatabaseFlag drives the BackupDatabaseEnabled
// branch (currently uncovered) without touching name/type/config.
func TestStorageUpdate_BackupDatabaseFlag(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"backup_database_enabled":true}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageUpdate_InvalidConfigRejected exercises the storage.NewAdapter
// validation branch on Update (post-merge) by sending a bogus type-mismatched
// config blob.
func TestStorageUpdate_InvalidConfigRejected(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	// preserveRedactedSecrets falls back to passthrough on non-JSON
	// incoming, so a string that's clearly not JSON triggers the validator
	// branch.
	body := []byte(`{"config":"this is not json"}`)
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/api/v1/storage/"+idStr, idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// jobs.go — GetVerifyRun invalid id, Cancel error, AllNextRuns DB-closed
// ---------------------------------------------------------------------------

// TestGetVerifyRun_InvalidJobID parseID failure for the first id.
func TestGetVerifyRun_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/bad/verify-runs/1", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.GetVerifyRun(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestGetVerifyRun_InvalidVRID parseID failure for the second id.
func TestGetVerifyRun_InvalidVRID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/verify-runs/bad", nil)
	req = withURLParams(req, "id", "1", "vrid", "bad")
	w := httptest.NewRecorder()
	h.GetVerifyRun(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestCancel_InvalidID parseID failure.
func TestCancel_InvalidID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/bad/cancel", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Cancel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestAllNextRuns_DBClosed forces the ListJobs error branch.
func TestAllNextRuns_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	_ = d.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/next-runs", nil)
	w := httptest.NewRecorder()
	h.AllNextRuns(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestJobGetHistory_DBClosed forces GetJobRuns to fail.
func TestJobGetHistory_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	_ = d.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/history", nil)
	req = withURLParam(req, "id", strconv.FormatInt(jobID, 10))
	w := httptest.NewRecorder()
	h.GetHistory(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestVerifyRestorePoint_InvalidJobID parseID failure for first id.
func TestVerifyRestorePoint_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/bad/restore-points/1/verify", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.VerifyRestorePoint(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestListRestorePointVerifyRuns_InvalidJobID parseID failure.
func TestListRestorePointVerifyRuns_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/bad/restore-points/1/verify-runs", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.ListRestorePointVerifyRuns(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestore_InvalidJobID forces the parseID branch.
func TestRestore_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	body := strings.NewReader(`{"restore_point_id":1,"items":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/bad/restore", body)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Restore(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// replication.go — ListReplicatedJobs DB closed, Update validation paths
// ---------------------------------------------------------------------------

// TestReplicationListReplicatedJobs_DBClosed forces the
// ListReplicatedJobs DB error branch.
func TestReplicationListReplicatedJobs_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "x", Type: "remote_vault",
		URL: "http://x:24085", Config: "{}", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	_ = d.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/replication/1/jobs", nil)
	req = withURLParam(req, "id", strconv.FormatInt(srcID, 10))
	w := httptest.NewRecorder()
	h.ListReplicatedJobs(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationUpdate_InvalidID parseID failure.
func TestReplicationUpdate_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/replication/bad",
		strings.NewReader(`{"name":"x","url":"http://x:24085"}`))
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationUpdate_InvalidJSON parseID succeeds; decode fails.
func TestReplicationUpdate_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/replication/1",
		strings.NewReader("not-json"))
	req = withURLParam(req, "id", "1")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationUpdate_InvalidURL exercises NormalizeBaseURL failure path.
func TestReplicationUpdate_InvalidURL(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	body := strings.NewReader(`{"name":"src","url":"::not-a-url::"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/replication/1", body)
	req = withURLParam(req, "id", "1")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// settings.go — GenerateAPIKey happy path (covers sealed + hash writes),
// SetEncryption successful round-trip, GetStagingInfo trivial branch coverage.
// ---------------------------------------------------------------------------

// TestGenerateAPIKey_HappyPath_Then_Revoke runs the entire lifecycle so the
// final hash-write success branch is taken.
func TestGenerateAPIKey_HappyPath_Then_Revoke(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("generate: status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	// Now revoke.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/settings/api-key", nil)
	w = httptest.NewRecorder()
	h.RevokeAPIKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// browse.go — discoverRoots branch where the lister is set and second call
// succeeds (we already covered set + error). Here we drive a happy path with
// a temp-dir-backed allowed-root + ZFS include.
// ---------------------------------------------------------------------------

// TestBrowse_ListWithFilesParam drives the listEntries includeFiles=true
// branch with a JSON-shaped response.
func TestBrowse_ListWithFilesParam_RespectsHiddenSkip(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	// Create some dirs and a hidden entry.
	if err := os.Mkdir(filepath.Join(base, "vis"), 0o755); err != nil {
		t.Fatalf("mkdir vis: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatalf("hidden: %v", err)
	}

	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{filepath.Dir(base)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path="+base+"&files=true", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries, ok := resp["entries"].([]any)
	if !ok {
		t.Fatalf("entries not slice")
	}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		name, _ := m["name"].(string)
		if strings.HasPrefix(name, ".") {
			t.Errorf("hidden entry leaked: %v", name)
		}
	}
}

// TestBrowse_ListEntries_BadPath returns 400 (listEntries fails on ReadDir)
// after path passes safepath normalization. Achieved by giving the browse
// handler an extra allowed root that's a file, not a dir.
func TestBrowse_ListEntries_BadPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	// Path under base that doesn't exist.
	target := filepath.Join(base, "missing-subdir")

	h := NewBrowseHandler()
	h.extraAllowedRoots = []string{base}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?path="+target, nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
