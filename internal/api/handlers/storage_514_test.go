package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// Issue #514: a local destination saved with an empty path (the directory
// picker's root listing) resolved against the daemon's working directory,
// /usr/local/emhttp on Unraid, and "Delete Storage & Files" then erased the
// WebGUI. These tests pin the API side of the fix.

func TestStorageCreateRejectsUnsafeLocalRoots514(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"", "   ", ".", "relative/dir", "/", "/usr", "/usr/local/emhttp", "/etc", "/boot", "/mnt", "/mnt/user", "/mnt/user/"} {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			h := newStorageHandlerForTest(t)
			cfg, _ := json.Marshal(map[string]string{"path": p})
			body, _ := json.Marshal(map[string]string{"name": "unsafe-" + nextUnique(), "type": "local", "config": string(cfg)})
			w := httptest.NewRecorder()
			h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/storage", strings.NewReader(string(body))))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("path %q: status = %d, want 400; body: %s", p, w.Code, w.Body.String())
			}
			dests, err := h.db.ListStorageDestinations()
			if err != nil {
				t.Fatal(err)
			}
			if len(dests) != 0 {
				t.Fatalf("path %q: a destination was persisted", p)
			}
		})
	}
}

func TestStorageUpdateAndTestRejectUnsafeLocalRoot514(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	destID := seedDest458(t, h.db, "local", localCfg458(t.TempDir()), false)

	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/x", idStr(destID), []byte(`{"config":"{\"path\":\"/\"}"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update to /: status = %d, want 400; body: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.TestConfig(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"type":"local","config":"{\"path\":\"\"}"}`)))
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["success"] != false || !strings.Contains(res["error"].(string), "required") {
		t.Fatalf("test-config with empty path: got %v", res)
	}
}

// A destination saved before validation existed must fail closed: deleting
// it with files is refused before anything is touched, and deleting only
// the record still works.
func TestStorageDeleteLegacyUnsafeDestination514(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	destID := seedDest458(t, h.db, "local", `{"path":""}`, false)

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", idStr(destID), nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("deleteFiles on unsafe root: status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if _, err := h.db.GetStorageDestination(destID); err != nil {
		t.Fatalf("record must survive: %v", err)
	}

	w = httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/x", idStr(destID), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("keep-files delete: status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDeleteMissingDestinationWithFiles514(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", "987654", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// A partial cleanup still deletes the record but reports cleanup_error, and
// content Vault does not own survives.
func TestStorageDeleteReportsCleanupError514(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	root := t.TempDir()
	destID := seedDest458(t, h.db, "local", localCfg458(root), false)
	jobID, err := h.db.CreateJob(db.Job{Name: "j514-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := h.db.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	// A recorded path that is a single top-level folder is refused.
	if _, err := h.db.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "foreign", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "foreign", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true&force=true", idStr(destID), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["deleted"] != true || res["cleanup_error"] == nil {
		t.Fatalf("want deleted with cleanup_error, got %v", res)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign content must survive: %v", err)
	}
}

func TestStorageDeleteWithFilesLookupFailures514(t *testing.T) {
	t.Parallel()
	t.Run("database error", func(t *testing.T) {
		t.Parallel()
		h := newStorageHandlerForTest(t)
		destID := seedDest458(t, h.db, "local", localCfg458(t.TempDir()), false)
		breakTable458(t, h.db, "storage_destinations")
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", idStr(destID), nil))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
		}
	})
	t.Run("no runner", func(t *testing.T) {
		t.Parallel()
		d := newTestDB(t)
		h := NewStorageHandler(d, nil, make([]byte, 32))
		destID := seedDest458(t, d, "local", localCfg458(t.TempDir()), false)
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", idStr(destID), nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
		}
		if _, err := d.GetStorageDestination(destID); err != nil {
			t.Fatalf("record must survive: %v", err)
		}
	})
}
