package crypto

import (
	"fmt"
	"io"

	"filippo.io/age"
)

// DecryptReader wraps src (an age-encrypted stream) and returns a reader
// that yields the decrypted plaintext. The caller must close the returned
// ReadCloser when done.
func DecryptReader(passphrase string, src io.Reader) (io.ReadCloser, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("decryption passphrase must not be empty")
	}

	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("creating scrypt identity: %w", err)
	}

	r, err := age.Decrypt(src, identity)
	if err != nil {
		return nil, fmt.Errorf("decrypting data: %w", err)
	}

	return io.NopCloser(r), nil
}
