package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestReclaimDedupAfterJobDelete_PreservesDBBackups pins the #183 contract:
// removing an orphaned dedup repo must delete only the dedup-owned subpaths
// (_vault/repo.json, _vault/packs, _vault/index) — the runner's database
// backups share the _vault directory and must survive.
func TestReclaimDedupAfterJobDelete_PreservesDBBackups(t *testing.T) {
	r, database, dir := setupTestRunner(t)
	dest := createLocalDest(t, database, dir)

	// Lay out a shared _vault directory: dedup repo + DB recovery copies.
	mustWrite := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("_vault/repo.json")
	mustWrite("_vault/packs/000001.pack")
	mustWrite("_vault/index/000001.add")
	mustWrite("_vault/vault.db.2026-07-03T00-00-00.db")
	mustWrite("_vault/vault.db.latest.db")

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)

	var errs []error
	// No jobs reference dest → the "last dedup job deleted" branch runs.
	r.reclaimDedupAfterJobDelete(adapter, 999, dest, &errs)
	for _, e := range errs {
		t.Errorf("reclaim error: %v", e)
	}

	for _, gone := range dedup.RepoSubpaths() {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("dedup path %s should have been removed", gone)
		}
	}
	for _, kept := range []string{"_vault/vault.db.2026-07-03T00-00-00.db", "_vault/vault.db.latest.db"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("DB backup %s was destroyed by dedup reclaim (#183): %v", kept, err)
		}
	}
}
