package dedup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// indexWriteFailingAdapter accepts pack blobs but rejects index blobs, which is
// what a crash or a storage hiccup between the two writes looks like.
type indexWriteFailingAdapter struct {
	*FakeAdapter
	fail bool
}

func (a *indexWriteFailingAdapter) Write(path string, r io.Reader) error {
	if a.fail && strings.HasPrefix(path, indexRootPath) {
		return errors.New("index write failed")
	}
	return a.FakeAdapter.Write(path, r)
}

// TestFlushDoesNotClaimChunksWithoutADurableIndexRecord covers the ordering in
// buildRepo's onFlush.
//
// RebuildFromStorage reconstructs SQLite from the on-storage index blobs, so
// storage is the durable record and the DB is a cache of it. Registering in
// SQLite first meant a failure before the index blob landed left the DB
// claiming chunks with no replayable add record: the backup SUCCEEDED, and only
// a later `vault dedup repair` would silently drop those chunks and reveal the
// backup was unrestorable.
//
// The failure must instead be loud, and must not leave the chunks advertised.
func TestFlushDoesNotClaimChunksWithoutADurableIndexRecord(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	adapter := &indexWriteFailingAdapter{FakeAdapter: NewFakeAdapter()}
	r.adapter = adapter
	r.idx.adapter = adapter
	r.packer.adapter = adapter

	payload := bytes.Repeat([]byte{0x21}, 4096)
	id, err := r.Put(payload)
	if err != nil {
		t.Fatal(err)
	}

	adapter.fail = true
	if err := r.Flush(); err == nil {
		t.Fatal("Flush succeeded despite the index write failing — the run would " +
			"report success while the pack has no replayable add record")
	}

	if r.Has(id) {
		t.Fatal("chunk advertised as present without a durable index record; a " +
			"rebuild would drop it and strand any backup referencing it")
	}
}

// TestFlushRecordsIndexBeforeSQLite pins the ordering directly, so a future
// refactor cannot quietly swap it back.
func TestFlushRecordsIndexBeforeSQLite(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	order := &writeOrderAdapter{FakeAdapter: NewFakeAdapter(), repo: r}
	r.adapter = order
	r.idx.adapter = order
	r.packer.adapter = order

	if _, err := r.Put(bytes.Repeat([]byte{0x22}, 4096)); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	if !order.sawIndexWrite {
		t.Fatal("no index blob was written")
	}
	if order.packsInDBAtIndexWrite != 0 {
		t.Fatal("the pack was registered in SQLite before its index blob was durable")
	}
}

// writeOrderAdapter records how much SQLite state existed at the moment the
// index blob was written.
type writeOrderAdapter struct {
	*FakeAdapter
	repo                  *Repo
	sawIndexWrite         bool
	packsInDBAtIndexWrite int
}

func (a *writeOrderAdapter) Write(path string, r io.Reader) error {
	if strings.HasPrefix(path, indexRootPath) && !a.sawIndexWrite {
		a.sawIndexWrite = true
		packs, err := a.repo.db.ListDedupPacks(a.repo.storageID)
		if err == nil {
			a.packsInDBAtIndexWrite = len(packs)
		}
	}
	return a.FakeAdapter.Write(path, r)
}
