package crypto

import (
	"bytes"
	"io"
	"strings"
	"testing"
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
