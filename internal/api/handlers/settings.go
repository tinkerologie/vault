package handlers

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/ruaandeysel/vault/internal/crypto"
	"github.com/ruaandeysel/vault/internal/db"
)

// SettingsHandler handles global application settings.
type SettingsHandler struct {
	db          *db.DB
	serverKey   []byte // AES-256 key for sealing secrets at rest.
	onKeyChange func() // called after API key is changed to invalidate caches.
}

// NewSettingsHandler creates a new SettingsHandler.
func NewSettingsHandler(database *db.DB, serverKey []byte) *SettingsHandler {
	return &SettingsHandler{db: database, serverKey: serverKey}
}

// SetOnKeyChange sets a callback that is called after the API key changes.
func (h *SettingsHandler) SetOnKeyChange(fn func()) {
	h.onKeyChange = fn
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

// GetEncryptionPassphrase returns the unsealed encryption passphrase.
// This is used by the UI to show the passphrase or generate an emergency kit.
//
//	GET /api/v1/settings/encryption/passphrase
func (h *SettingsHandler) GetEncryptionPassphrase(w http.ResponseWriter, _ *http.Request) {
	sealed, _ := h.db.GetSetting("encryption_passphrase_sealed", "")
	if sealed == "" {
		respondError(w, http.StatusNotFound, "no encryption passphrase configured")
		return
	}

	if len(h.serverKey) == 0 {
		respondError(w, http.StatusInternalServerError, "server key not available")
		return
	}

	passphrase, err := crypto.Unseal(h.serverKey, sealed)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "unsealing passphrase: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"passphrase": passphrase,
	})
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
