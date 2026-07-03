package handlers

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/diagnostics"
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

func TestGetDiagnostics(t *testing.T) {
	t.Parallel()

	h := newTestSettingsHandler(t)
	statusFn := func() diagnostics.RunnerStatus {
		return diagnostics.RunnerStatus{Active: false}
	}
	collector := diagnostics.NewCollector(h.db, statusFn, "test-version", nil)
	h.SetDiagnosticsCollector(collector)

	req := httptest.NewRequest("GET", "/api/v1/settings/diagnostics", nil)
	w := httptest.NewRecorder()
	h.GetDiagnostics(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	if cd := resp.Header.Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition header")
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parsing zip: %v", err)
	}

	// The split layout produces multiple JSON files; find and decode
	// diagnostics.json as the top-level summary.
	var summary *zip.File
	for _, f := range zr.File {
		if f.Name == "diagnostics.json" {
			summary = f
			break
		}
	}
	if summary == nil {
		t.Fatalf("diagnostics.json missing from zip (got %d files)", len(zr.File))
	}

	f, err := summary.Open()
	if err != nil {
		t.Fatalf("opening zip entry: %v", err)
	}
	defer f.Close()

	var bundle diagnostics.DiagnosticBundle
	if err := json.NewDecoder(f).Decode(&bundle); err != nil {
		t.Fatalf("decoding diagnostics json: %v", err)
	}

	if bundle.System.Version != "test-version" {
		t.Errorf("version = %q, want test-version", bundle.System.Version)
	}
}

func TestGetDiagnosticsNotConfigured(t *testing.T) {
	t.Parallel()

	h := newTestSettingsHandler(t)
	// Do NOT set a diagnostics collector.

	req := httptest.NewRequest("GET", "/api/v1/settings/diagnostics", nil)
	w := httptest.NewRecorder()
	h.GetDiagnostics(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// SetSnapshotManager / SetConfigChangeHook trivial setters
// ---------------------------------------------------------------------------

// mockSnapshotManager satisfies the snapshotManager interface used by SettingsHandler.
type mockSnapshotManager struct {
	snapshotPath string
}

func (m *mockSnapshotManager) SnapshotPath() string        { return m.snapshotPath }
func (m *mockSnapshotManager) DefaultSnapshotPath() string { return "/default/vault.db" }
func (m *mockSnapshotManager) SetSnapshotPath(p string) error {
	m.snapshotPath = p
	return nil
}
func (m *mockSnapshotManager) LastSnapshot() time.Time                { return time.Time{} }
func (m *mockSnapshotManager) RestorationSource() *db.RestorationInfo { return nil }

func TestSetSnapshotManager_NoPanic(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	sm := &mockSnapshotManager{snapshotPath: "/some/path/vault.db"}
	h.SetSnapshotManager(sm) // should not panic
	if h.snapshotManager == nil {
		t.Error("snapshotManager should be set after SetSnapshotManager")
	}
}

func TestSetConfigChangeHook_NoPanic(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	called := false
	h.SetConfigChangeHook(func() { called = true })
	h.notifyConfigChange()
	if !called {
		t.Error("config change hook was not called")
	}
}

func TestSetConfigChangeHook_Nil(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	// nil hook must not panic when notifyConfigChange is called
	h.SetConfigChangeHook(nil)
	h.notifyConfigChange() // must not panic
}

// ---------------------------------------------------------------------------
// List / Update
// ---------------------------------------------------------------------------

func TestSettingsList_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Defaults must be present even for a fresh DB.
	for _, k := range []string{
		"notifications_enabled",
		"container_backup_enabled",
		"vm_backup_enabled",
		"folder_backup_enabled",
		"flash_backup_enabled",
	} {
		if resp[k] == "" {
			t.Errorf("key %q missing from defaults", k)
		}
	}
}

func TestSettingsList_SensitiveKeysExcluded(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Seed a sensitive key manually.
	if err := h.db.SetSetting("api_key_hash", "somehash"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["api_key_hash"]; ok {
		t.Error("api_key_hash should be excluded from List response")
	}
}

func TestSettingsUpdate_HappyPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"notifications_enabled":"false","my_custom_key":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Verify persisted.
	val, _ := h.db.GetSetting("notifications_enabled", "")
	if val != "false" {
		t.Errorf("notifications_enabled = %q, want false", val)
	}
	val2, _ := h.db.GetSetting("my_custom_key", "")
	if val2 != "hello" {
		t.Errorf("my_custom_key = %q, want hello", val2)
	}
}

func TestSettingsUpdate_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSettingsUpdate_ProtectedKeyRejected(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"api_key_hash":"hacker"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Encryption lifecycle
// ---------------------------------------------------------------------------

func TestSetEncryption_HappyPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"passphrase":"hunter42!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetEncryption(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["encryption_enabled"] != true {
		t.Errorf("encryption_enabled = %v, want true", resp["encryption_enabled"])
	}

	// Hash and sealed value must be stored.
	hash, _ := h.db.GetSetting("encryption_passphrase_hash", "")
	if hash == "" {
		t.Error("encryption_passphrase_hash not stored")
	}
	sealed, _ := h.db.GetSetting("encryption_passphrase_sealed", "")
	if sealed == "" {
		t.Error("encryption_passphrase_sealed not stored")
	}
}

func TestSetEncryption_TooShortPassphrase(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"passphrase":"short"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SetEncryption(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestSetEncryption_EmptyPassphraseDisables(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// First enable it.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	w1 := httptest.NewRecorder()
	h.SetEncryption(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("set: status = %d", w1.Code)
	}

	// Then disable by sending empty passphrase.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":""}`))
	w2 := httptest.NewRecorder()
	h.SetEncryption(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("disable: status = %d, want 200; body: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["encryption_enabled"] != false {
		t.Errorf("encryption_enabled = %v, want false", resp["encryption_enabled"])
	}
}

func TestSetEncryption_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.SetEncryption(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestGetEncryptionStatus_Enabled(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Enable encryption first.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	h.SetEncryption(httptest.NewRecorder(), req1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["encryption_enabled"] != true {
		t.Errorf("encryption_enabled = %v, want true", resp["encryption_enabled"])
	}
}

func TestGetEncryptionStatus_Disabled(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Fresh DB — no encryption configured.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["encryption_enabled"] != false {
		t.Errorf("encryption_enabled = %v, want false", resp["encryption_enabled"])
	}
}

func TestVerifyEncryption_Correct(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Set passphrase first.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	h.SetEncryption(httptest.NewRecorder(), req1)

	// Verify with correct passphrase.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption/verify",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	w := httptest.NewRecorder()
	h.VerifyEncryption(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["valid"] != true {
		t.Errorf("valid = %v, want true", resp["valid"])
	}
}

func TestVerifyEncryption_Incorrect(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Set passphrase first.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	h.SetEncryption(httptest.NewRecorder(), req1)

	// Verify with wrong passphrase.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption/verify",
		strings.NewReader(`{"passphrase":"wrongpass!"}`))
	w := httptest.NewRecorder()
	h.VerifyEncryption(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["valid"] != false {
		t.Errorf("valid = %v, want false", resp["valid"])
	}
}

func TestVerifyEncryption_NoPassphraseConfigured(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption/verify",
		strings.NewReader(`{"passphrase":"anything"}`))
	w := httptest.NewRecorder()
	h.VerifyEncryption(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["valid"] != false {
		t.Errorf("valid = %v, want false", resp["valid"])
	}
}

func TestVerifyEncryption_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption/verify",
		strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.VerifyEncryption(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestGetEncryptionPassphrase_HappyPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Set passphrase first.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/settings/encryption",
		strings.NewReader(`{"passphrase":"hunter42!"}`))
	h.SetEncryption(httptest.NewRecorder(), req1)

	// Retrieve it.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption/passphrase", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionPassphrase(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["passphrase"] != "hunter42!" {
		t.Errorf("passphrase = %q, want hunter42!", resp["passphrase"])
	}
}

func TestGetEncryptionPassphrase_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption/passphrase", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionPassphrase(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestGetEncryptionPassphrase_LegacyPlaintext(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Seed a legacy plaintext passphrase directly (no sealed value).
	if err := h.db.SetSetting("encryption_passphrase", "legacypass"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption/passphrase", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionPassphrase(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["passphrase"] != "legacypass" {
		t.Errorf("passphrase = %q, want legacypass", resp["passphrase"])
	}
}

// ---------------------------------------------------------------------------
// API key lifecycle
// ---------------------------------------------------------------------------

func TestGetAPIKeyStatus_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	w := httptest.NewRecorder()
	h.GetAPIKeyStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] != false {
		t.Errorf("enabled = %v, want false", resp["enabled"])
	}
}

func TestGenerateAPIKey_HappyPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["api_key"] == "" {
		t.Error("expected non-empty api_key in response")
	}

	// Verify GetAPIKeyStatus now shows enabled.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	w2 := httptest.NewRecorder()
	h.GetAPIKeyStatus(w2, req2)

	var statusResp map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusResp["enabled"] != true {
		t.Errorf("enabled = %v, want true after generate", statusResp["enabled"])
	}
}

func TestGetAPIKey_AfterGenerate(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Generate a key first.
	genReq := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	genW := httptest.NewRecorder()
	h.GenerateAPIKey(genW, genReq)
	if genW.Code != http.StatusCreated {
		t.Fatalf("generate: status = %d", genW.Code)
	}
	var genResp map[string]string
	if err := json.NewDecoder(genW.Body).Decode(&genResp); err != nil {
		t.Fatalf("decode gen: %v", err)
	}
	generatedKey := genResp["api_key"]

	// Retrieve it.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key/reveal", nil)
	w := httptest.NewRecorder()
	h.GetAPIKey(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["api_key"] != generatedKey {
		t.Errorf("api_key = %q, want %q", resp["api_key"], generatedKey)
	}
}

func TestGetAPIKey_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key/reveal", nil)
	w := httptest.NewRecorder()
	h.GetAPIKey(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestRotateAPIKey_ReplacesOldKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Generate original.
	genReq := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	genW := httptest.NewRecorder()
	h.GenerateAPIKey(genW, genReq)
	if genW.Code != http.StatusCreated {
		t.Fatalf("generate: status = %d", genW.Code)
	}
	var genResp map[string]string
	if err := json.NewDecoder(genW.Body).Decode(&genResp); err != nil {
		t.Fatalf("decode gen: %v", err)
	}
	originalKey := genResp["api_key"]

	// Rotate.
	rotReq := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/rotate", nil)
	rotW := httptest.NewRecorder()
	h.RotateAPIKey(rotW, rotReq)
	if rotW.Code != http.StatusCreated {
		t.Fatalf("rotate: status = %d, want 201; body: %s", rotW.Code, rotW.Body.String())
	}
	var rotResp map[string]string
	if err := json.NewDecoder(rotW.Body).Decode(&rotResp); err != nil {
		t.Fatalf("decode rotate: %v", err)
	}
	newKey := rotResp["api_key"]
	if newKey == "" {
		t.Error("new key should not be empty")
	}
	if newKey == originalKey {
		t.Error("rotated key should differ from original")
	}
}

func TestRotateAPIKey_NoExistingKey(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Rotate without any key set should fail.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/rotate", nil)
	w := httptest.NewRecorder()
	h.RotateAPIKey(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRevokeAPIKey_DisablesAuth(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Generate first.
	genReq := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	h.GenerateAPIKey(httptest.NewRecorder(), genReq)

	// Revoke.
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/api-key", nil)
	w := httptest.NewRecorder()
	h.RevokeAPIKey(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Status must be disabled now.
	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key", nil)
	statusW := httptest.NewRecorder()
	h.GetAPIKeyStatus(statusW, statusReq)

	var statusResp map[string]any
	if err := json.NewDecoder(statusW.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if statusResp["enabled"] != false {
		t.Errorf("enabled = %v after revoke, want false", statusResp["enabled"])
	}
}

// ---------------------------------------------------------------------------
// TestDiscordWebhook
// ---------------------------------------------------------------------------

func TestTestDiscordWebhook_MissingURL(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"webhook_url":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/discord/test", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestDiscordWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestTestDiscordWebhook_InvalidJSON(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/discord/test",
		strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.TestDiscordWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestTestDiscordWebhook_InvalidScheme(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	body := `{"webhook_url":"ftp://discord.com/api/webhooks/123/abc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/discord/test",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestDiscordWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestTestDiscordWebhook_BadWebhookFormat(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// A valid https URL that is not a Discord webhook format — SendDiscord
	// will return an error which the handler converts to 502.
	body := `{"webhook_url":"https://not-discord.example.com/api/webhooks/123/abc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/discord/test",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestDiscordWebhook(w, req)

	// normalizeDiscordWebhookURL rejects non-discord hosts; expect 502 BadGateway.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestTestDiscordWebhook_InvalidWebhookPath(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// A Discord URL with an invalid path (no /api/webhooks/<id>/<token>).
	body := `{"webhook_url":"https://discord.com/not/a/webhook"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/discord/test",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	h.TestDiscordWebhook(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestGenerateAPIKey_NoServerKey(t *testing.T) {
	t.Parallel()
	// Handler with empty server key must return 500.
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	h := NewSettingsHandler(database, nil) // no server key

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/api-key/generate", nil)
	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

func TestGetAPIKey_NoServerKey(t *testing.T) {
	t.Parallel()
	// Seed a sealed value directly but give the handler no server key.
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.SetSetting("api_key_sealed", "somesealedvalue"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	h := NewSettingsHandler(database, nil) // no server key

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/api-key/reveal", nil)
	w := httptest.NewRecorder()
	h.GetAPIKey(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

func TestGetEncryptionPassphrase_NoServerKey(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	// Store a fake sealed value so the handler tries to unseal.
	if err := database.SetSetting("encryption_passphrase_sealed", "somesealedvalue"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	h := NewSettingsHandler(database, nil) // no server key

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/encryption/passphrase", nil)
	w := httptest.NewRecorder()
	h.GetEncryptionPassphrase(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GetDatabaseInfo with snapshotManager
// ---------------------------------------------------------------------------

func TestGetDatabaseInfo_WithSnapshotManager(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)

	// Create a real temp file to back the snapshot path stat call.
	tmpDir := t.TempDir()
	snapFile := filepath.Join(tmpDir, "vault.db")
	if f, err := os.Create(snapFile); err == nil {
		f.Close()
	}

	sm := &mockSnapshotManager{snapshotPath: snapFile}
	h.SetSnapshotManager(sm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/database", nil)
	w := httptest.NewRecorder()
	h.GetDatabaseInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["mode"] != "hybrid" {
		t.Errorf("mode = %v, want hybrid", resp["mode"])
	}
	if resp["snapshot_path"] == nil {
		t.Error("expected snapshot_path in response")
	}
}
