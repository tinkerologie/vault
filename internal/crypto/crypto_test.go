package crypto

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		passphrase string
		plaintext  string
	}{
		{"simple text", "my-secret-passphrase", "hello world"},
		{"empty content", "pass123", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := strings.NewReader(tt.plaintext)
			encrypted, err := EncryptReader(tt.passphrase, src)
			if err != nil {
				t.Fatalf("EncryptReader() error = %v", err)
			}
			defer encrypted.Close()
			ciphertext, err := io.ReadAll(encrypted)
			if err != nil {
				t.Fatalf("reading encrypted data: %v", err)
			}
			if tt.plaintext != "" && bytes.Equal(ciphertext, []byte(tt.plaintext)) {
				t.Error("ciphertext equals plaintext")
			}
			decrypted, err := DecryptReader(tt.passphrase, bytes.NewReader(ciphertext))
			if err != nil {
				t.Fatalf("DecryptReader() error = %v", err)
			}
			defer decrypted.Close()
			got, err := io.ReadAll(decrypted)
			if err != nil {
				t.Fatalf("reading decrypted data: %v", err)
			}
			if string(got) != tt.plaintext {
				t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(tt.plaintext))
			}
		})
	}
}

func TestEncryptReaderEmptyPassphrase(t *testing.T) {
	_, err := EncryptReader("", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestDecryptReaderEmptyPassphrase(t *testing.T) {
	_, err := DecryptReader("", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestDecryptReaderWrongPassphrase(t *testing.T) {
	encrypted, err := EncryptReader("correct-pass", strings.NewReader("secret data"))
	if err != nil {
		t.Fatalf("EncryptReader() error = %v", err)
	}
	defer encrypted.Close()
	ciphertext, err := io.ReadAll(encrypted)
	if err != nil {
		t.Fatalf("reading encrypted: %v", err)
	}
	_, err = DecryptReader("wrong-pass", bytes.NewReader(ciphertext))
	if err == nil {
		t.Fatal("expected error for wrong passphrase")
	}
}

func TestHashAndVerifyPassphrase(t *testing.T) {
	passphrase := "my-backup-passphrase"
	hash, err := HashPassphrase(passphrase)
	if err != nil {
		t.Fatalf("HashPassphrase() error = %v", err)
	}
	if hash == passphrase {
		t.Error("hash should not equal plaintext passphrase")
	}
	if err := VerifyPassphrase(passphrase, hash); err != nil {
		t.Errorf("VerifyPassphrase() correct pass: %v", err)
	}
	if err := VerifyPassphrase("wrong-pass", hash); err == nil {
		t.Error("VerifyPassphrase() should fail with wrong passphrase")
	}
}

// failingReader returns an error after returning some initial data so the
// goroutine inside EncryptReader hits its io.Copy error branch.
type failingReader struct{ err error }

func (f *failingReader) Read(p []byte) (int, error) { return 0, f.err }

func TestEncryptReaderSourceError(t *testing.T) {
	r, err := EncryptReader("pw", &failingReader{err: io.ErrUnexpectedEOF})
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}
	_, err = io.ReadAll(r)
	if err == nil {
		t.Error("expected pipe error from failing source reader")
	}
	_ = r.Close()
}

func TestEncryptReaderCloseUnblocksGoroutine(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 10*1024*1024))
	encrypted, err := EncryptReader("pw", src)
	if err != nil {
		t.Fatalf("EncryptReader() error = %v", err)
	}

	buf := make([]byte, 64)
	if _, err := encrypted.Read(buf); err != nil {
		t.Fatalf("initial encrypted read: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- encrypted.Close() }()
	select {
	case err := <-closed:
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close() did not unblock encryption goroutine")
	}
}

func TestHashPassphraseTooLong(t *testing.T) {
	// bcrypt rejects passphrases longer than 72 bytes.
	long := strings.Repeat("a", 73)
	if _, err := HashPassphrase(long); err == nil {
		t.Error("expected bcrypt to reject >72-byte passphrase")
	}
}

func TestSealBadKeySize(t *testing.T) {
	if _, err := Seal([]byte("short"), "data"); err == nil {
		t.Error("expected error for invalid key size")
	}
}

func TestUnsealBadKeySize(t *testing.T) {
	if _, err := Unseal([]byte("short"), "anything"); err == nil {
		t.Error("expected error for invalid key size")
	}
}

func TestUnsealInvalidBase64(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	if _, err := Unseal(key, "not-valid-base64!!!"); err == nil {
		t.Error("expected base64 decode error")
	}
}

func TestUnsealTooShort(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	// Valid base64 but only 4 bytes — shorter than the 12-byte GCM nonce.
	if _, err := Unseal(key, "AAAA"); err == nil {
		t.Error("expected too-short error")
	}
}
