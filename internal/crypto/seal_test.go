package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateServerKey(t *testing.T) {
	t.Parallel()

	t.Run("creates new key", func(t *testing.T) {
		t.Parallel()
		keyPath := filepath.Join(t.TempDir(), "vault.key")
		key, err := LoadOrCreateServerKey(keyPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(key) != ServerKeySize {
			t.Fatalf("key size = %d, want %d", len(key), ServerKeySize)
		}
		// Verify file was written.
		data, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("reading key file: %v", err)
		}
		if len(data) != ServerKeySize {
			t.Fatalf("file size = %d, want %d", len(data), ServerKeySize)
		}
	})

	t.Run("loads existing key", func(t *testing.T) {
		t.Parallel()
		keyPath := filepath.Join(t.TempDir(), "vault.key")
		key1, _ := LoadOrCreateServerKey(keyPath)
		key2, err := LoadOrCreateServerKey(keyPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(key1) != string(key2) {
			t.Fatal("keys differ on second load")
		}
	})

	t.Run("rejects wrong size", func(t *testing.T) {
		t.Parallel()
		keyPath := filepath.Join(t.TempDir(), "vault.key")
		os.WriteFile(keyPath, []byte("short"), 0o600)
		_, err := LoadOrCreateServerKey(keyPath)
		if err == nil {
			t.Fatal("expected error for wrong-size key")
		}
	})
}

func TestSealUnseal(t *testing.T) {
	t.Parallel()

	key := make([]byte, ServerKeySize)
	for i := range key {
		key[i] = byte(i)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{"short passphrase", "hunter2"},
		{"long passphrase", "this is a much longer passphrase with special chars: !@#$%^&*()"},
		{"empty string", ""},
		{"unicode", "日本語のパスフレーズ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sealed, err := Seal(key, tt.plaintext)
			if err != nil {
				t.Fatalf("Seal error: %v", err)
			}
			if sealed == tt.plaintext {
				t.Fatal("sealed value should not equal plaintext")
			}

			got, err := Unseal(key, sealed)
			if err != nil {
				t.Fatalf("Unseal error: %v", err)
			}
			if got != tt.plaintext {
				t.Errorf("Unseal() = %q, want %q", got, tt.plaintext)
			}
		})
	}
}

func TestUnsealWrongKey(t *testing.T) {
	t.Parallel()

	key1 := make([]byte, ServerKeySize)
	key2 := make([]byte, ServerKeySize)
	key2[0] = 1

	sealed, err := Seal(key1, "secret")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Unseal(key2, sealed)
	if err == nil {
		t.Fatal("expected error with wrong key")
	}
}

func TestUnsealInvalidInput(t *testing.T) {
	t.Parallel()

	key := make([]byte, ServerKeySize)

	tests := []struct {
		name  string
		input string
	}{
		{"not base64", "not-valid-base64!!!"},
		{"too short", "AAAA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Unseal(key, tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
