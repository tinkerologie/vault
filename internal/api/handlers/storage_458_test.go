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

// seedDest458 inserts a storage destination with the given type and config
// straight into the DB (bypassing adapter validation).
func seedDest458(t *testing.T, d *db.DB, typ, cfg string, dedupOn bool) int64 {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "d458-" + nextUnique(), Type: typ, Config: cfg, DedupEnabled: dedupOn,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	return id
}

func localCfg458(p string) string {
	b, _ := json.Marshal(map[string]string{"path": p})
	return string(b)
}

func TestStorageRewrapDedupKeys458(t *testing.T) {
	t.Parallel()

	t.Run("no dedup destinations returns empty array", func(t *testing.T) {
		t.Parallel()
		h, _ := newDedupStorageHandler(t, false)
		w := httptest.NewRecorder()
		h.RewrapDedupKeys(w, httptest.NewRequest(http.MethodPost, "/x", nil))
		wantStatus458(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), `"destinations":[]`) {
			t.Errorf("body = %s", w.Body.String())
		}
	})

	t.Run("runner error", func(t *testing.T) {
		t.Parallel()
		h, _ := newDedupStorageHandler(t, false)
		_ = h.db.Close()
		w := httptest.NewRecorder()
		h.RewrapDedupKeys(w, httptest.NewRequest(http.MethodPost, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestStorageMaintenanceLock458(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	mu := h.MaintenanceLock()
	if mu == nil || mu != h.MaintenanceLock() {
		t.Fatal("MaintenanceLock must return the same non-nil mutex")
	}
	if !mu.TryLock() {
		t.Fatal("fresh lock should be free")
	}
	mu.Unlock()
}

func TestStorageUpdate458_WriteError(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	failWrites458(t, h.db, "storage_destinations", "UPDATE")
	w := httptest.NewRecorder()
	h.Update(w, reqWithID(http.MethodPut, "/x", idStr(destID), []byte(`{"name":"renamed"}`)))
	if w.Code < 400 {
		t.Fatalf("status = %d, want error; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageDelete458_Branches(t *testing.T) {
	t.Parallel()

	t.Run("deleteFiles cleans storage", func(t *testing.T) {
		t.Parallel()
		h, destID := newDedupStorageHandler(t, false)
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", idStr(destID), nil))
		wantStatus458(t, w, http.StatusNoContent)
	})

	t.Run("deleteFiles with an unusable adapter fails before deleting", func(t *testing.T) {
		t.Parallel()
		h := newStorageHandlerForTest(t)
		bogus := seedDest458(t, h.db, "bogus-type", "{}", false)
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x?deleteFiles=true", idStr(bogus), nil))
		wantStatus458(t, w, http.StatusBadRequest)
		if _, err := h.db.GetStorageDestination(bogus); err != nil {
			t.Fatalf("record must survive a cleanup that could not run: %v", err)
		}
	})

	t.Run("force disable error", func(t *testing.T) {
		t.Parallel()
		h, destID := newDedupStorageHandler(t, false)
		if _, err := h.db.CreateJob(db.Job{Name: "dep-" + nextUnique(), StorageDestID: destID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		failWrites458(t, h.db, "jobs", "UPDATE")
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x?force=true", idStr(destID), nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("delete row error", func(t *testing.T) {
		t.Parallel()
		h, destID := newDedupStorageHandler(t, false)
		failWrites458(t, h.db, "storage_destinations", "DELETE")
		w := httptest.NewRecorder()
		h.Delete(w, reqWithID(http.MethodDelete, "/x", idStr(destID), nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestStorageTestConnectionAndConfig458(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	missing := filepath.Join(t.TempDir(), "missing")
	destID := seedDest458(t, h.db, "local", localCfg458(missing), false)

	w := httptest.NewRecorder()
	h.TestConnection(w, reqWithID(http.MethodPost, "/x", idStr(destID), nil))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Errorf("TestConnection body = %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	h.TestConfig(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{bad")))
	wantStatus458(t, w, http.StatusBadRequest)

	body, _ := json.Marshal(map[string]string{"type": "local", "config": localCfg458(missing)})
	w = httptest.NewRecorder()
	h.TestConfig(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(body))))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Errorf("TestConfig body = %s", w.Body.String())
	}
}

func TestStorageCloseBreaker458_WriteError(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	failWrites458(t, h.db, "storage_destinations", "UPDATE")
	w := httptest.NewRecorder()
	h.CloseBreaker(w, reqWithID(http.MethodPost, "/x", idStr(destID), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestStorageOrphans458(t *testing.T) {
	t.Parallel()

	t.Run("scan on dedup destination errors", func(t *testing.T) {
		t.Parallel()
		h, destID := newDedupStorageHandler(t, true)
		w := httptest.NewRecorder()
		h.ScanOrphans(w, reqWithID(http.MethodPost, "/x", idStr(destID), nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("delete with paths", func(t *testing.T) {
		t.Parallel()
		h, destID := newDedupStorageHandler(t, false)
		w := httptest.NewRecorder()
		h.DeleteOrphans(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"paths":["not/an/orphan"]}`)))
		wantStatus458(t, w, http.StatusOK)
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if _, ok := resp["deleted"]; !ok {
			t.Errorf("missing deleted key: %v", resp)
		}
	})
}

func TestStorageScan458_VaultDBAndAppdataError(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	if err := os.MkdirAll(filepath.Join(storageDir, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "_vault", "vault.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.Scan(w, reqWithID(http.MethodPost, "/x?path=does-not-exist", idStr(destID), nil))
	wantStatus458(t, w, http.StatusOK)
	var resp struct {
		VaultDB map[string]any `json:"vault_db"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.VaultDB == nil || resp.VaultDB["path"] != "_vault" {
		t.Errorf("vault_db = %v", resp.VaultDB)
	}
}

func TestStorageListDBBackups458(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)

	w := httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/x", "bad", nil))
	wantStatus458(t, w, http.StatusBadRequest)

	w = httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/x", "9999", nil))
	wantStatus458(t, w, http.StatusNotFound)

	bogus := seedDest458(t, h.db, "bogus-type", "{}", false)
	w = httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/x", idStr(bogus), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestStorageAdapterErrors458(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	bogus := seedDest458(t, h.db, "bogus-type", "{}", false)

	w := httptest.NewRecorder()
	h.DownloadFile(w, reqWithID(http.MethodGet, "/x?path=a.txt", idStr(bogus), nil))
	wantStatus458(t, w, http.StatusInternalServerError)

	// Local destination whose base path is a regular file: List fails.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileDest := seedDest458(t, h.db, "local", localCfg458(f), false)
	w = httptest.NewRecorder()
	h.ListFiles(w, reqWithID(http.MethodGet, "/x", idStr(fileDest), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestStorageDependentJobs458_ListError(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	breakTable458(t, h.db, "jobs")
	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/x", idStr(destID), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestStorageGetDedupStats458_CorruptRepo(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_vault", "repo.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	destID := seedDest458(t, h.db, "local", localCfg458(dir), true)
	w := httptest.NewRecorder()
	h.GetDedupStats(w, reqWithID(http.MethodGet, "/x", idStr(destID), nil))
	if w.Code < 400 {
		t.Fatalf("status = %d, want error; body: %s", w.Code, w.Body.String())
	}
}

func TestStorageRefreshCapacity458_PersistError(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)
	failWrites458(t, h.db, "storage_destinations", "UPDATE")
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/x", idStr(destID), nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestStorageRefreshCapacity458_ProbeError(t *testing.T) {
	t.Parallel()
	h := newStorageHandlerForTest(t)
	destID := seedDest458(t, h.db, "local", localCfg458(filepath.Join(t.TempDir(), "missing")), false)
	w := httptest.NewRecorder()
	h.RefreshCapacity(w, reqWithID(http.MethodPost, "/x", idStr(destID), nil))
	wantStatus458(t, w, http.StatusBadGateway)
}

func TestPreserveRedactedSecrets458(t *testing.T) {
	t.Parallel()
	in := `{"password":"` + redactionMarker + `","user":"u"}`

	// Existing config is not JSON: the marker is left alone.
	got, err := preserveRedactedSecrets(in, "not json")
	if err != nil || got != in {
		t.Fatalf("got %q, %v; want passthrough", got, err)
	}

	// Existing config lacks the key: still a passthrough.
	got, err = preserveRedactedSecrets(in, `{"user":"u"}`)
	if err != nil || got != in {
		t.Fatalf("got %q, %v; want passthrough", got, err)
	}

	// Non-string sensitive value is ignored.
	got, err = preserveRedactedSecrets(`{"password":5}`, `{"password":"old"}`)
	if err != nil || got != `{"password":5}` {
		t.Fatalf("got %q, %v", got, err)
	}

	// Marker replaced from existing.
	got, err = preserveRedactedSecrets(in, `{"password":"secret"}`)
	if err != nil || !strings.Contains(got, `"secret"`) {
		t.Fatalf("got %q, %v; want restored secret", got, err)
	}
}

func TestStorageImport458_NotFoundAndBadJSON(t *testing.T) {
	t.Parallel()
	h, destID := newDedupStorageHandler(t, false)

	w := httptest.NewRecorder()
	h.Import(w, reqWithID(http.MethodPost, "/x", "9999", []byte(`{}`)))
	wantStatus458(t, w, http.StatusNotFound)

	w = httptest.NewRecorder()
	h.Import(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{bad`)))
	wantStatus458(t, w, http.StatusBadRequest)
}
