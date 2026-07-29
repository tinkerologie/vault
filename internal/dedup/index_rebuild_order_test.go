package dedup

import (
	"bytes"
	"encoding/json"
	"testing"
)

// addEntry builds the JSONL add record AppendStorageIndex would write for info.
func addEntry(info PackInfo) indexEntry {
	return indexEntry{
		PackID: info.ID, PackPath: info.Path,
		SizeBytes: info.SizeBytes, ChunkCount: info.ChunkCount,
		Chunks: info.Entries,
	}
}

// writeRawIndexBlob writes an index blob under an explicit name so a test can
// control replay order directly, independent of writer IDs and sequencing.
func writeRawIndexBlob(t *testing.T, a *FakeAdapter, name string, e indexEntry) {
	t.Helper()
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	if err := a.Write(indexRootPath+"/"+name, bytes.NewReader(line)); err != nil {
		t.Fatal(err)
	}
}

// TestRebuildSkipsAddsForTombstonedPacks is the regression guard for the
// compaction cascade.
//
// Compaction copies chunk C out of dying pack X into new pack Y, then
// tombstones X. Chunk rows are one-per-(storage_id, chunk_id) and
// registerForRebuild uses REPLACE, so replaying add(Y) then add(X) leaves C
// pointing at X — and applying tombstone(X) as a DELETE then cascades C away
// even though live pack Y still holds it. Restores fail after a repair.
//
// The blob names below force exactly that order: Y's add sorts first, X's add
// second, X's tombstone last.
func TestRebuildSkipsAddsForTombstonedPacks(t *testing.T) {
	idx, a, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	shared := ID{0xC0, 0xDE}
	packY := PackInfo{ID: "packY", Path: "_vault/packs/pa/packY", SizeBytes: 20, ChunkCount: 1,
		Entries: []PackEntry{{ID: shared, Offset: 5, Length: 20}}}
	packX := PackInfo{ID: "packX", Path: "_vault/packs/pa/packX", SizeBytes: 10, ChunkCount: 1,
		Entries: []PackEntry{{ID: shared, Offset: 0, Length: 10}}}

	writeRawIndexBlob(t, a, "0000000001-aaaa.idx", addEntry(packY))
	writeRawIndexBlob(t, a, "0000000002-bbbb.idx", addEntry(packX))
	writeRawIndexBlob(t, a, "0000000003-cccc.idx", indexEntry{Tombstone: "packX"})

	if err := idx.RebuildFromStorage(); err != nil {
		t.Fatalf("RebuildFromStorage: %v", err)
	}

	// The chunk must survive, resolving to the live pack.
	path, offset, length, err := idx.Locate(shared)
	if err != nil {
		t.Fatalf("chunk was cascaded away by the tombstone: %v", err)
	}
	if path != packY.Path {
		t.Fatalf("chunk resolves to %q, want the surviving pack %q", path, packY.Path)
	}
	if offset != 5 || length != 20 {
		t.Fatalf("chunk resolves to offset=%d length=%d, want packY's 5/20", offset, length)
	}
}

// TestRebuildIsOrderIndependent replays the same three records with the
// tombstone sorting FIRST — which is reachable because two Index instances can
// hand out equal sequence numbers, leaving the random writerID to break the
// tie. The outcome must be identical to the in-order replay above.
func TestRebuildIsOrderIndependent(t *testing.T) {
	idx, a, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	shared := ID{0xC0, 0xDE}
	packY := PackInfo{ID: "packY", Path: "_vault/packs/pa/packY", SizeBytes: 20, ChunkCount: 1,
		Entries: []PackEntry{{ID: shared, Offset: 5, Length: 20}}}
	packX := PackInfo{ID: "packX", Path: "_vault/packs/pa/packX", SizeBytes: 10, ChunkCount: 1,
		Entries: []PackEntry{{ID: shared, Offset: 0, Length: 10}}}

	// Same sequence number for all three; writerID decides sort order.
	writeRawIndexBlob(t, a, "0000000007-0000.idx", indexEntry{Tombstone: "packX"})
	writeRawIndexBlob(t, a, "0000000007-5555.idx", addEntry(packX))
	writeRawIndexBlob(t, a, "0000000007-9999.idx", addEntry(packY))

	if err := idx.RebuildFromStorage(); err != nil {
		t.Fatalf("RebuildFromStorage: %v", err)
	}

	path, _, _, err := idx.Locate(shared)
	if err != nil {
		t.Fatalf("chunk lost when the tombstone replayed before its add: %v", err)
	}
	if path != packY.Path {
		t.Fatalf("chunk resolves to %q, want the surviving pack %q", path, packY.Path)
	}

	// The tombstoned pack must not have been resurrected.
	packs, err := idx.db.ListDedupPacks(idx.storageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packs {
		if p.ID == "packX" {
			t.Fatal("tombstoned pack packX was resurrected by an out-of-order add")
		}
	}
}
