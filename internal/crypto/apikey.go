package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// apiKeyPrefix is prepended to generated API keys for identification.
	apiKeyPrefix = "vault_"
	// AssistantKeyPrefix identifies a scoped key before host-local access checks.
	AssistantKeyPrefix = "vault_ak_"
	// apiKeyRandomBytes is the number of random bytes in an API key.
	apiKeyRandomBytes = 32
)

// GenerateAPIKey creates a new random API key with the "vault_" prefix.
func GenerateAPIKey() (string, error) {
	for {
		key, err := generateKey(apiKeyPrefix)
		if err != nil || !strings.HasPrefix(key, AssistantKeyPrefix) {
			return key, err
		}
	}
}

// GenerateAssistantKey creates an independently scoped random credential.
func GenerateAssistantKey() (string, error) {
	return generateKey(AssistantKeyPrefix)
}

func generateKey(prefix string) (string, error) {
	b := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// AssistantKeyDigest returns a verifier for a canonical Assistant Key.
func AssistantKeyDigest(key string) (string, bool) {
	suffix, ok := strings.CutPrefix(key, AssistantKeyPrefix)
	if !ok || !validKeySuffix(suffix) {
		return "", false
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]), true
}

// IsLegacyAdminKeyWithAssistantPrefix recognizes keys that Vault generated
// before it reserved the Assistant Key prefix.
func IsLegacyAdminKeyWithAssistantPrefix(key string) bool {
	return strings.HasPrefix(key, AssistantKeyPrefix) && validKeySuffix(strings.TrimPrefix(key, apiKeyPrefix))
}

func validKeySuffix(suffix string) bool {
	if len(suffix) != base64.RawURLEncoding.EncodedLen(apiKeyRandomBytes) || strings.ContainsAny(suffix, "\r\n") {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(suffix)
	return err == nil
}
