package dedup

import (
	"crypto/rand"
	"strconv"
	"testing"
)

// buildOneMixedPack creates a single pack containing exactly liveN live
// chunks and deadN dead chunks (random 4 KiB payloads each) — small enough
// that all chunks fit in one 24 MiB pack. Returns the live manifest IDs
// (for the RunGC mark) and the live chunk IDs.
func buildOneMixedPack(t *testing.T, r *Repo, liveN, deadN int) (liveMID []ID, liveIDs []ID) {
	t.Helper()
	liveIDs = make([]ID, 0, liveN)
	for i := 0; i < liveN; i++ {
		b := make([]byte, 4096)
		_, _ = rand.Read(b)
		id, err := r.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		liveIDs = append(liveIDs, id)
	}
	deadIDs := make([]ID, 0, deadN)
	for i := 0; i < deadN; i++ {
		b := make([]byte, 4096)
		_, _ = rand.Read(b)
		id, err := r.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		deadIDs = append(deadIDs, id)
	}

	// One manifest references only the live chunks; the dead-chunks manifest
	// is never referenced (and is intentionally dropped from the live set).
	liveFiles := map[string]ManifestEntry{}
	for i, id := range liveIDs {
		liveFiles["live_"+strconv.Itoa(i)] = ManifestEntry{Size: 4096, Chunks: []ID{id}}
	}
	deadFiles := map[string]ManifestEntry{}
	for i, id := range deadIDs {
		deadFiles["dead_"+strconv.Itoa(i)] = ManifestEntry{Size: 4096, Chunks: []ID{id}}
	}
	mLive := Manifest{Version: ManifestVersion, Item: "live", Files: liveFiles}
	mDead := Manifest{Version: ManifestVersion, Item: "dead", Files: deadFiles}
	lmID, err := r.PutManifest("live", mLive)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.PutManifest("dead", mDead)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Sanity: one pack only (so the live + dead chunks are guaranteed mixed).
	if s := r.Stats(); s.TotalPacks != 1 {
		t.Fatalf("test fixture: want exactly one pack, got %d", s.TotalPacks)
	}
	return []ID{lmID}, liveIDs
}

func TestCompactionRepacksAboveThreshold(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	liveM, liveIDs := buildOneMixedPack(t, r, 4, 4)
	before := r.Stats()

	res, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 0.4})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompactedPacks < 1 {
		t.Fatalf("expected at least one pack compacted, got %+v", res)
	}
	if res.ReclaimedBytes <= 0 {
		t.Fatalf("expected positive ReclaimedBytes, got %d", res.ReclaimedBytes)
	}
	after := r.Stats()
	if after.PhysicalBytes >= before.PhysicalBytes {
		t.Fatalf("PhysicalBytes did not drop: before=%d after=%d", before.PhysicalBytes, after.PhysicalBytes)
	}
	for _, id := range liveIDs {
		if _, err := r.Get(id); err != nil {
			t.Fatalf("live chunk %x unreadable after compaction: %v", id[:4], err)
		}
	}
}

func TestCompactionLeavesPackBelowThresholdAlone(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	liveM, _ := buildOneMixedPack(t, r, 4, 4)

	// 50% dead vs threshold 0.9 → no compaction.
	res, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompactedPacks != 0 {
		t.Fatalf("expected no compaction above threshold, got %+v", res)
	}
	if res.RewritableBytes == 0 {
		t.Fatal("expected RewritableBytes > 0 for the untouched mixed pack")
	}
}

func TestCompactionZeroRatioDisables(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	liveM, _ := buildOneMixedPack(t, r, 4, 4)

	res, err := RunGC(r, liveM, GCOptions{}) // zero value = disabled
	if err != nil {
		t.Fatal(err)
	}
	if res.CompactedPacks != 0 || res.ReclaimedBytes != 0 {
		t.Fatalf("expected zero-value GCOptions to disable compaction, got %+v", res)
	}
}

func TestCompactionThresholdOneDisables(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	liveM, _ := buildOneMixedPack(t, r, 4, 4)

	res, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 1.0})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompactedPacks != 0 {
		t.Fatalf("threshold=1.0 should disable compaction, got %+v", res)
	}
}

// TestCompactionSurvivesRebuild proves the DR path: repack → DB wipe →
// RebuildFromStorage → fresh OpenRepo → every moved chunk still readable
// AND the old pack is actually gone (rebuild applied the tombstone).
// This is the test the tombstone + REPLACE work in Task 0 was designed
// to enable. Without those changes, rebuild would either resurrect the
// old pack (chunks pointing at a deleted blob) or fail to record the
// move (chunks pointing at the old pack).
func TestCompactionSurvivesRebuild(t *testing.T) {
	r, sk, cleanup := newTestRepo(t)
	defer cleanup()
	liveM, liveIDs := buildOneMixedPack(t, r, 4, 4)

	// Capture the pre-compaction pack so we can assert it's gone post-rebuild.
	beforePacks, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforePacks) != 1 {
		t.Fatalf("test fixture: want 1 pack, got %d", len(beforePacks))
	}
	oldPackID := beforePacks[0].ID

	if _, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 0.4}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.DropDedupState(r.storageID); err != nil {
		t.Fatal(err)
	}
	idx := NewIndex(r.db, r.adapter, r.storageID)
	if err := idx.RebuildFromStorage(); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenRepo(r.db, r.adapter, r.storageID, sk)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range liveIDs {
		got, err := r2.Get(id)
		if err != nil {
			t.Fatalf("live chunk %x unreadable after compaction+rebuild: %v", id[:4], err)
		}
		if len(got) != 4096 {
			t.Fatalf("chunk %x has wrong size %d after rebuild", id[:4], len(got))
		}
	}

	// The original pack row must NOT come back from rebuild (proves the
	// tombstone was applied), and at least one fresh pack must exist (the
	// compaction target).
	afterPacks, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range afterPacks {
		if p.ID == oldPackID {
			t.Fatalf("old pack %s resurrected after compaction+rebuild", oldPackID)
		}
	}
	if len(afterPacks) == 0 {
		t.Fatal("expected at least one new pack after compaction+rebuild")
	}
}

// TestCompactionPreservesOldPackOnReadFailure proves the partial-failure
// invariant: when ReadRange fails for chunks in pack P, P is preserved
// (not deleted) so a future GC retry can attempt the move again. We
// inject the failure by deleting the on-storage data-pack blob while
// leaving the DB rows pointing at it — every ReadRange against the
// missing path then fails on the FakeAdapter. The manifest is placed
// in a separate, surviving pack so the mark phase can still read it.
func TestCompactionPreservesOldPackOnReadFailure(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Stage data-chunks into one pack, flush to seal it, then write the
	// manifest into a second pack. This guarantees the manifest survives
	// when we later nuke the data pack's blob.
	liveIDs := make([]ID, 4)
	for i := 0; i < 4; i++ {
		b := make([]byte, 4096)
		_, _ = rand.Read(b)
		id, err := r.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		liveIDs[i] = id
	}
	deadIDs := make([]ID, 4)
	for i := 0; i < 4; i++ {
		b := make([]byte, 4096)
		_, _ = rand.Read(b)
		id, err := r.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		deadIDs[i] = id
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// At this point pack #1 holds live+dead data chunks; manifest goes
	// into pack #2.
	liveFiles := map[string]ManifestEntry{}
	for i, id := range liveIDs {
		liveFiles["live_"+strconv.Itoa(i)] = ManifestEntry{Size: 4096, Chunks: []ID{id}}
	}
	mLive := Manifest{Version: ManifestVersion, Item: "live", Files: liveFiles}
	lmID, err := r.PutManifest("live", mLive)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Identify the data pack (the one whose chunks include liveIDs[0]).
	dataPackPath, _, _, err := r.idx.Locate(liveIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	var dataPackID string
	allPacks, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range allPacks {
		if p.Path == dataPackPath {
			dataPackID = p.ID
			break
		}
	}
	if dataPackID == "" {
		t.Fatal("could not identify data pack")
	}

	// Yank the data blob from storage — DB still says it lives here.
	if err := r.adapter.Delete(dataPackPath); err != nil {
		t.Fatal(err)
	}

	res, err := RunGC(r, []ID{lmID}, GCOptions{CompactMinDeadRatio: 0.4})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompactedPacks != 0 {
		t.Fatalf("expected no pack fully drained on read failure, got %+v", res)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected per-chunk read errors recorded, got none")
	}

	// DB row for the data pack must still be present: future GC retry needs it.
	stillPresent := false
	postPacks, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range postPacks {
		if p.ID == dataPackID {
			stillPresent = true
			break
		}
	}
	if !stillPresent {
		t.Fatal("data pack row was deleted despite no chunks moving — retry path broken")
	}
}
