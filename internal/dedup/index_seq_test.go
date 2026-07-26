package dedup

import (
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// listCountingAdapter counts List calls so a test can assert that appending
// index blobs does not re-scan the index directory every time.
type listCountingAdapter struct {
	*FakeAdapter
	lists int
}

func (l *listCountingAdapter) List(prefix string) ([]storage.FileInfo, error) {
	l.lists++
	return l.FakeAdapter.List(prefix)
}

// TestIndexAppendListsDirectoryOnce is the regression guard for issue #256.
//
// nextIndexSeq used to call adapter.List on EVERY append, so a backup that
// flushed N packs performed N listings of a directory holding N entries —
// quadratic remote I/O. A 500 GB folder flushes ~21k packs (PackTargetSize
// is 24 MiB), which degraded until the daemon appeared frozen. The directory
// must now be scanned once per Index to seed the counter.
func TestIndexAppendListsDirectoryOnce(t *testing.T) {
	idx, fake, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	counter := &listCountingAdapter{FakeAdapter: fake}
	idx.adapter = counter

	const appends = 50
	for i := 0; i < appends; i++ {
		info := PackInfo{ID: "p", Path: "_vault/packs/p/p", SizeBytes: 1, ChunkCount: 0}
		if err := idx.AppendStorageIndex(info); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if counter.lists > 1 {
		t.Fatalf("nextIndexSeq listed the index directory %d times for %d appends; want 1", counter.lists, appends)
	}
	listing, err := fake.List(indexRootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing) != appends {
		t.Fatalf("got %d index blobs, want %d — sequence numbers collided", len(listing), appends)
	}
}

// TestIndexSeqSeedsFromExistingBlobs confirms a new Index continues the
// sequence already on storage rather than restarting at 1 and overwriting
// history, and that the legacy "%010d.idx" name (written before writer IDs
// existed) still parses.
func TestIndexSeqSeedsFromExistingBlobs(t *testing.T) {
	idx, fake, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	// Legacy-format blob at sequence 41, as written by older versions.
	if err := fake.Write(indexRootPath+"/0000000041.idx", strings.NewReader("{}\n")); err != nil {
		t.Fatal(err)
	}
	seq, err := idx.nextIndexSeq()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 42 {
		t.Fatalf("seeded sequence = %d, want 42", seq)
	}
	if next, _ := idx.nextIndexSeq(); next != 43 {
		t.Fatalf("second sequence = %d, want 43", next)
	}
}

// TestIndexTwoWritersDoNotCollide guards the trade-off introduced by caching
// the sequence counter: two Index instances against one destination (e.g. a
// backup and a GC compaction) now each hold their own counter, so they will
// hand out the same sequence number. Per-instance writer IDs in the blob name
// keep those writes distinct — without them the caching would turn a narrow
// pre-existing race into sustained overwriting, losing chunk locations on a
// later RebuildFromStorage.
func TestIndexTwoWritersDoNotCollide(t *testing.T) {
	idxA, fake, d, destID, cleanup := newTestIndex(t)
	defer cleanup()
	idxB := NewIndex(d, fake, destID)

	if idxA.writerID == idxB.writerID {
		t.Fatal("two Index instances share a writer ID")
	}
	for i := 0; i < 5; i++ {
		if err := idxA.AppendStorageIndex(PackInfo{ID: "a", Path: "pa"}); err != nil {
			t.Fatal(err)
		}
		if err := idxB.AppendTombstone("b"); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := fake.List(indexRootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing) != 10 {
		t.Fatalf("got %d index blobs, want 10 — writers overwrote each other", len(listing))
	}
}
