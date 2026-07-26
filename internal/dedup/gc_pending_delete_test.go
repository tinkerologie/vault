package dedup

import (
	"bytes"
	"errors"
	"testing"
)

// deleteFailingAdapter simulates a storage backend that accepts writes but
// fails deletions — a remote that is up but rejecting DELETE, which is exactly
// the window this fix covers.
type deleteFailingAdapter struct {
	*FakeAdapter
	failDelete bool
}

func (a *deleteFailingAdapter) Delete(path string) error {
	if a.failDelete {
		return errors.New("storage delete failed")
	}
	return a.FakeAdapter.Delete(path)
}

// swapAdapter points the repo, its index, and its packer at a replacement.
func swapAdapter(r *Repo, a *deleteFailingAdapter) {
	r.adapter = a
	r.idx.adapter = a
	r.packer.adapter = a
}

// TestGCFailedDeleteHidesChunksFromReuse covers the hazard raised in review.
//
// GC writes a pack's tombstone BEFORE deleting it, so the intent survives a
// crash. If the storage delete then fails, the pack and its chunks used to
// stay in SQLite — and HasDedupChunk kept advertising them. A later backup of
// the same content would dedupe against those chunks instead of writing its
// own pack, producing a manifest that depends on a pack whose tombstone is
// already durable on storage. RebuildFromStorage skips tombstoned packs, so
// repairing the index would drop those chunks and leave that backup
// unrestorable.
//
// Once tombstoned, the chunks must therefore be invisible to dedup reuse even
// though the row survives for the retry.
func TestGCFailedDeleteHidesChunksFromReuse(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	adapter := &deleteFailingAdapter{FakeAdapter: NewFakeAdapter()}
	swapAdapter(r, adapter)

	payload := bytes.Repeat([]byte{0x5A}, 4096)
	id, err := r.Put(payload)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if !r.Has(id) {
		t.Fatal("setup: chunk should be present before GC")
	}

	// No live manifests => the pack is fully dead and GC sweeps it, but the
	// storage delete fails.
	adapter.failDelete = true
	res, err := RunGC(r, nil, GCOptions{})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected the failed storage delete to be reported")
	}

	if r.Has(id) {
		t.Fatal("chunk still advertised for reuse after its pack was tombstoned — " +
			"a later backup would reference a pack that a rebuild will drop")
	}

	// The row must survive so a later GC can retry the delete.
	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 {
		t.Fatalf("got %d pack rows, want 1 retained for retry", len(packs))
	}
	if !packs[0].PendingDelete {
		t.Fatal("pack was not marked pending delete")
	}
}

// TestGCRetryAfterFailedDeleteCompletes confirms the retained row lets a later
// GC finish the job, and that re-running does not append a second tombstone
// for the same pack.
func TestGCRetryAfterFailedDeleteCompletes(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	adapter := &deleteFailingAdapter{FakeAdapter: NewFakeAdapter()}
	swapAdapter(r, adapter)

	if _, err := r.Put(bytes.Repeat([]byte{0x77}, 4096)); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	adapter.failDelete = true
	if _, err := RunGC(r, nil, GCOptions{}); err != nil {
		t.Fatal(err)
	}
	tombstonesAfterFirst := countTombstones(t, adapter.FakeAdapter)
	if tombstonesAfterFirst != 1 {
		t.Fatalf("got %d tombstones after the first sweep, want 1", tombstonesAfterFirst)
	}

	// Storage recovers; the next sweep should finish the delete.
	adapter.failDelete = false
	if _, err := RunGC(r, nil, GCOptions{}); err != nil {
		t.Fatal(err)
	}

	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Fatalf("got %d pack rows after a successful retry, want 0", len(packs))
	}
	if got := countTombstones(t, adapter.FakeAdapter); got != tombstonesAfterFirst {
		t.Fatalf("retry wrote %d extra tombstone(s); the intent was already durable", got-tombstonesAfterFirst)
	}
}

// TestGCTombstonedChunkIsRewrittenByNextBackup shows the practical outcome:
// because the chunks are no longer advertised, the next backup of the same
// content writes its own pack rather than depending on a doomed one.
func TestGCTombstonedChunkIsRewrittenByNextBackup(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	adapter := &deleteFailingAdapter{FakeAdapter: NewFakeAdapter()}
	swapAdapter(r, adapter)

	payload := bytes.Repeat([]byte{0x11}, 4096)
	if _, err := r.Put(payload); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	adapter.failDelete = true
	if _, err := RunGC(r, nil, GCOptions{}); err != nil {
		t.Fatal(err)
	}

	// Same content again: must land in a fresh, un-tombstoned pack.
	if _, err := r.Put(payload); err != nil {
		t.Fatalf("re-Put after tombstone: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("re-Flush: %v", err)
	}

	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	var live int
	for _, p := range packs {
		if !p.PendingDelete {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("got %d live packs, want 1 freshly written for the re-backed-up content", live)
	}
}

func countTombstones(t *testing.T, a *FakeAdapter) int {
	t.Helper()
	entries, err := a.List(indexRootPath)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		rc, err := a.Read(e.Path)
		if err != nil {
			t.Fatal(err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
		if bytes.Contains(buf.Bytes(), []byte(`"tombstone"`)) {
			n++
		}
	}
	return n
}
