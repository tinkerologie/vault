package handlers

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// newTestSettingsHandler creates a SettingsHandler backed by an in-memory DB
// and a random AES-256 server key, suitable for unit tests.
func newTestSettingsHandler(t *testing.T) *SettingsHandler {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("random key: %v", err)
	}
	return NewSettingsHandler(database, key)
}

func TestGetStagingInfo(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/staging", nil)
	w := httptest.NewRecorder()
	h.GetStagingInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Should have a resolved_path (at minimum the system temp dir).
	if resp["resolved_path"] == nil || resp["resolved_path"] == "" {
		t.Error("expected non-empty resolved_path")
	}
	if resp["source"] == nil || resp["source"] == "" {
		t.Error("expected non-empty source")
	}
	// Cascade should be present.
	cascade, ok := resp["cascade"].([]any)
	if !ok || len(cascade) == 0 {
		t.Error("expected non-empty cascade list")
	}
}

func TestSetStagingOverride_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestSetStagingOverride_RelativePath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"override": "relative/path"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestSetStagingOverride_NonexistentPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"override": "/tmp/nonexistent/path/that/does/not/exist"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestSetStagingOverride_ValidPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	tmpDir, err := os.MkdirTemp("/tmp", "vault-staging-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	body := `{"override": "` + tmpDir + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["override"] != tmpDir {
		t.Errorf("override = %v, want %s", resp["override"], tmpDir)
	}
	if resp["source"] != "override" {
		t.Errorf("source = %v, want override", resp["source"])
	}
}

func TestSetStagingOverride_ClearOverride(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// First set an override.
	tmpDir, err := os.MkdirTemp("/tmp", "vault-staging-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	body := `{"override": "` + tmpDir + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set: status = %d", w.Code)
	}

	// Clear the override by sending an empty string.
	body2 := `{"override": ""}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/settings/staging", strings.NewReader(body2))
	w2 := httptest.NewRecorder()
	h.SetStagingOverride(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, want %d; body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["override"] != "" {
		t.Errorf("override = %v, want empty string", resp["override"])
	}
	if resp["source"] == "override" {
		t.Error("source should not be 'override' after clearing")
	}
}

func TestSetSnapshotPath_ValidPath(t *testing.T) {
	t.Parallel()

	h := newTestSettingsHandler(t)
	snapshotDir, err := os.MkdirTemp("/tmp", "vault-snapshot-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(snapshotDir) })

	snapshotPath := filepath.Join(snapshotDir, "vault.db")
	body := `{"snapshot_path": "` + snapshotPath + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	stored, _ := h.db.GetSetting("snapshot_path_override", "")
	if stored != snapshotPath {
		t.Fatalf("snapshot_path_override = %q, want %q", stored, snapshotPath)
	}
}

func TestSetSnapshotPath_RejectsOutsideRoots(t *testing.T) {
	t.Parallel()

	h := newTestSettingsHandler(t)
	body := `{"snapshot_path": "/var/lib/vault/vault.db"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/database", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
