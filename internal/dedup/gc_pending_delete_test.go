package dedup

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
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
	if packs[0].DeleteState == db.PackLive {
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
		if p.DeleteState == db.PackLive {
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

// TestGCRewrittenChunkRepointsAwayFromDoomedPack covers the mapping half of
// the fix.
//
// Hiding a doomed pack's chunks makes the packer rewrite the content into a
// fresh pack, but chunk rows are keyed on (storage_id, chunk_id) and were
// registered with INSERT OR IGNORE — so the mapping stayed on the doomed
// pack. When a later sweep finally deleted it, the cascade took the only
// mapping with it and the backup that wrote the replacement became
// unrestorable. The row must follow the content to the new pack.
func TestGCRewrittenChunkRepointsAwayFromDoomedPack(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	adapter := &deleteFailingAdapter{FakeAdapter: NewFakeAdapter()}
	swapAdapter(r, adapter)

	payload := bytes.Repeat([]byte{0x33}, 4096)
	id, err := r.Put(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	doomedPath, _, _, err := r.idx.Locate(id)
	if err != nil {
		t.Fatal(err)
	}

	adapter.failDelete = true
	if _, err := RunGC(r, nil, GCOptions{}); err != nil {
		t.Fatal(err)
	}

	// Re-backup the same content: it lands in a fresh pack.
	if _, err := r.Put(payload); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	newPath, _, _, err := r.idx.Locate(id)
	if err != nil {
		t.Fatalf("chunk has no mapping after being rewritten: %v", err)
	}
	if newPath == doomedPath {
		t.Fatal("chunk still mapped to the tombstoned pack; deleting it would strand the new backup")
	}

	// Now let the delete succeed, with a manifest keeping the chunk live. The
	// doomed pack's cascade must not take the live mapping with it.
	mID, err := r.PutManifest("item", Manifest{
		Version: ManifestVersion, Item: "item",
		Files: map[string]ManifestEntry{"f.bin": {Chunks: []ID{id}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	adapter.failDelete = false
	if _, err := RunGC(r, []ID{mID}, GCOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(id)
	if err != nil {
		t.Fatalf("chunk unreadable after the doomed pack was collected: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("chunk content changed after the doomed pack was collected")
	}
}

// notFoundDeleteAdapter reports fs.ErrNotExist for deletes, as the local,
// SFTP, and SMB adapters do when the blob is already gone.
type notFoundDeleteAdapter struct{ *FakeAdapter }

func (a *notFoundDeleteAdapter) Delete(string) error { return fs.ErrNotExist }

// TestGCAlreadyMissingBlobCompletesDeletion guards the wedge that follows a
// successful storage delete whose DB delete failed: every later sweep re-issues
// the delete, the adapter reports not-found, and the row is never reached — so
// the pack stays pending forever, inflating stats and erroring on every run.
// An already-absent blob must count as deleted.
func TestGCAlreadyMissingBlobCompletesDeletion(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	base := NewFakeAdapter()
	swapAdapter(r, &deleteFailingAdapter{FakeAdapter: base})

	if _, err := r.Put(bytes.Repeat([]byte{0x44}, 4096)); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Blob already gone (a prior sweep removed it before failing on the row).
	missing := &notFoundDeleteAdapter{FakeAdapter: base}
	r.adapter = missing
	r.idx.adapter = missing
	r.packer.adapter = missing

	res, err := RunGC(r, nil, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Errors {
		if strings.Contains(e, "_vault/packs/") {
			t.Fatalf("an already-missing blob was reported as a delete failure: %s", e)
		}
	}
	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Fatalf("got %d pack rows, want 0 — deletion wedged on a missing blob", len(packs))
	}
}

// TestGCCrashBetweenMarkAndTombstoneHidesChunks covers the ordering boundary.
//
// The tombstone is durable the instant it lands on storage, and
// RebuildFromStorage suppresses tombstoned packs. If the row were marked only
// AFTER the tombstone write, a crash in between would leave SQLite advertising
// chunks that a repair will drop — so a backup taken before the next GC would
// be unrestorable. Marking first means the crash window contains no such state.
//
// Simulated by failing the tombstone write, which halts the sweep at exactly
// the point a crash would.
func TestGCCrashBetweenMarkAndTombstoneHidesChunks(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	base := NewFakeAdapter()
	swapAdapter(r, &deleteFailingAdapter{FakeAdapter: base})

	id, err := r.Put(bytes.Repeat([]byte{0x66}, 4096))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Writes to the index directory now fail, so the tombstone never lands.
	blocked := &indexWriteBlockingAdapter{FakeAdapter: base}
	r.adapter = blocked
	r.idx.adapter = blocked
	r.packer.adapter = blocked

	if _, err := RunGC(r, nil, GCOptions{}); err != nil {
		t.Fatal(err)
	}

	if r.Has(id) {
		t.Fatal("chunks still advertised after the sweep began retiring the pack — " +
			"a crash here would let a backup depend on a pack about to be tombstoned")
	}
	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].DeleteState != db.PackMarked {
		t.Fatalf("pack state = %v, want exactly one pack at PackMarked", packs)
	}
	if got := countTombstones(t, base); got != 0 {
		t.Fatalf("got %d tombstones, want 0 — the write was meant to fail", got)
	}
}

// indexWriteBlockingAdapter fails writes under the index prefix only, so a
// test can halt the sweep precisely between marking and tombstoning.
type indexWriteBlockingAdapter struct{ *FakeAdapter }

func (a *indexWriteBlockingAdapter) Write(path string, r io.Reader) error {
	if strings.HasPrefix(path, indexRootPath) {
		return errors.New("index write blocked")
	}
	return a.FakeAdapter.Write(path, r)
}
