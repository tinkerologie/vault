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
// Read order is lexicographic (matches numeric ascending because the names
// are zero-padded sequence numbers). Each JSONL line is one pack.
func (idx *Index) RebuildFromStorage() error {
	if err := idx.dropDBState(); err != nil {
		return err
	}
	entries, err := idx.adapter.List(indexRootPath)
	if err != nil {
		return fmt.Errorf("dedup: list index: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
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
			// Tombstone lines carry zero-valued add fields (no chunks, blank pack ID).
			// Check this branch first so we never act on those zero values as an add.
			if entry.Tombstone != "" {
				if err := idx.db.DeleteDedupPack(idx.storageID, entry.Tombstone); err != nil {
					_ = rc.Close()
					return fmt.Errorf("dedup: rebuild tombstone %s: %w", entry.Tombstone, err)
				}
				continue
			}
			info := PackInfo{
				ID: entry.PackID, Path: entry.PackPath,
				SizeBytes: entry.SizeBytes, ChunkCount: entry.ChunkCount,
				Entries: entry.Chunks,
			}
			if err := idx.registerForRebuild(info); err != nil {
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
// Concurrency: the counter is mutex-guarded, and blob names carry a
// per-instance writerID, so two Index instances writing the same destination
// (e.g. a backup and a GC compaction) can no longer overwrite each other's
// blobs — the collision hazard the previous implementation documented but
// did not prevent. Equal sequence numbers across instances are harmless:
// RebuildFromStorage applies each line by pack ID, and GC only tombstones
// packs whose add-line was written at a strictly lower sequence.
func (idx *Index) nextIndexSeq() (int64, error) {
	idx.seqMu.Lock()
	defer idx.seqMu.Unlock()
	if !idx.seeded {
		idx.seq = idx.scanMaxIndexSeq()
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
// _vault/index/, or 0 if the directory is empty, missing, or unreadable.
// Both the legacy "%010d.idx" and current "%010d-<writer>.idx" forms parse.
func (idx *Index) scanMaxIndexSeq() int64 {
	entries, err := idx.adapter.List(indexRootPath)
	if err != nil {
		// First-ever write — directory may not exist on some adapters.
		return 0
	}
	var maxSeq int64
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		base := strings.TrimSuffix(path.Base(e.Path), ".idx")
		if i := strings.IndexByte(base, '-'); i >= 0 {
			base = base[:i]
		}
		n, err := strconv.ParseInt(base, 10, 64)
		if err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	return maxSeq
}
