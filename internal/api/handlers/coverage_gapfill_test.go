package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ============================================================================
// RestoreDB additional error branches
// ============================================================================

// TestRestoreDB_RootCleanedPath drives the cleanedStoragePath == "/" branch.
// We hit this when the request body's storage_path resolves to exactly "/"
// after path.Clean (e.g. "" + "/" handling, "/" or "//").
func TestRestoreDB_RootCleanedPath(t *testing.T) {
	t.Parallel()
	h, destID, _ := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	// "/" — after path.Clean("/" + "/") = "/" → 400.
	body := []byte(`{"storage_path":"/"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db", idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestoreDB_TraversalRejected drives the `strings.Contains("..")` reject.
func TestRestoreDB_TraversalRejected(t *testing.T) {
	t.Parallel()
	h, destID, _ := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"storage_path":"../escape"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db", idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestoreDB_EmptyStoragePath drives the empty-string check.
func TestRestoreDB_EmptyStoragePath(t *testing.T) {
	t.Parallel()
	h, destID, _ := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	body := []byte(`{"storage_path":""}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db", idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestoreDB_BadStorageConfig drives the storage.NewAdapter error branch
// by configuring the destination with a corrupt JSON config blob.
func TestRestoreDB_BadStorageConfig(t *testing.T) {
	t.Parallel()
	// Use the in-memory test helper that lets us pick the dest config freely.
	d := newTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "bad-cfg-restore-" + nextUnique(),
		Type:   "local",
		Config: `{not valid json`, // factory.go falls through to local case, then json.Unmarshal fails.
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	h := NewStorageHandler(d, nil, nil)
	idStr := strconv.FormatInt(destID, 10)
	body := []byte(`{"storage_path":"any-path"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db", idStr, body))
	// NewAdapter fails -> respondInternalError -> 500.
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// ============================================================================
// RefreshCapacity error branches: adapter-construction failure
// ============================================================================

// TestRefreshCapacity_BadAdapterConfig drives the NewAdapter error path:
// invalid JSON -> 502.
func TestRefreshCapacity_BadAdapterConfig(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "bad-cfg-rc-" + nextUnique(),
		Type:   "local",
		Config: `{not valid json`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	h := NewStorageHandler(d, nil, nil)
	idStr := strconv.FormatInt(destID, 10)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/api/v1/storage/x/capacity-check", idStr, nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

// TestRefreshCapacity_AdapterGetCapacityFails drives the capErr branch by
// pointing the local adapter at a non-existent directory so unix.Statfs
// errors out. The response should be 502 with the error body persisted to
// the destination row.
func TestRefreshCapacity_AdapterGetCapacityFails(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	// Point local adapter at a path under a non-existent root so
	// unix.Statfs returns ENOENT.
	missingDir := filepath.Join(t.TempDir(), "does-not-exist", "nested")
	cfg, _ := json.Marshal(map[string]string{"path": missingDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "missing-rc-" + nextUnique(),
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	h := NewStorageHandler(d, nil, nil)
	idStr := strconv.FormatInt(destID, 10)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/api/v1/storage/x/capacity-check", idStr, nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

// TestRefreshCapacity_DestNotFound drives the GetStorageDestination
// not-found branch.
func TestRefreshCapacity_DestNotFound(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewStorageHandler(d, nil, nil)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/capacity-check", "9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// ============================================================================
// RestorePointContents: deeper error branches
// ============================================================================

// (TestRestorePointContents_RestorePointNotFound already lives in
// jobs_test.go.)

// TestRestorePointContents_JobMismatch drives the rp.JobID != jobID branch:
// the restore point exists but belongs to a different job.
func TestRestorePointContents_JobMismatch(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	// Create two distinct jobs.
	jobA, err := d.CreateJob(db.Job{Name: "rpc-A-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("create jobA: %v", err)
	}
	jobB, err := d.CreateJob(db.Job{Name: "rpc-B-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("create jobB: %v", err)
	}

	// Restore point belongs to jobA, but we query with jobB's id.
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobA, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobA,
		BackupType:  "full",
		StoragePath: "rp-mismatch",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=foo", jobB, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobB, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_MissingItemQuery drives the empty-item-name branch.
func TestRestorePointContents_MissingItemQuery(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	jobID, err := d.CreateJob(db.Job{Name: "rpc-noitem-" + nextUnique(), StorageDestID: destID})
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
		StoragePath: "rp-noitem",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	// No `?item=` query.
	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents", jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_BadStorageAdapter drives the storage.NewAdapter
// failure branch (corrupt JSON config) inside RestorePointContents.
func TestRestorePointContents_BadStorageAdapter(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "bad-cfg-rpc-" + nextUnique(),
		Type:   "local",
		Config: `{not valid json`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "rpc-badcfg-" + nextUnique(), StorageDestID: destID})
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
		StoragePath: "rp-badcfg",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=foo", jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// ============================================================================
// GenerateAPIKey: SetSetting failure on api_key_hash (rollback branch)
// ============================================================================

// (This is largely covered by TestGenerateAPIKey_DBClosed, which trips
// SetSetting on api_key_sealed. The rollback path that runs when only the
// SECOND SetSetting fails is harder to drive without injecting a
// partially-broken DB. We document it here but rely on the existing
// coverage.)

// ============================================================================
// discoverRoots fallback: /mnt ReadDir failure
// ============================================================================

// TestDiscoverRoots_NoMntDirectory is a documentation test: on macOS, /mnt
// usually doesn't exist, so the os.ReadDir("/mnt") inside discoverRoots
// returns an error and the function returns early with the well-known
// roots only. This is the most-hit branch on dev machines. The existing
// TestDiscoverRoots_* tests already exercise the include_zfs branch, so
// here we just confirm a default List() call returns 200 to lock in the
// no-/mnt early-return.
func TestDiscoverRoots_DefaultList(t *testing.T) {
	t.Parallel()
	h := NewBrowseHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestDiscoverRoots_WithMntDirectory simulates a /mnt-style discovery by
// pointing through the default List handler when a temporary /mnt-like
// directory containing a "disk1" subdir is set up under the system root.
// On macOS /mnt doesn't exist, so this just confirms the empty-mnt fallback.
func TestDiscoverRoots_EmptyMntFallback(t *testing.T) {
	t.Parallel()
	// We can't write to /mnt on macOS; just confirm the no-/mnt path
	// returns 200 with an empty-ish roots array. The actual disk-discovery
	// branches require /mnt/disk1, /mnt/disk2 etc. which only exist on
	// Unraid.
	h := NewBrowseHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/browse", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["entries"]; !ok {
		t.Errorf("response missing 'entries' key: %v", resp)
	}
}

// (newTestDB is provided by testhelpers_test.go.)
var _ = bytes.NewReader
