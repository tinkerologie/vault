package crypto

import (
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	t.Parallel()

	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey() error = %v", err)
	}

	if !strings.HasPrefix(key, "vault_") {
		t.Errorf("expected prefix vault_, got %q", key)
	}

	// Verify uniqueness (sanity check).
	key2, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey() second call error = %v", err)
	}
	if key == key2 {
		t.Error("two generated keys should not be identical")
	}
}
