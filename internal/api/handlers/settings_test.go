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

	"github.com/ruaandeysel/vault/internal/db"
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

func TestGenerateAPIKey_Bootstrap(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	var keyChanged bool
	h.SetOnKeyChange(func() { keyChanged = true })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	key := resp["api_key"]
	if !strings.HasPrefix(key, "vault_") {
		t.Errorf("key %q missing vault_ prefix", key)
	}
	if len(key) < 20 {
		t.Errorf("key too short: %d", len(key))
	}
	if !keyChanged {
		t.Error("onKeyChange not called")
	}
}

func TestGenerateAPIKey_ConflictWhenExists(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Generate the first key.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap: status = %d", w.Code)
	}

	// Second attempt should return 409 Conflict.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w2 := httptest.NewRecorder()
	h.GenerateAPIKey(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want %d; body: %s", w2.Code, http.StatusConflict, w2.Body.String())
	}
}

func TestRotateAPIKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Bootstrap a key first.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap: status = %d", w.Code)
	}
	var first map[string]string
	_ = json.NewDecoder(w.Body).Decode(&first)

	// Rotate.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/rotate", nil)
	w2 := httptest.NewRecorder()
	h.RotateAPIKey(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("rotate: status = %d, want %d; body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}

	var second map[string]string
	_ = json.NewDecoder(w2.Body).Decode(&second)

	if second["api_key"] == first["api_key"] {
		t.Error("rotated key should differ from original")
	}
	if !strings.HasPrefix(second["api_key"], "vault_") {
		t.Errorf("rotated key %q missing vault_ prefix", second["api_key"])
	}
}

func TestRevokeAPIKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Bootstrap.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap: status = %d", w.Code)
	}

	var revoked bool
	h.SetOnKeyChange(func() { revoked = true })

	// Revoke.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/api-key", nil)
	w2 := httptest.NewRecorder()
	h.RevokeAPIKey(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d; body: %s", w2.Code, w2.Body.String())
	}
	if !revoked {
		t.Error("onKeyChange not called on revoke")
	}

	// Status should show disabled.
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	w3 := httptest.NewRecorder()
	h.GetAPIKeyStatus(w3, req3)
	var status map[string]any
	_ = json.NewDecoder(w3.Body).Decode(&status)

	if status["enabled"] != false {
		t.Errorf("expected enabled=false after revoke, got %v", status["enabled"])
	}
}

func TestGetAPIKeyStatus_NoKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	w := httptest.NewRecorder()
	h.GetAPIKeyStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if resp["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", resp["enabled"])
	}
	if resp["preview"] != "" {
		t.Errorf("expected empty preview, got %q", resp["preview"])
	}
}

func TestGetAPIKeyStatus_WithKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Bootstrap.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap: status = %d", w.Code)
	}

	// Check status.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	w2 := httptest.NewRecorder()
	h.GetAPIKeyStatus(w2, req2)

	var resp map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&resp)

	if resp["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", resp["enabled"])
	}

	preview, _ := resp["preview"].(string)
	if !strings.HasPrefix(preview, "vault_") {
		t.Errorf("preview %q should start with vault_", preview)
	}
	if !strings.Contains(preview, "...") {
		t.Errorf("preview %q should be masked with ...", preview)
	}
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

func TestGenerateAPIKey_Format(t *testing.T) {
	t.Parallel()

	// generateAPIKey is a package-level helper, test it directly.
	key, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "vault_") {
		t.Errorf("key %q missing vault_ prefix", key)
	}
	// 6 ("vault_") + base64url(32 bytes) ≈ 43 chars → total ~49.
	if len(key) < 40 {
		t.Errorf("key too short: %d chars", len(key))
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
