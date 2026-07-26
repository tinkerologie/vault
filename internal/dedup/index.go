package dedup

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

const indexRootPath = "_vault/index"

// indexEntry is one line in a JSONL index blob.
type indexEntry struct {
	PackID     string      `json:"pack_id"`
	PackPath   string      `json:"pack_path"`
	SizeBytes  int64       `json:"size_bytes"`
	ChunkCount int         `json:"chunk_count"`
	Chunks     []PackEntry `json:"chunks"`
	// Tombstone, when non-empty, marks this line as a delete record for the
	// named packID — RebuildFromStorage removes the pack row and any chunks
	// still pointing at it. Compaction (Task 4) and GC's fully-dead-pack
	// delete (Task 1) both append a tombstone so a later rebuild does not
	// resurrect the pack.
	Tombstone string `json:"tombstone,omitempty"`
}

// Index owns both the SQLite tables and the on-storage JSONL blobs that
// together form the dedup repo's content map for one destination.
type Index struct {
	db        *db.DB
	adapter   storage.Adapter
	storageID int64

	// seqMu guards the cached blob sequence counter. seq is the last
	// sequence number this Index handed out; seeded lazily from storage on
	// first use (see nextIndexSeq). writerID makes blob names unique per
	// Index instance so two concurrent writers can never collide.
	seqMu    sync.Mutex
	seq      int64
	seeded   bool
	writerID string
}

// NewIndex constructs an Index bound to one destination.
func NewIndex(d *db.DB, a storage.Adapter, storageID int64) *Index {
	return &Index{db: d, adapter: a, storageID: storageID, writerID: newWriterID()}
}

// newWriterID returns a short random token that disambiguates index blobs
// written by different Index instances against the same destination. Falls
// back to a fixed token if the system RNG fails — a collision is then no
// more likely than it was before writer IDs existed.
func newWriterID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// Has returns true if the chunk is already present in this destination.
// O(1) DB lookup keyed on (storage_id, chunk_id).
func (idx *Index) Has(chunkID ID) bool {
	ok, _ := idx.db.HasDedupChunk(idx.storageID, chunkID[:])
	return ok
}

// Locate returns the pack path + chunk offset + chunk length for the chunkID.
// Used by Repo.Get to compose a single adapter.ReadRange call.
func (idx *Index) Locate(chunkID ID) (packPath string, offset int64, length int64, err error) {
	return idx.db.LocateDedupChunk(idx.storageID, chunkID[:])
}

// Register inserts the pack and its chunks into the SQLite tables.
// Uses INSERT OR IGNORE so a crash-and-retry mid-flush is safe.
func (idx *Index) Register(info PackInfo) error {
	if err := idx.db.UpsertDedupPack(db.DedupPack{
		ID: info.ID, StorageID: idx.storageID, Path: info.Path,
		SizeBytes: info.SizeBytes, ChunkCount: info.ChunkCount,
	}); err != nil {
		return fmt.Errorf("dedup: upsert pack: %w", err)
	}
	for _, e := range info.Entries {
		if err := idx.db.UpsertDedupChunk(db.DedupChunk{
			ChunkID: e.ID[:], StorageID: idx.storageID, PackID: info.ID,
			Offset: e.Offset, Length: e.Length,
		}); err != nil {
			return fmt.Errorf("dedup: upsert chunk: %w", err)
		}
	}
	return nil
}

// registerForRebuild mirrors Register but uses REPLACE (ON CONFLICT DO UPDATE)
// semantics so a later add-line for the same chunk (e.g. compaction moving a
// chunk to a new pack) wins over an earlier one. Only called by
// RebuildFromStorage; the live-write Register path keeps INSERT OR IGNORE for
// crash-retry idempotency.
func (idx *Index) registerForRebuild(info PackInfo) error {
	if err := idx.db.ReplaceDedupPack(db.DedupPack{
		ID: info.ID, StorageID: idx.storageID, Path: info.Path,
		SizeBytes: info.SizeBytes, ChunkCount: info.ChunkCount,
	}); err != nil {
		return fmt.Errorf("dedup: replace pack: %w", err)
	}
	for _, e := range info.Entries {
		if err := idx.db.ReplaceDedupChunk(db.DedupChunk{
			ChunkID: e.ID[:], StorageID: idx.storageID, PackID: info.ID,
			Offset: e.Offset, Length: e.Length,
		}); err != nil {
			return fmt.Errorf("dedup: replace chunk: %w", err)
		}
	}
	return nil
}

// AppendStorageIndex writes a JSONL entry for this pack to the next
// sequence-numbered blob under _vault/index/. The blob contains a single
// indexEntry per file (one line). RebuildFromStorage concatenates them in
// sequence order to reconstitute the SQLite tables.
func (idx *Index) AppendStorageIndex(info PackInfo) error {
	seq, err := idx.nextIndexSeq()
	if err != nil {
		return err
	}
	name := idx.indexBlobName(seq)
	line, err := json.Marshal(indexEntry{
		PackID: info.ID, PackPath: info.Path,
		SizeBytes: info.SizeBytes, ChunkCount: info.ChunkCount, Chunks: info.Entries,
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return idx.adapter.Write(path.Join(indexRootPath, name), strings.NewReader(string(line)))
}

// AppendTombstone records that packID has been deleted from storage. The
// caller writes the tombstone BEFORE deleting the on-storage blob and DB row,
// so a crash mid-delete still leaves a durable intent that the next rebuild
// will apply (idempotently — re-applying tombstones is a no-op).
func (idx *Index) AppendTombstone(packID string) error {
	seq, err := idx.nextIndexSeq()
	if err != nil {
		return err
	}
	name := idx.indexBlobName(seq)
	line, err := json.Marshal(indexEntry{Tombstone: packID})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return idx.adapter.Write(path.Join(indexRootPath, name), strings.NewReader(string(line)))
}

// dropDBState wipes ONLY the SQLite-side state. Used by RebuildFromStorage.
// Package-private — callers outside this package should use RebuildFromStorage.
func (idx *Index) dropDBState() error {
	return idx.db.DropDedupState(idx.storageID)
}

// RebuildFromStorage wipes the SQLite tables for this destination and
// re-populates them from the on-storage JSONL blobs. Used by:
//   - `vault dedup repair --dest=X` (Task 14)
//   - TestDisasterRecovery_RebuildIndex (Task 8 integration test)
//
// Replay runs in two passes and is deliberately order-independent. A single
// in-order pass that applied tombstones as deletes made correctness depend on
// which blob sorted first, which is not something we can guarantee: two Index
// instances writing one destination cache their own counters and so hand out
// equal sequence numbers, leaving the random writerID to break the tie.
//
// Order mattered because chunk rows are one-per-(storage_id, chunk_id) with
// REPLACE semantics, while DeleteDedupPack cascades onto them. After
// compaction copies chunk C from dying pack X into new pack Y, the sequence
// add(Y), add(X), tombstone(X) left C pointing at X and then cascaded it away
// — losing a chunk that live pack Y still held, so restores failed after a
// repair. Deferring the deletes to the end does not help: C still points at X
// when the cascade fires.
//
// Collecting the tombstones first and skipping their adds sidesteps all of it.
// dropDBState has already emptied the tables, so a tombstone has nothing to
// delete — suppressing the add is its entire job. This mirrors restic's
// RepairIndex, which builds a removePacks set before rewriting the index.
//
// Safe because pack IDs are random (see Packer.Flush), never content-derived,
// so a tombstoned ID is never legitimately reused by a later pack.
func (idx *Index) RebuildFromStorage() error {
	if err := idx.dropDBState(); err != nil {
		return err
	}
	entries, err := idx.adapter.List(indexRootPath)
	if err != nil {
		return fmt.Errorf("dedup: list index: %w", err)
	}
	// Sorted for deterministic replay and error reporting only — the result no
	// longer depends on this order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	// Pass 1: which packs were deleted? Only pack IDs are retained, so memory
	// stays flat regardless of repository size.
	tombstoned := make(map[string]struct{})
	if err := idx.forEachIndexEntry(entries, func(e indexEntry) error {
		if e.Tombstone != "" {
			tombstoned[e.Tombstone] = struct{}{}
		}
		return nil
	}); err != nil {
		return err
	}

	// Pass 2: apply the adds that survived.
	return idx.forEachIndexEntry(entries, func(e indexEntry) error {
		// Tombstone lines carry zero-valued add fields (no chunks, blank pack
		// ID). Check this branch first so we never act on those zero values as
		// an add.
		if e.Tombstone != "" {
			return nil
		}
		if _, dead := tombstoned[e.PackID]; dead {
			return nil
		}
		return idx.registerForRebuild(PackInfo{
			ID: e.PackID, Path: e.PackPath,
			SizeBytes: e.SizeBytes, ChunkCount: e.ChunkCount,
			Entries: e.Chunks,
		})
	})
}

// forEachIndexEntry reads each index blob in turn and invokes fn once per
// JSONL line. Shared by both RebuildFromStorage passes so the read, scan, and
// close handling exists in one place.
func (idx *Index) forEachIndexEntry(entries []storage.FileInfo, fn func(indexEntry) error) error {
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		rc, err := idx.adapter.Read(e.Path)
		if err != nil {
			return fmt.Errorf("dedup: read index %s: %w", e.Path, err)
		}
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 1<<16), 16<<20) // allow large lines (manifests can be large)
		for sc.Scan() {
			var entry indexEntry
			if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
				_ = rc.Close()
				return fmt.Errorf("dedup: parse index entry: %w", err)
			}
			if err := fn(entry); err != nil {
				_ = rc.Close()
				return err
			}
		}
		_ = rc.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("dedup: scan index %s: %w", e.Path, err)
		}
	}
	return nil
}

// nextIndexSeq returns the next blob sequence number for this Index.
//
// The directory is listed ONCE per Index instance to seed the counter; every
// subsequent call increments in memory. Listing on each call made a large
// backup quadratic in pack count: a 500 GB folder flushes ~21k packs
// (PackTargetSize is 24 MiB), so the old code issued ~21k remote listings of
// a directory growing to ~21k entries — hundreds of millions of directory
// entries pulled over the wire, degrading until the backup appeared frozen
// (issue #256).
//
// Concurrency: the counter is mutex-guarded. Caching it means two Index
// instances against one destination no longer re-read each other's writes,
// so they can hand out the same sequence number — blob names therefore carry
// a per-instance writerID and both writes survive rather than one silently
// overwriting the other (which is what the old code did on a same-sequence
// race). In-daemon this does not arise: RunDedupGC takes the same run slot as
// a backup, so GC and backup never write one destination concurrently. Their
// relative replay order at an equal sequence is decided by writerID rather
// than by wall-clock order; making RebuildFromStorage order-independent
// (apply tombstones as a pre-pass) is the durable fix and is tracked
// separately — it is a pre-existing property of the replay format, not
// something this change introduces.
func (idx *Index) nextIndexSeq() (int64, error) {
	idx.seqMu.Lock()
	defer idx.seqMu.Unlock()
	if !idx.seeded {
		seq, err := idx.scanMaxIndexSeq()
		if err != nil {
			// Leave the counter unseeded so a later append retries the scan.
			// Seeding from a failed listing would restart at 1 and keep that
			// wrong baseline for the rest of the run: subsequent blobs would
			// replay before the history already on storage, letting a
			// RebuildFromStorage resurrect packs GC had tombstoned.
			return 0, err
		}
		idx.seq = seq
		idx.seeded = true
	}
	idx.seq++
	return idx.seq, nil
}

// indexBlobName renders the on-storage name for one index blob. The
// zero-padded sequence leads so lexicographic listing order (what
// RebuildFromStorage relies on) still matches numeric order.
func (idx *Index) indexBlobName(seq int64) string {
	return fmt.Sprintf("%010d-%s.idx", seq, idx.writerID)
}

// scanMaxIndexSeq returns the largest sequence number already present under
// _vault/index/, or 0 when the directory does not exist yet (the first-ever
// write to a destination). Any other listing failure is propagated: treating
// it as "no blobs" would silently restart the sequence and let later blobs
// replay ahead of the history already on storage. Both the legacy
// "%010d.idx" and current "%010d-<writer>.idx" forms parse.
func (idx *Index) scanMaxIndexSeq() (int64, error) {
	entries, err := idx.adapter.List(indexRootPath)
	if err != nil {
		if storage.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("dedup: list index: %w", err)
	}
	var maxSeq int64
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := path.Base(e.Path)
		base := strings.TrimSuffix(name, ".idx")
		if base == name {
			// TrimSuffix is a no-op on a non-match, so without this guard a
			// stray artifact like "0000000042-upload.partial" would parse as
			// sequence 42 and inflate the counter.
			continue
		}
		if i := strings.IndexByte(base, '-'); i >= 0 {
			base = base[:i]
		}
		n, err := strconv.ParseInt(base, 10, 64)
		if err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	return maxSeq, nil
}
