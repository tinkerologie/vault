package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestPruneDBBackups pins the #184 contract: only the newest dbBackupsKept
// timestamped copies survive; the latest pointer and co-located dedup repo
// paths are never touched.
func TestPruneDBBackups(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "_vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, "packs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		name := fmt.Sprintf("vault.db.2026-06-%02dT00-00-00.db", i)
		if err := os.WriteFile(filepath.Join(vaultDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, keep := range []string{"vault.db.latest.db", "repo.json"} {
		if err := os.WriteFile(filepath.Join(vaultDir, keep), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	adapter, err := storage.NewAdapter("local", `{"path":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)

	pruneDBBackups(adapter, "test")

	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	var hist []string
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 9 && e.Name()[:9] == "vault.db." && e.Name() != "vault.db.latest.db" {
			hist = append(hist, e.Name())
		}
	}
	if len(hist) != dbBackupsKept {
		t.Fatalf("kept %d timestamped copies, want %d: %v", len(hist), dbBackupsKept, hist)
	}
	// Oldest survivor must be day 7 (20 copies − 14 kept = 6 pruned).
	for _, name := range hist {
		if name < "vault.db.2026-06-07" {
			t.Errorf("copy older than expected survived: %s", name)
		}
	}
	for _, keep := range []string{"vault.db.latest.db", "repo.json"} {
		if _, err := os.Stat(filepath.Join(vaultDir, keep)); err != nil {
			t.Errorf("%s must never be pruned: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "packs")); err != nil {
		t.Errorf("dedup packs dir must never be pruned: %v", err)
	}
}
