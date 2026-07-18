package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// writeContentsSidecar writes a tar-index sidecar for one restore point's item
// directory under the local storage root.
func writeContentsSidecar(t *testing.T, storageRoot, storagePath, item string, files []engine.TarIndexEntry) {
	t.Helper()
	itemDir := filepath.Join(storageRoot, storagePath, item)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", itemDir, err)
	}
	idx := engine.TarIndex{Version: 1, Archive: "backup.tar", Files: files}
	b, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal idx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "backup.tar"+engine.IndexSuffix), b, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

func chainContentsFixture(t *testing.T) (h *JobHandler, storageRoot string, jobID, fullID, incID int64) {
	t.Helper()
	var d *db.DB
	h, d = newJobHandlerDB(t)

	storageRoot = t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "rpc-chain-" + nextUnique(), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err = d.CreateJob(db.Job{
		Name: "rpc-chain-job-" + nextUnique(), StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	fullID, err = d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp-chain-full", Metadata: "{}",
	})
	if err != nil {
		t.Fatalf("create full rp: %v", err)
	}
	incID, err = d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "incremental",
		StoragePath: "rp-chain-inc", Metadata: "{}",
		ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("create inc rp: %v", err)
	}
	return h, storageRoot, jobID, fullID, incID
}

func getContents(t *testing.T, h *JobHandler, jobID, rpID int64) *httptest.ResponseRecorder {
	t.Helper()
	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=fooitem", jobID, rpID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)
	return w
}

// TestRestorePointContents_MergesIncrementalChain verifies that browsing an
// incremental restore point returns the union of the chain's indexes, with
// the increment's entries overriding the base full's by path.
func TestRestorePointContents_MergesIncrementalChain(t *testing.T) {
	t.Parallel()
	h, storageRoot, jobID, _, incID := chainContentsFixture(t)

	writeContentsSidecar(t, storageRoot, "rp-chain-full", "fooitem", []engine.TarIndexEntry{
		{Path: "a.txt", Size: 1, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
		{Path: "b.txt", Size: 2, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
	})
	writeContentsSidecar(t, storageRoot, "rp-chain-inc", "fooitem", []engine.TarIndexEntry{
		{Path: "b.txt", Size: 20, Mode: "0644", ModTime: "2026-01-02T00:00:00Z"},
		{Path: "c.txt", Size: 3, Mode: "0644", ModTime: "2026-01-02T00:00:00Z"},
	})

	w := getContents(t, h, jobID, incID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got engine.TarIndex
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	wantPaths := []string{"a.txt", "b.txt", "c.txt"}
	if len(got.Files) != len(wantPaths) {
		t.Fatalf("files = %+v, want 3 merged entries", got.Files)
	}
	for i, p := range wantPaths {
		if got.Files[i].Path != p {
			t.Errorf("files[%d].Path = %q, want %q (sorted merge order)", i, got.Files[i].Path, p)
		}
	}
	if got.Files[1].Size != 20 {
		t.Errorf("b.txt size = %d, want 20 (increment overrides base)", got.Files[1].Size)
	}
}

// TestRestorePointContents_EmptyIncrementBrowsesAsBase verifies that an
// increment that captured no changes (empty index of its own) still browses
// as the base full's contents instead of an empty picker. Regression test for
// "Some of my restoration points are not allowing me to browse files".
func TestRestorePointContents_EmptyIncrementBrowsesAsBase(t *testing.T) {
	t.Parallel()
	h, storageRoot, jobID, _, incID := chainContentsFixture(t)

	writeContentsSidecar(t, storageRoot, "rp-chain-full", "fooitem", []engine.TarIndexEntry{
		{Path: "a.txt", Size: 1, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
		{Path: "b.txt", Size: 2, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
	})
	writeContentsSidecar(t, storageRoot, "rp-chain-inc", "fooitem", nil)

	w := getContents(t, h, jobID, incID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got engine.TarIndex
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want the base full's 2 entries", got.Files)
	}
}

// TestRestorePointContents_ChainStepMissingIndexFailsClosed verifies that a
// chain step whose index is missing (without metadata proving the item was
// not captured) fails the request instead of silently returning a partial
// file list.
func TestRestorePointContents_ChainStepMissingIndexFailsClosed(t *testing.T) {
	t.Parallel()
	h, storageRoot, jobID, _, incID := chainContentsFixture(t)

	// Only the increment has an index; the base full's is missing.
	writeContentsSidecar(t, storageRoot, "rp-chain-inc", "fooitem", []engine.TarIndexEntry{
		{Path: "c.txt", Size: 3, Mode: "0644", ModTime: "2026-01-02T00:00:00Z"},
	})

	w := getContents(t, h, jobID, incID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (fail closed); body: %s", w.Code, w.Body.String())
	}
}
