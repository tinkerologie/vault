package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

func TestTrimLineEnding(t *testing.T) {
	for in, want := range map[string]string{
		"secret":     "secret",
		"secret\n":   "secret",
		"secret\r\n": "secret",
		"secret\n\n": "secret\n",
		"secret\r":   "secret\r",
		"":           "",
	} {
		if got := trimLineEnding(in); got != want {
			t.Errorf("trimLineEnding(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDedupRepairExplainsKeyMismatch checks `vault dedup` points the user at
// the original vault.key when --key does not match the destination (#451).
func TestDedupRepairExplainsKeyMismatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: string(cfg), DedupEnabled: true})
	adapter, _ := storage.NewAdapter("local", string(cfg))
	if _, err := dedup.InitRepo(d, adapter, destID, bytes.Repeat([]byte{0x01}, crypto.ServerKeySize)); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	wrongKey := filepath.Join(dir, "other.key")
	if err := os.WriteFile(wrongKey, bytes.Repeat([]byte{0x02}, crypto.ServerKeySize), 0o600); err != nil {
		t.Fatal(err)
	}

	prev := dedupDestID
	dedupDestID = destID
	t.Cleanup(func() { dedupDestID = prev })
	_, cleanup, err := openDedupContext(dbPath, wrongKey)
	if cleanup != nil {
		cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "is not the vault.key this destination was created with") {
		t.Fatalf("err = %v", err)
	}
}
