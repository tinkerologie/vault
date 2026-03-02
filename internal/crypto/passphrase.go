package crypto

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassphrase hashes a passphrase using bcrypt for verify-only storage.
// The hash can later be used with VerifyPassphrase to check correctness
// without storing the plaintext passphrase.
func HashPassphrase(passphrase string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(passphrase), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing passphrase: %w", err)
	}
	return string(hash), nil
}

// VerifyPassphrase checks whether the given passphrase matches the stored
// bcrypt hash. Returns nil on success, or an error if the passphrase is wrong.
func VerifyPassphrase(passphrase, hash string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(passphrase)); err != nil {
		return fmt.Errorf("passphrase does not match: %w", err)
	}
	return nil
}
