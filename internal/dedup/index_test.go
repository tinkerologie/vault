package dedup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func newTestIndex(t *testing.T) (*Index, *fakeAdapter, *db.DB, int64, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a := newFakeAdapter()
	idx := NewIndex(d, a, destID)
	cleanup := func() { d.Close(); os.RemoveAll(dir) }
	return idx, a, d, destID, cleanup
}

func TestIndexRegisterAndLocate(t *testing.T) {
	idx, _, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	id := ID{0x01}
	info := PackInfo{
		ID: "abc123", Path: "_vault/packs/ab/abc123",
		SizeBytes: 100, ChunkCount: 1,
		Entries: []PackEntry{{ID: id, Offset: 0, Length: 50, Flags: 0}},
	}
	if err := idx.Register(info); err != nil {
		t.Fatal(err)
	}
	if !idx.Has(id) {
		t.Fatal("Has(id) false after Register")
	}
	path, offset, length, err := idx.Locate(id)
	if err != nil {
		t.Fatal(err)
	}
	if path != info.Path || offset != 0 || length != 50 {
		t.Fatalf("Locate returned (%q, %d, %d)", path, offset, length)
	}
}

func TestIndexRegisterIdempotent(t *testing.T) {
	idx, _, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	id := ID{0x02}
	info := PackInfo{
		ID: "p1", Path: "_vault/packs/p1/p1", SizeBytes: 10, ChunkCount: 1,
		Entries: []PackEntry{{ID: id, Offset: 0, Length: 10}},
	}
	if err := idx.Register(info); err != nil {
		t.Fatal(err)
	}
	if err := idx.Register(info); err != nil {
		t.Fatalf("second register failed: %v", err)
	}
}

func TestIndexAppendStorageIndexSequencing(t *testing.T) {
	idx, a, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	info := PackInfo{ID: "p1", Path: "_vault/packs/p1/p1", SizeBytes: 10, ChunkCount: 1, Entries: []PackEntry{{ID: ID{0x03}, Offset: 0, Length: 10}}}
	if err := idx.AppendStorageIndex(info); err != nil {
		t.Fatal(err)
	}
	if err := idx.AppendStorageIndex(info); err != nil {
		t.Fatal(err)
	}
	// Two sequenced files should exist under _vault/index/
	listing, err := a.List("_vault/index")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing) != 2 {
		t.Fatalf("expected 2 index blobs, got %d", len(listing))
	}
}

func TestIndexRebuildFromStorage(t *testing.T) {
	idx, a, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	id := ID{0xff}
	info := PackInfo{
		ID: "pack1", Path: "_vault/packs/pa/pack1",
		SizeBytes: 99, ChunkCount: 1,
		Entries: []PackEntry{{ID: id, Offset: 0, Length: 99}},
	}
	// Write a fake pack body so any Stat-based size check works; Rebuild only
	// reads the JSONL blobs, not the pack footers themselves.
	_ = a.Write(info.Path, bytes.NewReader(make([]byte, info.SizeBytes)))
	if err := idx.AppendStorageIndex(info); err != nil {
		t.Fatal(err)
	}
	if err := idx.Register(info); err != nil {
		t.Fatal(err)
	}

	// Wipe DB state, then rebuild from storage.
	if err := idx.dropDBState(); err != nil {
		t.Fatal(err)
	}
	if idx.Has(id) {
		t.Fatal("Has(id) still true after dropDBState")
	}
	if err := idx.RebuildFromStorage(); err != nil {
		t.Fatal(err)
	}
	if !idx.Has(id) {
		t.Fatal("Has(id) false after RebuildFromStorage")
	}
}
