package dedup

import (
	"errors"
	"io/fs"
	"strings"
	"sync"
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

	// Exactly one: more means the quadratic re-scan is back; zero means the
	// counter never seeds from storage and would overwrite existing history.
	if counter.lists != 1 {
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

// failingListAdapter fails List until enabled is flipped, so a test can model
// a transient storage outage at seed time.
type failingListAdapter struct {
	*FakeAdapter
	err error
}

func (f *failingListAdapter) List(prefix string) ([]storage.FileInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.FakeAdapter.List(prefix)
}

// TestIndexSeedFailurePropagates guards the regression that caching the
// counter introduced. Swallowing a List error would seed the sequence at 0
// and keep that wrong baseline for the whole run: every later blob would sort
// before the history already on storage, so a RebuildFromStorage could replay
// a stale add after a tombstone and resurrect a pack GC had deleted. A real
// listing failure must surface, and must not poison the cache.
func TestIndexSeedFailurePropagates(t *testing.T) {
	idx, fake, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	// Existing history the failing scan must not be allowed to ignore.
	if err := fake.Write(indexRootPath+"/0000000900.idx", strings.NewReader("{}\n")); err != nil {
		t.Fatal(err)
	}
	flaky := &failingListAdapter{FakeAdapter: fake, err: errors.New("connection reset")}
	idx.adapter = flaky

	if err := idx.AppendStorageIndex(PackInfo{ID: "p", Path: "pp"}); err == nil {
		t.Fatal("AppendStorageIndex succeeded despite a failing index listing")
	}

	// The failure must not have been cached: once storage recovers, the
	// counter seeds from the real maximum rather than restarting at 1.
	flaky.err = nil
	seq, err := idx.nextIndexSeq()
	if err != nil {
		t.Fatalf("nextIndexSeq after recovery: %v", err)
	}
	if seq != 901 {
		t.Fatalf("sequence after recovery = %d, want 901 (seed failure was cached)", seq)
	}
}

// TestIndexSeedTreatsMissingDirectoryAsEmpty keeps the first-ever write to a
// destination working: the index directory does not exist yet, which is not
// an error.
func TestIndexSeedTreatsMissingDirectoryAsEmpty(t *testing.T) {
	idx, fake, _, _, cleanup := newTestIndex(t)
	defer cleanup()
	idx.adapter = &failingListAdapter{FakeAdapter: fake, err: fs.ErrNotExist}

	seq, err := idx.nextIndexSeq()
	if err != nil {
		t.Fatalf("missing index directory should not be an error: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first sequence = %d, want 1", seq)
	}
}

// TestIndexNextSeqConcurrentSameInstance confirms the cached counter never
// hands the same sequence to two callers. Run under -race.
func TestIndexNextSeqConcurrentSameInstance(t *testing.T) {
	idx, _, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	const n = 100
	seqs := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq, err := idx.nextIndexSeq()
			if err != nil {
				t.Error(err)
				return
			}
			seqs[i] = seq
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for _, s := range seqs {
		if seen[s] {
			t.Fatalf("duplicate sequence %d handed out under concurrent access", s)
		}
		seen[s] = true
	}
}

// TestIndexSeedIgnoresNonIndexFiles confirms a stray artifact in the index
// directory cannot inflate the counter. strings.TrimSuffix is a no-op when
// the suffix does not match, so an unguarded parse would read
// "0000009999-upload.partial" as sequence 9999.
func TestIndexSeedIgnoresNonIndexFiles(t *testing.T) {
	idx, fake, _, _, cleanup := newTestIndex(t)
	defer cleanup()

	if err := fake.Write(indexRootPath+"/0000000007.idx", strings.NewReader("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := fake.Write(indexRootPath+"/0000009999-upload.partial", strings.NewReader("junk")); err != nil {
		t.Fatal(err)
	}

	seq, err := idx.nextIndexSeq()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 8 {
		t.Fatalf("sequence = %d, want 8 — a non-.idx artifact inflated the counter", seq)
	}
}
