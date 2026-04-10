package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const (
	// apiKeyPrefix is prepended to generated API keys for identification.
	apiKeyPrefix = "vault_"
	// apiKeyRandomBytes is the number of random bytes in an API key.
	apiKeyRandomBytes = 32
)

// GenerateAPIKey creates a new random API key with the "vault_" prefix.
func GenerateAPIKey() (string, error) {
	b := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}
