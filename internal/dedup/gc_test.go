package dedup

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestGCSweepsUnreferenced(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	aPlain := make([]byte, 4096)
	_, _ = rand.Read(aPlain)
	bPlain := make([]byte, 4096)
	_, _ = rand.Read(bPlain)
	aID, err := r.Put(aPlain)
	if err != nil {
		t.Fatal(err)
	}
	bID, err := r.Put(bPlain)
	if err != nil {
		t.Fatal(err)
	}
	mA := Manifest{Version: ManifestVersion, Item: "A", Files: map[string]ManifestEntry{"a.bin": {Chunks: []ID{aID}}}}
	mB := Manifest{Version: ManifestVersion, Item: "B", Files: map[string]ManifestEntry{"b.bin": {Chunks: []ID{bID}}}}
	_, _ = r.PutManifest("A", mA)
	bManifestID, _ := r.PutManifest("B", mB)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// RunGC with only manifest B reachable → A's chunk should be reaped.
	res, err := RunGC(r, []ID{bManifestID})
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedPacks == 0 && res.RewritableBytes == 0 {
		t.Fatalf("expected GC to reclaim or mark space, got %+v", res)
	}
	// B's chunk is still readable.
	if _, err := r.Get(bID); err != nil {
		t.Fatalf("GC removed live chunk B: %v", err)
	}
}

func TestGCConcurrentPutSurvives(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Initial live state: one manifest + chunk.
	livePlain := bytes.Repeat([]byte("live"), 1024)
	liveID, _ := r.Put(livePlain)
	liveManifest := Manifest{Version: ManifestVersion, Item: "live", Files: map[string]ManifestEntry{"l.bin": {Chunks: []ID{liveID}}}}
	liveManifestID, _ := r.PutManifest("live", liveManifest)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent backup: a new chunk + new manifest, registered
	// BEFORE the next RunGC call — both reachable IDs are passed in.
	newPlain := bytes.Repeat([]byte("new"), 1024)
	newID, _ := r.Put(newPlain)
	newManifest := Manifest{Version: ManifestVersion, Item: "new", Files: map[string]ManifestEntry{"n.bin": {Chunks: []ID{newID}}}}
	newManifestID, _ := r.PutManifest("new", newManifest)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	if _, err := RunGC(r, []ID{liveManifestID, newManifestID}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(newID); err != nil {
		t.Fatalf("concurrent put got swept: %v", err)
	}
}

func TestGCUpdatesStats(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	p := make([]byte, 4096)
	_, _ = rand.Read(p)
	id, _ := r.Put(p)
	m := Manifest{Version: ManifestVersion, Item: "x", Files: map[string]ManifestEntry{"x": {Chunks: []ID{id}}}}
	mID, _ := r.PutManifest("x", m)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// No live manifests → everything reaped.
	_ = mID
	res, err := RunGC(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedPacks == 0 {
		t.Fatal("expected packs freed")
	}
	s := r.Stats()
	if s.LastGCAt.IsZero() {
		t.Fatal("LastGCAt not updated")
	}
	if s.LastGCFreedBytes <= 0 {
		t.Fatal("LastGCFreedBytes not updated")
	}
}

func TestGCNoLivePacksAreDeleted(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	p := make([]byte, 4096)
	_, _ = rand.Read(p)
	id, _ := r.Put(p)
	m := Manifest{Version: ManifestVersion, Item: "x", Files: map[string]ManifestEntry{"x": {Chunks: []ID{id}}}}
	mID, _ := r.PutManifest("x", m)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	res, err := RunGC(r, []ID{mID})
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedPacks != 0 || res.FreedBytes != 0 {
		t.Fatalf("expected no frees with all live, got %+v", res)
	}
}

func TestGCStatsVisibleFromFreshRepo(t *testing.T) {
	r, sk, cleanup := newTestRepo(t)
	defer cleanup()
	p := make([]byte, 4096)
	_, _ = rand.Read(p)
	id, _ := r.Put(p)
	m := Manifest{Version: ManifestVersion, Item: "x", Files: map[string]ManifestEntry{"x": {Chunks: []ID{id}}}}
	_, _ = r.PutManifest("x", m)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// GC with the manifest unreferenced → its pack is freed.
	if _, err := RunGC(r, nil); err != nil {
		t.Fatal(err)
	}

	// A DIFFERENT repo instance (simulating the stats-poll path, which opens
	// its own repo) must still see the GC result.
	r2, err := OpenRepo(r.db, r.adapter, r.storageID, sk)
	if err != nil {
		t.Fatal(err)
	}
	s := r2.Stats()
	if s.LastGCAt.IsZero() {
		t.Fatal("LastGCAt zero on fresh repo — GC result not persisted")
	}
	if s.LastGCFreedBytes <= 0 {
		t.Fatalf("LastGCFreedBytes not persisted, got %d", s.LastGCFreedBytes)
	}
}
