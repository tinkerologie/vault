package handlers

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ruaandeysel/vault/internal/crypto"
	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/notify"
	"github.com/ruaandeysel/vault/internal/tempdir"
)

// SettingsHandler handles global application settings.
type SettingsHandler struct {
	db              *db.DB
	serverKey       []byte // AES-256 key for sealing secrets at rest.
	onKeyChange     func() // called after API key is changed to invalidate caches.
	snapshotManager interface {
		SnapshotPath() string
		LastSnapshot() time.Time
	}
}

// NewSettingsHandler creates a new SettingsHandler.
func NewSettingsHandler(database *db.DB, serverKey []byte) *SettingsHandler {
	return &SettingsHandler{db: database, serverKey: serverKey}
}

// SetOnKeyChange sets a callback that is called after the API key changes.
func (h *SettingsHandler) SetOnKeyChange(fn func()) {
	h.onKeyChange = fn
}

// SetSnapshotManager sets the snapshot manager used by the database info endpoint.
func (h *SettingsHandler) SetSnapshotManager(sm interface {
	SnapshotPath() string
	LastSnapshot() time.Time
}) {
	h.snapshotManager = sm
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
	}

	respondJSON(w, http.StatusOK, info)
}

// SetSnapshotPath sets or clears the snapshot path override.
// Changes take effect on next daemon restart.
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
		dir := filepath.Dir(normalizedPath)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			respondError(w, http.StatusBadRequest, "parent directory does not exist")
			return
		}
		req.SnapshotPath = normalizedPath
	}

	if err := h.db.SetSetting("snapshot_path_override", req.SnapshotPath); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.GetDatabaseInfo(w, r)
}

// List returns all settings as a JSON object.
// Sensitive keys (hashes, sealed values) are excluded from the response.
func (h *SettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetAllSettings()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Provide defaults for known settings if not yet stored.
	defaults := map[string]string{
		"notifications_enabled": "true",
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
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Return the full settings object.
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.db.SetSetting("encryption_passphrase_hash", hash); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Seal the passphrase at rest using the server key.
	if len(h.serverKey) > 0 {
		sealed, sealErr := crypto.Seal(h.serverKey, req.Passphrase)
		if sealErr != nil {
			respondError(w, http.StatusInternalServerError, "sealing passphrase: "+sealErr.Error())
			return
		}
		if err := h.db.SetSetting("encryption_passphrase_sealed", sealed); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
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
			respondError(w, http.StatusInternalServerError, "server key is not configured")
			return
		}

		passphrase, err := crypto.Unseal(h.serverKey, sealed)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "unsealing passphrase: "+err.Error())
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

// apiKeySize is the number of random bytes used to generate an API key.
const apiKeySize = 32

// generateAPIKey creates a cryptographically random API key string.
func generateAPIKey() (string, error) {
	b := make([]byte, apiKeySize)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return "vault_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// GetAPIKeyStatus returns whether an API key is configured.
//
//	GET /api/v1/settings/api-key
func (h *SettingsHandler) GetAPIKeyStatus(w http.ResponseWriter, _ *http.Request) {
	hasKey := h.db.HasAPIKey()
	preview := ""
	if hasKey {
		sealed, _ := h.db.GetSetting("api_key_sealed", "")
		if sealed != "" && len(h.serverKey) > 0 {
			if key, err := crypto.Unseal(h.serverKey, sealed); err == nil && len(key) > 4 {
				preview = key[:6] + "..." + key[len(key)-4:]
			}
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled": hasKey,
		"preview": preview,
	})
}

// GenerateAPIKey generates a new API key and stores it.
// This endpoint is unauthenticated ONLY when no key exists (bootstrap).
// If a key already exists, it requires authentication (use RotateAPIKey instead).
//
//	POST /api/v1/settings/api-key/generate
func (h *SettingsHandler) GenerateAPIKey(w http.ResponseWriter, _ *http.Request) {
	if h.db.HasAPIKey() {
		respondError(w, http.StatusConflict, "API key already exists. Use rotate to change it.")
		return
	}

	key, err := generateAPIKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "generating key: "+err.Error())
		return
	}

	if err := h.storeAPIKey(key); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, map[string]string{
		"api_key": key,
		"message": "API key generated. Store it securely — it will not be shown again.",
	})
}

// RotateAPIKey generates a new API key, replacing the old one.
//
//	POST /api/v1/settings/api-key/rotate
func (h *SettingsHandler) RotateAPIKey(w http.ResponseWriter, _ *http.Request) {
	key, err := generateAPIKey()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "generating key: "+err.Error())
		return
	}

	if err := h.storeAPIKey(key); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"api_key": key,
		"message": "API key rotated. Update all clients with the new key.",
	})
}

// RevokeAPIKey removes the API key, disabling authentication.
//
//	DELETE /api/v1/settings/api-key
func (h *SettingsHandler) RevokeAPIKey(w http.ResponseWriter, _ *http.Request) {
	_ = h.db.SetSetting("api_key_hash", "")
	_ = h.db.SetSetting("api_key_sealed", "")

	if h.onKeyChange != nil {
		h.onKeyChange()
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "API key revoked. Authentication is now disabled.",
	})
}

// GetStagingInfo returns info about the current staging directory.
func (h *SettingsHandler) GetStagingInfo(w http.ResponseWriter, r *http.Request) {
	override, _ := h.db.GetSetting("staging_dir_override", "")
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
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
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return updated staging info.
	h.GetStagingInfo(w, r)
}

// storeAPIKey hashes, seals, and persists the API key, then invalidates caches.
func (h *SettingsHandler) storeAPIKey(key string) error {
	hash, err := crypto.HashPassphrase(key)
	if err != nil {
		return err
	}

	if err := h.db.SetSetting("api_key_hash", hash); err != nil {
		return err
	}

	if len(h.serverKey) > 0 {
		sealed, err := crypto.Seal(h.serverKey, key)
		if err != nil {
			return err
		}
		if err := h.db.SetSetting("api_key_sealed", sealed); err != nil {
			return err
		}
	}

	if h.onKeyChange != nil {
		h.onKeyChange()
	}

	return nil
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
