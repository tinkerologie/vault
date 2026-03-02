package crypto

import (
	"fmt"
	"io"

	"filippo.io/age"
)

// EncryptReader wraps src in an age-encrypted stream using a passphrase.
// The returned io.Reader streams encrypted data suitable for writing to
// storage. The caller must read the returned reader to completion.
func EncryptReader(passphrase string, src io.Reader) (io.Reader, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("encryption passphrase must not be empty")
	}

	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("creating scrypt recipient: %w", err)
	}

	pr, pw := io.Pipe()

	go func() {
		w, err := age.Encrypt(pw, recipient)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("starting age encryption: %w", err))
			return
		}
		if _, err := io.Copy(w, src); err != nil {
			_ = w.Close()
			pw.CloseWithError(fmt.Errorf("encrypting data: %w", err))
			return
		}
		if err := w.Close(); err != nil {
			pw.CloseWithError(fmt.Errorf("finalizing encryption: %w", err))
			return
		}
		pw.Close()
	}()

	return pr, nil
}
