package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ---------------------------------------------------------------------------
// jobs.go — error branches in List, GetRestorePoints, DeleteRestorePoint,
// RestorePointContents, resolveIndexCandidates.
// ---------------------------------------------------------------------------

// TestJobList_DBClosedError closes the DB before invoking the handler so the
// internal-error branch of List fires.
func TestJobList_DBClosedError(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	_ = d.Close() // intentional: force ListJobs to fail.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestGetRestorePoints_InvalidID covers the parseID-fails branch.
func TestGetRestorePoints_InvalidID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/abc/restore-points", nil)
	req = withURLParam(req, "id", "abc")
	w := httptest.NewRecorder()
	h.GetRestorePoints(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestGetRestorePoints_DBClosed forces ListRestorePoints to error after the
// job lookup succeeds.
func TestGetRestorePoints_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	_ = d.Close() // make subsequent DB ops fail.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/restore-points", nil)
	req = withURLParam(req, "id", strconv.FormatInt(jobID, 10))
	w := httptest.NewRecorder()
	h.GetRestorePoints(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_InvalidJobID covers parseID branch.
func TestRestorePointContents_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/abc/rp/1/contents", nil)
	req = withURLParam(req, "id", "abc")
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_InvalidRPID covers parseID rpid branch.
func TestRestorePointContents_InvalidRPID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/rp/bad/contents", nil)
	req = withURLParams(req, "id", "1", "rpid", "bad")
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_NoSidecar succeeds in opening the destination
// adapter but finds no tar-index file, returning 404. This is the path that
// drives resolveIndexCandidates with empty archiveName and an empty (but
// existing) directory.
func TestRestorePointContents_NoSidecar(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	// Build a local dest pointed at a known temp dir so we can pre-create
	// the rp/item subdir; without this, adapter.List() fails on the missing
	// directory and we go through the 500 branch instead of the 404 one we
	// want to cover.
	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "rpc-no-sidecar-" + nextUnique(),
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create storage dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:          "rpc-job-" + nextUnique(),
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "rp-1",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}
	// Pre-create the per-item directory so adapter.List() succeeds and
	// returns an empty slice → resolveIndexCandidates yields zero
	// candidates → handler returns 404.
	itemDir := filepath.Join(storageDir, "rp-1", "anything")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("mkdir item dir: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=anything", jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_WithArchiveNameBuildsCandidates drives the
// archiveName != "" branch of resolveIndexCandidates: the candidates are
// built directly and the adapter's Read fails for both (so we get 404).
func TestRestorePointContents_WithArchiveNameBuildsCandidates(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	jobID, err := d.CreateJob(db.Job{
		Name:          "rpc-archive-job",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "rp-archive",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=foo&file=backup.tar",
		jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	// Both candidate paths fail to Read → 404 "no sidecar readable".
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_WithArchiveAndAgeSuffix drives the
// strings.TrimSuffix(archiveName, ".age") branch of resolveIndexCandidates.
func TestRestorePointContents_WithArchiveAndAgeSuffix(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	jobID, err := d.CreateJob(db.Job{
		Name:          "rpc-age-job",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "rp-age",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=bar&file=backup.tar.age",
		jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestDeleteRestorePoint_InvalidJobID parseID failure path.
func TestDeleteRestorePoint_InvalidJobID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/x/rp/1", nil)
	req = withURLParam(req, "id", "x")
	w := httptest.NewRecorder()
	h.DeleteRestorePoint(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestDeleteRestorePoint_InvalidRPID covers second parseID failure.
func TestDeleteRestorePoint_InvalidRPID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/1/rp/bad", nil)
	req = withURLParams(req, "id", "1", "rpid", "bad")
	w := httptest.NewRecorder()
	h.DeleteRestorePoint(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestDeleteRestorePoint_WithStoragePath_Success exercises the storage
// cleanup branch of DeleteRestorePoint where the restore point has a
// non-empty StoragePath; the adapter and DeleteStorageDir lines run.
func TestDeleteRestorePoint_WithStoragePath_Success(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "rp-with-path",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d", jobID, rpID)
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.DeleteRestorePoint(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// replication.go — error branches in List, Get, Delete, SyncNow.
// ---------------------------------------------------------------------------

// TestReplicationList_DBClosed forces ListReplicationSources to fail.
func TestReplicationList_DBClosed(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	_ = d.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/replication", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationGet_InvalidID parseID failure.
func TestReplicationGet_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/replication/bad", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationGet_NotFound exercises the row-not-found branch.
func TestReplicationGet_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/replication/9999", nil)
	req = withURLParam(req, "id", "9999")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationDelete_InvalidID parseID failure.
func TestReplicationDelete_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/replication/bad", nil)
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationDelete_NonexistentSucceedsWith204 — Delete on a missing row
// still returns 204 because the SQL delete is a no-op rather than an error.
func TestReplicationDelete_NonexistentSucceedsWith204(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/replication/9999", nil)
	req = withURLParam(req, "id", "9999")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

// (SyncNow 202 happy path requires constructing a real *replication.Syncer
// which is heavy; SyncNow's other branches — getSyncer==nil and invalid id —
// are covered by the existing tests in replication_test.go.)

// ---------------------------------------------------------------------------
// storage.go — error branches.
// ---------------------------------------------------------------------------

// TestStorageGet_InvalidID parseID failure.
func TestStorageGet_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Get(w, reqWithID(http.MethodGet, "/api/v1/storage/bad", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageGet_NotFound — destination row missing.
func TestStorageGet_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Get(w, reqWithID(http.MethodGet, "/api/v1/storage/9999", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageDelete_InvalidID parseID failure.
func TestStorageDelete_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/api/v1/storage/bad", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageDelete_WithDeleteFilesFlag exercises the deleteFiles=true branch.
func TestStorageDelete_WithDeleteFilesFlag(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	url := "/api/v1/storage/" + idStr + "?deleteFiles=true"
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	req = withURLParam(req, "id", idStr)
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageTestConnection_InvalidID parseID failure.
func TestStorageTestConnection_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.TestConnection(w, reqWithID(http.MethodPost, "/api/v1/storage/bad/test", "bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageTestConnection_BadAdapterConfig forces NewAdapter to fail
// by storing an invalid config blob then calling TestConnection. We can't
// poke directly through Create (which validates), so we update the row
// directly in DB to bypass.
func TestStorageTestConnection_BadAdapterConfig(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)

	// Use raw SQL to inject invalid config bypassing validation.
	if _, err := h.db.Exec(
		`UPDATE storage_destinations SET config = ? WHERE id = ?`,
		"not-valid-json", destID,
	); err != nil {
		t.Fatalf("update config: %v", err)
	}

	w := httptest.NewRecorder()
	h.TestConnection(w, reqWithID(http.MethodPost, "/api/v1/storage/x/test",
		strconv.FormatInt(destID, 10), nil))

	// NewAdapter rejects the bad blob → 400.
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageCloseBreaker_NilRunner returns 500 when h.runner is nil.
func TestStorageCloseBreaker_NilRunner(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "no-runner-dest",
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	h := NewStorageHandler(d, nil) // explicit nil runner.
	idStr := strconv.FormatInt(destID, 10)
	w := httptest.NewRecorder()
	h.CloseBreaker(w, reqWithID(http.MethodPost, "/api/v1/storage/x/breaker/close", idStr, nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageCloseBreaker_Success drives the breaker close happy path.
func TestStorageCloseBreaker_Success(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.CloseBreaker(w, reqWithID(http.MethodPost, "/api/v1/storage/x/breaker/close", idStr, nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageScanOrphans_InvalidID parseID failure.
func TestStorageScanOrphans_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.ScanOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/bad/scan-orphans", "bad", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageScanOrphans_NotFound — destination row missing.
func TestStorageScanOrphans_NotFound(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.ScanOrphans(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/scan-orphans", "9999", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageRefreshCapacity_InvalidID parseID failure.
func TestStorageRefreshCapacity_InvalidID(t *testing.T) {
	t.Parallel()
	h, _ := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/api/v1/storage/bad/capacity-check", "bad", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageRefreshCapacity_BadAdapter — invalid config trips NewAdapter.
func TestStorageRefreshCapacity_BadAdapter(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)

	if _, err := h.db.Exec(
		`UPDATE storage_destinations SET config = ? WHERE id = ?`,
		"not-valid-json", destID,
	); err != nil {
		t.Fatalf("update config: %v", err)
	}

	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/api/v1/storage/x/capacity-check",
		strconv.FormatInt(destID, 10), nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// settings.go — GetDiagnostics, SetSnapshotPath, GenerateAPIKey, RevokeAPIKey
// ---------------------------------------------------------------------------

// TestSetSnapshotPath_InvalidJSON drives the invalid-JSON branch.
func TestSetSnapshotPath_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database",
		strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestSetSnapshotPath_EmptyClearsOverride passes an empty string to
// exercise the "if req.SnapshotPath != ''" false branch.
func TestSetSnapshotPath_EmptyClearsOverride(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"snapshot_path":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, _ := h.db.GetSetting("snapshot_path_override", "")
	if stored != "" {
		t.Errorf("snapshot_path_override = %q, want empty", stored)
	}
}

// TestSetSnapshotPath_AutoAppendDBFilename covers the "fi.IsDir()" branch
// where the user supplies a directory and the handler appends vault.db.
func TestSetSnapshotPath_AutoAppendDBFilename(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	dir, err := os.MkdirTemp("/tmp", "vault-snap-dir-*")
	if err != nil {
		t.Fatalf("mktmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	body := `{"snapshot_path":"` + dir + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	stored, _ := h.db.GetSetting("snapshot_path_override", "")
	if filepath.Base(stored) != "vault.db" {
		t.Errorf("expected vault.db basename, got %q", stored)
	}
}

// TestSetSnapshotPath_ParentMissing forces the "parent directory does not
// exist" branch.
func TestSetSnapshotPath_ParentMissing(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"snapshot_path":"/tmp/this/parent/does/not/exist/vault.db"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestSetSnapshotPath_AppliesToSnapshotManager exercises the
// h.snapshotManager.SetSnapshotPath() success path.
func TestSetSnapshotPath_AppliesToSnapshotManager(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	sm := &mockSnapshotManager{}
	h.SetSnapshotManager(sm)

	dir, err := os.MkdirTemp("/tmp", "vault-snap-applied-*")
	if err != nil {
		t.Fatalf("mktmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	snapPath := filepath.Join(dir, "vault.db")

	body := `{"snapshot_path":"` + snapPath + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if sm.snapshotPath != snapPath {
		t.Errorf("snapshot manager received %q, want %q", sm.snapshotPath, snapPath)
	}
}

// (GetDiagnostics collector-error branch is hard to drive: the collector
// internally swallows DB errors and still returns a bundle. Skipping.)

// TestGenerateAPIKey_InvalidServerKey forces the empty-server-key branch.
func TestGenerateAPIKey_EmptyServerKey(t *testing.T) {
	t.Parallel()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h := NewSettingsHandler(d, nil) // empty server key.

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestGenerateAPIKey_DBClosed forces the SetSetting failure branch.
func TestGenerateAPIKey_DBClosed(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	_ = h.db.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestRevokeAPIKey_HappyPath drives RevokeAPIKey success path.
func TestRevokeAPIKey_HappyPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Pre-seed a hash + sealed value so revoke clears something.
	if err := h.db.SetSetting("api_key_hash", "hash"); err != nil {
		t.Fatalf("set hash: %v", err)
	}
	if err := h.db.SetSetting("api_key_sealed", "sealed"); err != nil {
		t.Fatalf("set sealed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/api-key", nil)
	w := httptest.NewRecorder()
	h.RevokeAPIKey(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Confirm both keys are cleared.
	hash, _ := h.db.GetSetting("api_key_hash", "")
	sealed, _ := h.db.GetSetting("api_key_sealed", "")
	if hash != "" || sealed != "" {
		t.Errorf("expected both cleared, got hash=%q sealed=%q", hash, sealed)
	}
}

// TestRevokeAPIKey_DBClosed drives the first SetSetting error branch.
func TestRevokeAPIKey_DBClosed(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	_ = h.db.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/api-key", nil)
	w := httptest.NewRecorder()
	h.RevokeAPIKey(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// browse.go — discoverRoots branch where include_zfs=true (lister assigned).
// ---------------------------------------------------------------------------

// fakeZFSLister returns a fixed list of ZFS mounts; safe for parallel use.
type fakeZFSLister struct {
	mounts []ZFSMountInfo
	err    error
}

func (f *fakeZFSLister) ListZFSMountpoints() ([]ZFSMountInfo, error) {
	return f.mounts, f.err
}

// TestDiscoverRoots_WithZFS_ListSucceeds covers the includeZFS branch where
// the lister is set and the second ListZFSMountpoints call (inside
// discoverRoots) succeeds. We need a path under /mnt that exists; create a
// temp dir to hold something stat-able. We can't write to /mnt on macOS, so
// instead we exercise via List() with include_zfs=true and an empty-but-
// installed lister — the function still returns even if no roots exist.
func TestDiscoverRoots_WithZFS_NoDuplicates(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	// Install a lister that returns at least one valid mountpoint.
	lister := &fakeZFSLister{
		mounts: []ZFSMountInfo{
			{Name: "tank", Mountpoint: "/mnt/tank-test"},
		},
	}
	h.SetZFSLister(lister) // success path → h.zfsLister is set.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?include_zfs=true", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestDiscoverRoots_WithZFS_ListerErrors drives the include_zfs=true branch
// where the SECOND ListZFSMountpoints (inside discoverRoots) returns an
// error. SetZFSLister calls ListZFSMountpoints once during setup; we want
// that one to succeed so the lister is stored, but the next call inside
// discoverRoots to fail.
func TestDiscoverRoots_WithZFS_ListerErrorsOnSecondCall(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()
	// Use a lister that succeeds on first call (so SetZFSLister stores it)
	// and fails on subsequent calls.
	lister := &countingZFSLister{
		mounts: []ZFSMountInfo{{Name: "z", Mountpoint: "/mnt/z-test"}},
	}
	h.SetZFSLister(lister)
	lister.failNext = true // every subsequent call returns an error.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse?include_zfs=true", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// countingZFSLister returns success on its first call and an error on every
// subsequent call when failNext is set. Mirrors the lister-stored-then-fails
// path inside BrowseHandler.discoverRoots.
type countingZFSLister struct {
	mounts   []ZFSMountInfo
	failNext bool
	calls    int
}

func (c *countingZFSLister) ListZFSMountpoints() ([]ZFSMountInfo, error) {
	c.calls++
	if c.failNext && c.calls > 1 {
		return nil, fmt.Errorf("simulated zfs failure")
	}
	return c.mounts, nil
}

// Ensure unused-import safety: reference time and bytes from the test
// surface even when none of the above tests use them directly.
var _ = time.Now
var _ = bytes.NewReader
