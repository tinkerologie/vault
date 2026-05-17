package crypto

import (
	"fmt"
	"io"
	"sync"

	"filippo.io/age"
)

// EncryptReader wraps src in an age-encrypted stream using a passphrase.
// The returned reader streams encrypted data suitable for writing to storage.
// Callers must close it if they stop reading early so the encryption goroutine
// is unblocked and can release its buffers.
func EncryptReader(passphrase string, src io.Reader) (io.ReadCloser, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("encryption passphrase must not be empty")
	}

	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("creating scrypt recipient: %w", err)
	}

	pr, pw := io.Pipe()
	done := make(chan struct{})

	go func() {
		defer close(done)
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
		_ = pw.Close()
	}()

	return &encryptReadCloser{PipeReader: pr, done: done}, nil
}

type encryptReadCloser struct {
	*io.PipeReader
	done chan struct{}
	once sync.Once
}

func (r *encryptReadCloser) Close() error {
	var err error
	r.once.Do(func() {
		err = r.PipeReader.Close()
		<-r.done
	})
	return err
}
