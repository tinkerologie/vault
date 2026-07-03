package runner

import (
	"io/fs"
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

// Stat reports not-found so the scoped reclaim (#183) treats every subpath as
// a directory and routes it through DeleteStorageDir.
func (a *recordingDedupAdapter) Stat(string) (storage.FileInfo, error) {
	return storage.FileInfo{}, fs.ErrNotExist
}

// TestReclaimDedupAfterJobDeleteLastJobRemovesRepo verifies that when the last
// job on a dedup destination is deleted, the dedup-owned repo paths are removed
// and the destination's dedup index rows are cleared (issue #143) — but ONLY
// the dedup-owned subpaths, never the shared _vault root, which also holds the
// runner's database backups (issue #183).
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
	deleted := map[string]bool{}
	for _, p := range adapter.deleted {
		deleted[p] = true
	}
	for _, sub := range dedup.RepoSubpaths() {
		if !deleted[sub] {
			t.Errorf("Delete(%q) not called; deleted = %v", sub, adapter.deleted)
		}
	}
	if deleted[dedup.RepoRoot] {
		t.Errorf("Delete(%q) was called on the shared root — DB backups would be destroyed (#183)", dedup.RepoRoot)
	}

	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM dedup_packs WHERE storage_id = ?", destID).Scan(&n); err != nil {
		t.Fatalf("count packs: %v", err)
	}
	if n != 0 {
		t.Errorf("dedup_packs rows after last-job delete = %d, want 0", n)
	}
}
