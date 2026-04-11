package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// ServerKeySize is the size in bytes of the AES-256 server key.
const ServerKeySize = 32

// LoadOrCreateServerKey loads the server key from keyPath. If the file does
// not exist, a new random 32-byte key is generated and written to keyPath
// with mode 0600. The key is used to seal/unseal secrets stored in the DB.
func LoadOrCreateServerKey(keyPath string) ([]byte, error) {
	data, err := os.ReadFile(keyPath) //nolint:gosec // keyPath is from CLI flags at daemon startup — not user input
	if err == nil {
		if len(data) != ServerKeySize {
			return nil, fmt.Errorf("server key at %s has unexpected size %d", keyPath, len(data))
		}
		return data, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading server key: %w", err)
	}

	// Generate a new random key.
	key := make([]byte, ServerKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating server key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating key directory: %w", err)
	}

	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("writing server key: %w", err)
	}

	return key, nil
}

// Seal encrypts plaintext using AES-256-GCM with the given key and returns
// a base64-encoded string containing the nonce prepended to the ciphertext.
func Seal(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Unseal decrypts a base64-encoded ciphertext (nonce + ciphertext) produced
// by Seal, returning the original plaintext.
func Unseal(key []byte, sealed string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("decoding sealed value: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("sealed value too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}

	return string(plaintext), nil
}
