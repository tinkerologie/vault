package db

import (
	"testing"
	"time"
)

// TestDeleteDedupState verifies that clearing a destination's dedup index rows
// removes only that destination's packs/chunks/gc-runs, leaving other
// destinations untouched (issue #143: removing the shared _vault repo when the
// last dedup job on a destination is deleted).
func TestDeleteDedupState(t *testing.T) {
	d := setupTestDB(t)

	id1, err := d.CreateStorageDestination(StorageDestination{Name: "a", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatalf("create dest a: %v", err)
	}
	id2, err := d.CreateStorageDestination(StorageDestination{Name: "b", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatalf("create dest b: %v", err)
	}

	seed := func(id int64, tag string) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO dedup_packs (id, storage_id, path, size_bytes, chunk_count) VALUES (?,?,?,?,?)`,
			"pack-"+tag, id, "_vault/packs/"+tag, 10, 1); err != nil {
			t.Fatalf("seed pack %s: %v", tag, err)
		}
		if _, err := d.Exec(`INSERT INTO dedup_chunks (chunk_id, storage_id, pack_id, offset, length) VALUES (?,?,?,?,?)`,
			[]byte("chunk-"+tag), id, "pack-"+tag, 0, 10); err != nil {
			t.Fatalf("seed chunk %s: %v", tag, err)
		}
		if _, err := d.Exec(`INSERT INTO dedup_gc_runs (storage_id, started_at, completed_at) VALUES (?,?,?)`,
			id, time.Now(), time.Now()); err != nil {
			t.Fatalf("seed gc_run %s: %v", tag, err)
		}
	}
	seed(id1, "one")
	seed(id2, "two")

	if err := d.DeleteDedupState(id1); err != nil {
		t.Fatalf("DeleteDedupState: %v", err)
	}

	count := func(table string, id int64) int {
		t.Helper()
		var n int
		if err := d.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE storage_id = ?", id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	for _, tbl := range []string{"dedup_packs", "dedup_chunks", "dedup_gc_runs"} {
		if got := count(tbl, id1); got != 0 {
			t.Errorf("%s rows for deleted dest = %d, want 0", tbl, got)
		}
		if got := count(tbl, id2); got != 1 {
			t.Errorf("%s rows for other dest = %d, want 1 (must not be touched)", tbl, got)
		}
	}
}
