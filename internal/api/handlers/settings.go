package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ruaan-deysel/vault/internal/config"
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/diagnostics"
	"github.com/ruaan-deysel/vault/internal/notify"
	"github.com/ruaan-deysel/vault/internal/tempdir"
)

// SettingsHandler handles global application settings.
type SettingsHandler struct {
	db              *db.DB
	serverKey       []byte // AES-256 key for sealing secrets at rest.
	snapshotManager interface {
		SnapshotPath() string
		DefaultSnapshotPath() string
		SetSnapshotPath(string) error
		LastSnapshot() time.Time
		RestorationSource() *db.RestorationInfo
	}
	diagnosticsCollector *diagnostics.Collector
	onConfigChange       ConfigChangeHook
}

// NewSettingsHandler creates a new SettingsHandler.
func NewSettingsHandler(database *db.DB, serverKey []byte) *SettingsHandler {
	return &SettingsHandler{db: database, serverKey: serverKey}
}

// SetSnapshotManager sets the snapshot manager used by the database info endpoint.
func (h *SettingsHandler) SetSnapshotManager(sm interface {
	SnapshotPath() string
	DefaultSnapshotPath() string
	SetSnapshotPath(string) error
	LastSnapshot() time.Time
	RestorationSource() *db.RestorationInfo
}) {
	h.snapshotManager = sm
}

// RestorationInfo returns the current restoration info, or nil if not available.
func (h *SettingsHandler) RestorationInfo() *db.RestorationInfo {
	if h.snapshotManager == nil {
		return nil
	}
	return h.snapshotManager.RestorationSource()
}

// SetConfigChangeHook registers a function called after settings mutations to
// flush the database to USB flash.
func (h *SettingsHandler) SetConfigChangeHook(fn ConfigChangeHook) {
	h.onConfigChange = fn
}

// notifyConfigChange calls the config change hook if set.
func (h *SettingsHandler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// SetDiagnosticsCollector sets the diagnostics collector for the handler.
func (h *SettingsHandler) SetDiagnosticsCollector(dc *diagnostics.Collector) {
	h.diagnosticsCollector = dc
}

// GetDiagnostics generates and returns a diagnostic bundle as a ZIP file.
//
//	GET /api/v1/settings/diagnostics
func (h *SettingsHandler) GetDiagnostics(w http.ResponseWriter, _ *http.Request) {
	if h.diagnosticsCollector == nil {
		respondError(w, http.StatusServiceUnavailable, "diagnostics not configured")
		return
	}

	bundle, err := h.diagnosticsCollector.Collect()
	if err != nil {
		respondInternalError(w, fmt.Errorf("collecting diagnostics: %w", err))
		return
	}

	zipReader, err := diagnostics.PackageAsZip(bundle)
	if err != nil {
		respondInternalError(w, fmt.Errorf("packaging diagnostics: %w", err))
		return
	}
	if closer, ok := zipReader.(io.Closer); ok {
		defer closer.Close()
	}

	date := time.Now().Format("2006-01-02")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vault-diagnostics-%s.zip"`, date))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, zipReader); err != nil {
		log.Printf("streaming diagnostics ZIP: %v", err)
	}
}

// GetDatabaseInfo returns information about the database mode and location.
//
//	GET /api/v1/settings/database
func (h *SettingsHandler) GetDatabaseInfo(w http.ResponseWriter, _ *http.Request) {
	info := map[string]any{
		"mode":         "legacy_usb",
		"working_path": h.db.Path(),
	}

	// Include the configured snapshot path override (may be empty).
	override, _ := h.db.GetSetting("snapshot_path_override", "")
	info["snapshot_path_override"] = override

	if h.snapshotManager != nil {
		snapPath := h.snapshotManager.SnapshotPath()
		info["mode"] = "hybrid"
		info["snapshot_path"] = snapPath

		// Use in-memory timestamp if available, fall back to file mtime.
		lastSnap := h.snapshotManager.LastSnapshot()
		if fi, err := os.Stat(snapPath); err == nil {
			info["snapshot_size_bytes"] = fi.Size()
			if lastSnap.IsZero() {
				lastSnap = fi.ModTime()
			}
		}
		if !lastSnap.IsZero() {
			info["last_snapshot"] = lastSnap
		}

		// Include restoration info so the UI can display warnings.
		if ri := h.snapshotManager.RestorationSource(); ri != nil {
			info["restoration_source"] = ri.Source
			info["restoration_reason"] = ri.Reason
			if ri.Source == "usb_backup" || ri.Source == "fresh" {
				info["degraded"] = true
			}
		}
	}

	respondJSON(w, http.StatusOK, info)
}

// SetSnapshotPath sets or clears the snapshot path override.
// Changes are applied immediately — a fresh snapshot is saved at the new
// location so that the next daemon restart sees up-to-date data.
//
//	PUT /api/v1/settings/database
func (h *SettingsHandler) SetSnapshotPath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotPath string `json:"snapshot_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.SnapshotPath != "" {
		normalizedPath, err := normalizeConfigurablePath(req.SnapshotPath)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		// If the path points to an existing directory, automatically append
		// the default database filename so the user can just pick a folder.
		if fi, err := os.Stat(normalizedPath); err == nil && fi.IsDir() {
			normalizedPath = filepath.Join(normalizedPath, "vault.db")
		}
		dir := filepath.Dir(normalizedPath)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			respondError(w, http.StatusBadRequest, "parent directory does not exist")
			return
		}
		req.SnapshotPath = normalizedPath
	}

	if err := h.db.SetSetting("snapshot_path_override", req.SnapshotPath); err != nil {
		respondInternalError(w, err)
		return
	}

	// Persist the effective snapshot path to vault.cfg on USB flash so that
	// the daemon can read it before restoring the database on next reboot.
	if err := config.WriteCfgValue(config.DefaultCfgPath, "SNAPSHOT_PATH", req.SnapshotPath); err != nil {
		log.Printf("Warning: failed to persist SNAPSHOT_PATH to vault.cfg: %v", err)
	}

	// Apply the path change to the running snapshot manager immediately so
	// that a fresh snapshot is written at the new location. This eliminates
	// the stale-snapshot problem where the old location retains outdated data.
	if h.snapshotManager != nil {
		if err := h.snapshotManager.SetSnapshotPath(req.SnapshotPath); err != nil {
			log.Printf("Warning: failed to apply snapshot path change at runtime: %v", err)
		}
	}

	h.notifyConfigChange()
	h.GetDatabaseInfo(w, r)
}

// List returns all settings as a JSON object.
// Sensitive keys (hashes, sealed values) are excluded from the response.
func (h *SettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetAllSettings()
	if err != nil {
		respondInternalError(w, err)
		return
	}

	// Provide defaults for known settings if not yet stored.
	defaults := map[string]string{
		"notifications_enabled":    "true",
		"container_backup_enabled": "true",
		"vm_backup_enabled":        "true",
		"folder_backup_enabled":    "true",
		"flash_backup_enabled":     "true",
	}
	for k, v := range defaults {
		if _, ok := settings[k]; !ok {
			settings[k] = v
		}
	}

	// Remove sensitive keys from the response.
	for _, key := range sensitiveSettingKeys {
		delete(settings, key)
	}

	respondJSON(w, http.StatusOK, settings)
}

// sensitiveSettingKeys are setting keys that must never be returned in API responses.
var sensitiveSettingKeys = []string{
	"encryption_passphrase",
	"encryption_passphrase_hash",
	"encryption_passphrase_sealed",
	"api_key_hash",
	"api_key_sealed",
}

// Update accepts a JSON object of key-value pairs and upserts them.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var incoming map[string]string
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	for k, v := range incoming {
		if err := h.db.SetSetting(k, v); err != nil {
			respondInternalError(w, err)
			return
		}
	}

	// Return the full settings object.
	h.notifyConfigChange()
	h.List(w, r)
}

// SetEncryption sets the global encryption passphrase.
// The passphrase is stored as a bcrypt hash (for verification) and sealed
// with the server key (for the runner to use). No plaintext is stored.
//
//	POST /api/v1/settings/encryption
func (h *SettingsHandler) SetEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Passphrase == "" {
		// Disable encryption: clear hash, sealed value, and any legacy plaintext.
		_ = h.db.SetSetting("encryption_passphrase_hash", "")
		_ = h.db.SetSetting("encryption_passphrase_sealed", "")
		_ = h.db.SetSetting("encryption_passphrase", "") // clean up legacy
		respondJSON(w, http.StatusOK, map[string]any{
			"encryption_enabled": false,
			"message":            "encryption disabled",
		})
		return
	}

	hash, err := crypto.HashPassphrase(req.Passphrase)
	if err != nil {
		respondInternalError(w, err)
		return
	}

	if err := h.db.SetSetting("encryption_passphrase_hash", hash); err != nil {
		respondInternalError(w, err)
		return
	}

	// Seal the passphrase at rest using the server key.
	if len(h.serverKey) > 0 {
		sealed, sealErr := crypto.Seal(h.serverKey, req.Passphrase)
		if sealErr != nil {
			respondInternalError(w, fmt.Errorf("sealing passphrase: %w", sealErr))
			return
		}
		if err := h.db.SetSetting("encryption_passphrase_sealed", sealed); err != nil {
			respondInternalError(w, err)
			return
		}
	}
	// Clean up any legacy plaintext.
	_ = h.db.SetSetting("encryption_passphrase", "")

	respondJSON(w, http.StatusOK, map[string]any{
		"encryption_enabled": true,
		"message":            "encryption passphrase set",
	})
}

// VerifyEncryption verifies a passphrase against the stored hash.
//
//	POST /api/v1/settings/encryption/verify
func (h *SettingsHandler) VerifyEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	hash, _ := h.db.GetSetting("encryption_passphrase_hash", "")
	if hash == "" {
		respondJSON(w, http.StatusOK, map[string]any{
			"valid":   false,
			"message": "no encryption passphrase configured",
		})
		return
	}

	if err := crypto.VerifyPassphrase(req.Passphrase, hash); err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"valid":   false,
			"message": "passphrase does not match",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"valid":   true,
		"message": "passphrase verified",
	})
}

// GetEncryptionStatus returns whether encryption is configured.
//
//	GET /api/v1/settings/encryption
func (h *SettingsHandler) GetEncryptionStatus(w http.ResponseWriter, _ *http.Request) {
	hash, _ := h.db.GetSetting("encryption_passphrase_hash", "")
	respondJSON(w, http.StatusOK, map[string]any{
		"encryption_enabled": hash != "",
	})
}

// GetEncryptionPassphrase returns the recoverable encryption passphrase.
//
//	GET /api/v1/settings/encryption/passphrase
func (h *SettingsHandler) GetEncryptionPassphrase(w http.ResponseWriter, _ *http.Request) {
	sealed, _ := h.db.GetSetting("encryption_passphrase_sealed", "")
	if sealed != "" {
		if len(h.serverKey) == 0 {
			respondInternalError(w, fmt.Errorf("server key is not configured"))
			return
		}

		passphrase, err := crypto.Unseal(h.serverKey, sealed)
		if err != nil {
			respondInternalError(w, fmt.Errorf("unsealing passphrase: %w", err))
			return
		}

		respondJSON(w, http.StatusOK, map[string]string{"passphrase": passphrase})
		return
	}

	legacyPassphrase, _ := h.db.GetSetting("encryption_passphrase", "")
	if legacyPassphrase != "" {
		respondJSON(w, http.StatusOK, map[string]string{"passphrase": legacyPassphrase})
		return
	}

	respondError(w, http.StatusNotFound, "encryption passphrase not configured")
}

// GetStagingInfo returns info about the current staging directory.
func (h *SettingsHandler) GetStagingInfo(w http.ResponseWriter, r *http.Request) {
	override, _ := h.db.GetSetting("staging_dir_override", "")
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	configs := make([]tempdir.StorageConfig, len(dests))
	for i, d := range dests {
		configs[i] = tempdir.StorageConfig{Type: d.Type, Config: d.Config}
	}
	info := tempdir.ResolveInfo(configs, override)
	respondJSON(w, http.StatusOK, info)
}

// SetStagingOverride sets or clears the staging directory override.
func (h *SettingsHandler) SetStagingOverride(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Override string `json:"override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Override != "" {
		normalizedPath, err := normalizeConfigurablePath(req.Override)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if fi, err := os.Stat(normalizedPath); err != nil || !fi.IsDir() {
			respondError(w, http.StatusBadRequest, "path does not exist or is not a directory")
			return
		}
		req.Override = normalizedPath
	}

	if err := h.db.SetSetting("staging_dir_override", req.Override); err != nil {
		respondInternalError(w, err)
		return
	}

	// Return updated staging info.
	h.notifyConfigChange()
	h.GetStagingInfo(w, r)
}

// TestDiscordWebhook sends a test message to a Discord webhook URL.
//
//	POST /api/v1/settings/discord/test
func (h *SettingsHandler) TestDiscordWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.WebhookURL == "" {
		respondError(w, http.StatusBadRequest, "webhook_url is required")
		return
	}

	embed := notify.DiscordEmbed{
		Title:       "🔔 Test Notification",
		Description: "Vault is connected to Discord!",
		Color:       notify.ColorInfo,
		Fields: []notify.DiscordField{
			{Name: "Status", Value: "Connection verified", Inline: true},
		},
	}
	if err := notify.SendDiscord(req.WebhookURL, embed); err != nil {
		respondError(w, http.StatusBadGateway, "Discord webhook failed: "+err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "Test notification sent"})
}

// GetAPIKeyStatus returns whether an API key has been configured.
//
//	GET /api/v1/settings/api-key
func (h *SettingsHandler) GetAPIKeyStatus(w http.ResponseWriter, _ *http.Request) {
	hash, _ := h.db.GetSetting("api_key_hash", "")
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled": hash != "",
	})
}

// GenerateAPIKey creates a new API key, stores it sealed + hashed, and
// returns the plaintext key once. Subsequent reads use GetAPIKey.
//
//	POST /api/v1/settings/api-key/generate
func (h *SettingsHandler) GenerateAPIKey(w http.ResponseWriter, _ *http.Request) {
	key, err := crypto.GenerateAPIKey()
	if err != nil {
		respondInternalError(w, fmt.Errorf("generating API key: %w", err))
		return
	}

	hash, err := crypto.HashPassphrase(key)
	if err != nil {
		respondInternalError(w, fmt.Errorf("hashing API key: %w", err))
		return
	}

	if len(h.serverKey) == 0 {
		respondInternalError(w, fmt.Errorf("server key is not configured"))
		return
	}

	sealed, err := crypto.Seal(h.serverKey, key)
	if err != nil {
		respondInternalError(w, fmt.Errorf("sealing API key: %w", err))
		return
	}

	// Write sealed key first so that if the hash write fails no auth is
	// enabled yet, avoiding a state where remote clients get locked out.
	if err := h.db.SetSetting("api_key_sealed", sealed); err != nil {
		respondInternalError(w, fmt.Errorf("storing sealed API key: %w", err))
		return
	}
	if err := h.db.SetSetting("api_key_hash", hash); err != nil {
		// Roll back the sealed value to avoid a dangling sealed key.
		_ = h.db.SetSetting("api_key_sealed", "")
		respondInternalError(w, fmt.Errorf("storing API key hash: %w", err))
		return
	}

	h.notifyConfigChange()
	respondJSON(w, http.StatusCreated, map[string]string{
		"api_key": key,
		"message": "API key generated — you can reveal it later from Settings › Security",
	})
}

// GetAPIKey returns the stored API key by unsealing it. This allows the
// user to view/copy the key from the Settings UI.
//
//	GET /api/v1/settings/api-key/reveal
func (h *SettingsHandler) GetAPIKey(w http.ResponseWriter, _ *http.Request) {
	sealed, _ := h.db.GetSetting("api_key_sealed", "")
	if sealed == "" {
		respondError(w, http.StatusNotFound, "no API key configured")
		return
	}

	if len(h.serverKey) == 0 {
		respondInternalError(w, fmt.Errorf("server key is not configured"))
		return
	}

	key, err := crypto.Unseal(h.serverKey, sealed)
	if err != nil {
		respondInternalError(w, fmt.Errorf("unsealing API key: %w", err))
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	respondJSON(w, http.StatusOK, map[string]string{"api_key": key})
}

// RotateAPIKey replaces the existing API key with a new random one.
//
//	POST /api/v1/settings/api-key/rotate
func (h *SettingsHandler) RotateAPIKey(w http.ResponseWriter, r *http.Request) {
	// Verify an existing key is set.
	hash, _ := h.db.GetSetting("api_key_hash", "")
	if hash == "" {
		respondError(w, http.StatusBadRequest, "no existing API key to rotate — generate one first")
		return
	}

	// Generate handles all storage — reuse the handler.
	h.GenerateAPIKey(w, r)
}

// RevokeAPIKey removes the stored API key, disabling authentication.
//
//	DELETE /api/v1/settings/api-key
func (h *SettingsHandler) RevokeAPIKey(w http.ResponseWriter, _ *http.Request) {
	if err := h.db.SetSetting("api_key_hash", ""); err != nil {
		respondInternalError(w, fmt.Errorf("clearing API key hash: %w", err))
		return
	}
	if err := h.db.SetSetting("api_key_sealed", ""); err != nil {
		respondInternalError(w, fmt.Errorf("clearing sealed API key: %w", err))
		return
	}

	h.notifyConfigChange()
	respondJSON(w, http.StatusOK, map[string]string{
		"message": "API key revoked — authentication disabled",
	})
}
