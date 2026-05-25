package dedup

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// TestDisasterRecoveryRebuildFromStorage proves the DR guarantee behind
// `vault dedup repair`: with the SQLite index gone but the on-storage packs +
// index blobs + key intact, a destination's chunks and manifests are fully
// recoverable.
func TestDisasterRecoveryRebuildFromStorage(t *testing.T) {
	r, sk, cleanup := newTestRepo(t)
	defer cleanup()

	// 1. Store real data + a manifest, then flush so packs + index blobs land
	//    on the (fake) storage adapter.
	plain := make([]byte, 8192)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	chunkID, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: ManifestVersion, Item: "dr", Files: map[string]ManifestEntry{"f.bin": {Size: 8192, Chunks: []ID{chunkID}}}}
	manifestID, err := r.PutManifest("dr", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// 2. Simulate total DB loss: wipe all SQLite dedup rows for the dest.
	if err := r.db.DropDedupState(r.storageID); err != nil {
		t.Fatal(err)
	}
	if r.idx.Has(chunkID) {
		t.Fatal("chunk still indexed after DropDedupState")
	}

	// 3. Rebuild the index purely from on-storage blobs.
	idx := NewIndex(r.db, r.adapter, r.storageID)
	if err := idx.RebuildFromStorage(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	// 4. A fresh repo (key + storage only) reads the original data + manifest.
	r2, err := OpenRepo(r.db, r.adapter, r.storageID, sk)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get(chunkID)
	if err != nil {
		t.Fatalf("chunk unreadable after rebuild: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("chunk bytes mismatch after rebuild")
	}
	gotM, err := r2.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("manifest unreadable after rebuild: %v", err)
	}
	if _, ok := gotM.Files["f.bin"]; !ok {
		t.Fatal("manifest contents lost after rebuild")
	}
	if gotM.Item != "dr" {
		t.Fatalf("manifest Item not recovered: got %q", gotM.Item)
	}
}
