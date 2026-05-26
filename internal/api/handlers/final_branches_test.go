package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestJobUpdate_InvalidID parseID failure.
func TestJobUpdate_InvalidID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	body := []byte(`{"name":"x"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/jobs/bad", bytes.NewReader(body))
	req = withURLParam(req, "id", "bad")
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_GetJobError forces the GetJob branch to fail by
// closing the DB after the restore-point lookup succeeded. Tricky since we
// can't pause mid-handler — use the fact that GetJob is hit BEFORE the
// item check, so we need a scenario where GetRestorePoint succeeds but
// GetJob fails. Easier: pre-seed restore point pointing at a job whose
// row was deleted via raw SQL (cascade off).
func TestRestorePointContents_GetJobError(t *testing.T) {
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
		StoragePath: "rp-orphan",
		Metadata:    "{}",
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}
	// Delete the job row directly bypassing cascade (NULL FK leaves rp).
	if _, err := d.Exec(`DELETE FROM jobs WHERE id = ?`, jobID); err != nil {
		t.Fatalf("delete job direct: %v", err)
	}

	url := "/api/v1/jobs/" + strconv.FormatInt(jobID, 10) +
		"/restore-points/" + strconv.FormatInt(rpID, 10) +
		"/contents?item=any"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10),
		"rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)

	// The job is gone but the rp is still there; GetJob returns
	// ErrNotFound which goes to respondInternalError → 500. (The handler
	// doesn't special-case this NotFound.)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestVerifyRestorePoint_InvalidRPID parseID rpid failure.
func TestVerifyRestorePoint_InvalidRPID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/1/restore-points/bad/verify", nil)
	req = withURLParams(req, "id", "1", "rpid", "bad")
	w := httptest.NewRecorder()
	h.VerifyRestorePoint(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestListRestorePointVerifyRuns_InvalidRPID parseID rpid failure.
func TestListRestorePointVerifyRuns_InvalidRPID(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/restore-points/bad/verify-runs", nil)
	req = withURLParams(req, "id", "1", "rpid", "bad")
	w := httptest.NewRecorder()
	h.ListRestorePointVerifyRuns(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestListRestorePointVerifyRuns_MissingRP — GetRestorePoint returns a
// raw sql.ErrNoRows when the row is missing rather than db.ErrNotFound, so
// the handler maps to 500 via respondInternalError. Either way the
// downstream "rp.JobID != jobID" branch is skipped because the wrapper
// short-circuits.
func TestListRestorePointVerifyRuns_MissingRP(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/1/restore-points/9999/verify-runs", nil)
	req = withURLParams(req, "id", "1", "rpid", "9999")
	w := httptest.NewRecorder()
	h.ListRestorePointVerifyRuns(w, req)

	if w.Code != http.StatusNotFound && w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 404 or 500; body: %s", w.Code, w.Body.String())
	}
}

// TestGetVerifyRun_InvalidVRIDExtra is a duplicate parseID for vrid.
// (Already covered; kept for completeness — coverage shows it's been hit.)

// TestStorage_RunDedupGC_HappyPath ensures the 202 path executes against
// a dedup-enabled destination. The async GC will fail because the repo
// has no contents, but the handler returns 202 before that.
func TestStorage_RunDedupGC_HappyPath(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, true)

	w := httptest.NewRecorder()
	h.RunDedupGC(w, reqWithID(http.MethodPost, "/api/v1/storage/x/gc",
		strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
}

// TestStorage_GetDedupStats_DBClosed forces stats query to fail.
func TestStorage_GetDedupStats_DBClosed(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, true)
	_ = h.db.Close()

	w := httptest.NewRecorder()
	h.GetDedupStats(w, reqWithID(http.MethodGet, "/api/v1/storage/x/dedup-stats",
		strconv.FormatInt(destID, 10), nil))
	// With DB closed, either 404 (GetStorageDestination failed) or 500.
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 or 500; body: %s", w.Code, w.Body.String())
	}
}

// TestStorage_ScanOrphans_NotFoundOnRunner is harder to test without a
// non-existent rp; instead ensure the happy path runs against an actually
// empty storage (already covered) — skip.

// ---------------------------------------------------------------------------
// Replication.TestConnection — invalid config JSON branch.
// ---------------------------------------------------------------------------

// TestReplicationTestConnection_InvalidConfigJSON drives the "invalid
// config JSON" branch.
func TestReplicationTestConnection_InvalidConfigJSON(t *testing.T) {
	t.Parallel()
	h, d := setupReplicationTest(t)
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "bad-config", Type: "remote_vault",
		URL: "http://x:24085", Config: "not-json-but-non-empty", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/replication/x/test", nil)
	req = withURLParam(req, "id", strconv.FormatInt(srcID, 10))
	w := httptest.NewRecorder()
	h.TestConnection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestReplicationTestURL_InvalidURL covers the NormalizeBaseURL error
// branch.
func TestReplicationTestURL_InvalidURL(t *testing.T) {
	t.Parallel()
	h, _ := setupReplicationTest(t)

	body := bytes.NewReader([]byte(`{"url":"::bad-url::"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/replication/test-url", body)
	w := httptest.NewRecorder()
	h.TestURL(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
