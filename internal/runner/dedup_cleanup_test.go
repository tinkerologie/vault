package runner

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// recordingDedupAdapter records Delete calls and reports empty listings so
// DeleteStorageDir proceeds to remove the prefix itself.
type recordingDedupAdapter struct {
	storage.Adapter
	deleted []string
}

func (a *recordingDedupAdapter) List(string) ([]storage.FileInfo, error) { return nil, nil }
func (a *recordingDedupAdapter) Delete(p string) error {
	a.deleted = append(a.deleted, p)
	return nil
}

// TestReclaimDedupAfterJobDeleteLastJobRemovesRepo verifies that when the last
// job on a dedup destination is deleted, the shared _vault repo is removed and
// the destination's dedup index rows are cleared (issue #143).
func TestReclaimDedupAfterJobDeleteLastJobRemovesRepo(t *testing.T) {
	t.Parallel()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close() //nolint:errcheck

	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "d", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	// Seed a dedup index row; no jobs reference the destination, so the
	// just-deleted job was the last one.
	if _, err := database.Exec(
		`INSERT INTO dedup_packs (id, storage_id, path, size_bytes, chunk_count) VALUES (?,?,?,?,?)`,
		"p1", destID, "_vault/packs/p1", 10, 1); err != nil {
		t.Fatalf("seed pack: %v", err)
	}

	r := &Runner{db: database}
	adapter := &recordingDedupAdapter{}
	var errs []error
	r.reclaimDedupAfterJobDelete(adapter, 42, db.StorageDestination{ID: destID, DedupEnabled: true}, &errs)

	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
	found := false
	for _, p := range adapter.deleted {
		if p == dedup.RepoRoot {
			found = true
		}
	}
	if !found {
		t.Errorf("Delete(%q) not called; deleted = %v", dedup.RepoRoot, adapter.deleted)
	}

	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM dedup_packs WHERE storage_id = ?", destID).Scan(&n); err != nil {
		t.Fatalf("count packs: %v", err)
	}
	if n != 0 {
		t.Errorf("dedup_packs rows after last-job delete = %d, want 0", n)
	}
}
