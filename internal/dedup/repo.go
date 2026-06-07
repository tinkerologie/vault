package dedup

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

const (
	repoConfigPath = "_vault/repo.json"
	packsRoot      = "_vault/packs"
	repoVersion    = 1
	splitterAlgo   = "kfastcdc"
	hashAlgo       = "hmac-blake2b-256"
	encryptionAlgo = "aes-256-gcm"
)

// maxChunkPlaintext bounds a single Put. Nothing legitimate reaches it —
// content chunks are bounded by ChunkMax (4 MiB) and manifest segments by
// ManifestSegmentSize (4 MiB) — so this is a guard that turns an oversized
// Put into a clear, early error instead of the packer's opaque Flush-time
// "pack size exceeds safety bound" failure.
const maxChunkPlaintext = PackTargetSize

// repoConfig is the on-disk JSON envelope at _vault/repo.json. Every
// destination has exactly one. The only secret inside is SealedMaster —
// safe to leave on untrusted storage because unsealing requires the
// daemon's serverKey.
type repoConfig struct {
	Version      int            `json:"version"`
	UUID         string         `json:"uuid"`
	Splitter     splitterConfig `json:"splitter"`
	Hash         string         `json:"hash"`
	Encryption   string         `json:"encryption"`
	SealedMaster []byte         `json:"sealed_master"`
}

type splitterConfig struct {
	Algo string `json:"algo"`
	Min  int    `json:"min"`
	Avg  int    `json:"avg"`
	Max  int    `json:"max"`
}

// Repo is the per-destination chunk repository facade. All dedup-aware
// engine handlers go through Repo — they never touch chunker / packer /
// index directly.
type Repo struct {
	db        *db.DB
	adapter   storage.Adapter
	storageID int64

	master       []byte
	chunkHashKey []byte
	splitterKey  []byte

	idx    *Index
	packer *Packer

	// mu serialises Put / Flush so the packer (which is not safe for
	// concurrent Add) sees a single writer. It also guards `pending`.
	mu sync.Mutex
	// pending tracks chunk IDs that have been Add()'d to the packer but not
	// yet flushed to the index. Without this set, two Puts with identical
	// plaintext that happen before the first Flush would both queue into
	// the pack — inflating TotalChunks and wasting space. Cleared in the
	// onFlush callback after the index records the pack.
	pending      map[ID]struct{}
	lastFlushErr error

	// statsMu guards the LogicalBytes session counter — written in Put(), read by SessionLogicalBytes().
	statsMu sync.RWMutex
	stats   Stats
}

// Stats is a snapshot of per-destination dedup metrics. Returned by Stats().
type Stats struct {
	TotalChunks         int64     `json:"total_chunks"`
	TotalPacks          int64     `json:"total_packs"`
	LogicalBytes        int64     `json:"logical_bytes"`
	PhysicalBytes       int64     `json:"physical_bytes"`
	WastedBytesEstimate int64     `json:"wasted_bytes_estimate"`
	LastGCAt            time.Time `json:"last_gc_at,omitempty"`
	LastGCFreedBytes    int64     `json:"last_gc_freed_bytes"`
}

// InitRepo creates a fresh dedup repository at the destination. Refuses if
// _vault/repo.json already exists — never overwrites an existing repo
// header (Stat, not Read, so even a 0-byte stub triggers the refuse path).
func InitRepo(d *db.DB, a storage.Adapter, storageID int64, serverKey []byte) (*Repo, error) {
	if _, err := a.Stat(repoConfigPath); err == nil {
		return nil, errors.New("dedup: destination already initialised (repo.json exists)")
	}
	master := make([]byte, SecretSize)
	if _, err := rand.Read(master); err != nil {
		return nil, fmt.Errorf("dedup: rand read master: %w", err)
	}
	sealed, err := SealMaster(serverKey, master)
	if err != nil {
		return nil, err
	}
	uuidB := make([]byte, 16)
	if _, err := rand.Read(uuidB); err != nil {
		return nil, fmt.Errorf("dedup: rand read uuid: %w", err)
	}
	cfg := repoConfig{
		Version:      repoVersion,
		UUID:         hex.EncodeToString(uuidB),
		Splitter:     splitterConfig{Algo: splitterAlgo, Min: ChunkMin, Avg: ChunkAvg, Max: ChunkMax},
		Hash:         hashAlgo,
		Encryption:   encryptionAlgo,
		SealedMaster: sealed,
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := a.Write(repoConfigPath, bytes.NewReader(body)); err != nil {
		return nil, fmt.Errorf("dedup: write repo.json: %w", err)
	}
	return buildRepo(d, a, storageID, master), nil
}

// OpenRepo opens an existing dedup repository at the destination. Returns
// an error (and no Repo) on missing config, unsupported version, or
// unsealing failure (wrong serverKey).
func OpenRepo(d *db.DB, a storage.Adapter, storageID int64, serverKey []byte) (*Repo, error) {
	rc, err := a.Read(repoConfigPath)
	if err != nil {
		return nil, fmt.Errorf("dedup: read repo.json: %w", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("dedup: read repo.json body: %w", err)
	}
	var cfg repoConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("dedup: decode repo.json: %w", err)
	}
	if cfg.Version != repoVersion {
		return nil, fmt.Errorf("dedup: unsupported repo version %d", cfg.Version)
	}
	master, err := UnsealMaster(serverKey, cfg.SealedMaster)
	if err != nil {
		return nil, fmt.Errorf("dedup: unseal master (wrong serverKey?): %w", err)
	}
	return buildRepo(d, a, storageID, master), nil
}

// buildRepo wires up the in-memory Repo, Index, and Packer. Defensive copies
// of every secret so the caller's buffers are independent.
func buildRepo(d *db.DB, a storage.Adapter, storageID int64, master []byte) *Repo {
	masterCopy := append([]byte(nil), master...)
	r := &Repo{
		db:           d,
		adapter:      a,
		storageID:    storageID,
		master:       masterCopy,
		chunkHashKey: DeriveChunkHashKey(masterCopy),
		splitterKey:  DeriveSplitterSecret(masterCopy),
		pending:      make(map[ID]struct{}),
	}
	r.idx = NewIndex(d, a, storageID)
	r.packer = NewPacker(a, masterCopy, packsRoot, func(info PackInfo) {
		// onFlush is invoked synchronously inside Add() / Flush() while r.mu
		// is held, so we can mutate lastFlushErr / pending without re-locking.
		// The Index calls don't need r.mu (SQLite is concurrency-safe).
		if err := r.idx.Register(info); err != nil {
			r.lastFlushErr = err
			return
		}
		if err := r.idx.AppendStorageIndex(info); err != nil {
			r.lastFlushErr = err
			return
		}
		for _, e := range info.Entries {
			delete(r.pending, e.ID)
		}
		r.statsMu.Lock()
		r.stats.TotalPacks++
		r.stats.TotalChunks += int64(info.ChunkCount)
		r.stats.PhysicalBytes += info.SizeBytes
		r.statsMu.Unlock()
	})
	return r
}

// SplitterSecret returns the per-repo secret used to seed the chunker's
// Gear table. Engine handlers (folder/plugin/container BackupChunked) pass
// this directly to dedup.NewChunker(). Returned as a copy so callers cannot
// mutate the Repo's internal key.
func (r *Repo) SplitterSecret() []byte { return append([]byte(nil), r.splitterKey...) }

// ChunkID computes the deterministic content ID for plaintext under this
// destination's HMAC key.
func (r *Repo) ChunkID(plaintext []byte) ID { return ChunkID(r.chunkHashKey, plaintext) }

// Has returns true if this destination already has a chunk for the given ID.
func (r *Repo) Has(id ID) bool { return r.idx.Has(id) }

// Put stores plaintext if not already present and returns its chunk ID.
// Pre-computes the ID, short-circuits on Has(); otherwise queues into the
// packer. Packs are flushed lazily inside the packer when the buffer
// crosses PackTargetSize. The caller must invoke Flush() at the end of a
// backup pass to drain any pending pack.
func (r *Repo) Put(plaintext []byte) (ID, error) {
	if len(plaintext) > maxChunkPlaintext {
		return ID{}, fmt.Errorf("dedup: chunk too large (%d bytes, limit %d) — data must be chunked before storage", len(plaintext), maxChunkPlaintext)
	}
	id := r.ChunkID(plaintext)
	// Track every Put's plaintext size on the session counter regardless of
	// dedup outcome — this is what the runner uses to populate
	// restore_points.size_bytes (the user's "would-have-cost-without-dedup"
	// total for this backup pass).
	r.statsMu.Lock()
	r.stats.LogicalBytes += int64(len(plaintext))
	r.statsMu.Unlock()
	if r.idx.Has(id) {
		return id, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Re-check under lock — another goroutine may have just put it.
	if r.idx.Has(id) {
		return id, nil
	}
	// Skip if this chunk is already buffered in the current pack — avoids
	// inflating TotalChunks / PhysicalBytes with intra-pack duplicates.
	if _, ok := r.pending[id]; ok {
		return id, nil
	}
	if _, err := r.packer.Add(id, plaintext); err != nil {
		return ID{}, err
	}
	r.pending[id] = struct{}{}
	// Surface any onFlush error that happened during the Add (if the buffer
	// crossed PackTargetSize the packer flushed and may have set lastFlushErr).
	if r.lastFlushErr != nil {
		e := r.lastFlushErr
		r.lastFlushErr = nil
		return ID{}, e
	}
	return id, nil
}

// Get retrieves plaintext for the given chunk ID via Index.Locate +
// adapter.ReadRange + DecryptChunk. The first byte of the on-disk chunk is
// the flags byte (bit0 reserved for compression, always 0 in v1); the
// ciphertext follows.
func (r *Repo) Get(id ID) ([]byte, error) {
	packPath, offset, length, err := r.idx.Locate(id)
	if err != nil {
		return nil, fmt.Errorf("dedup: locate chunk: %w", err)
	}
	rc, err := r.adapter.ReadRange(packPath, offset, length)
	if err != nil {
		return nil, fmt.Errorf("dedup: read chunk: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("dedup: read chunk body: %w", err)
	}
	if len(raw) < 1 {
		return nil, errors.New("dedup: chunk too short (missing flags byte)")
	}
	// flags := raw[0] — bit0 reserved for compression; ignored in v1.
	return DecryptChunk(r.master, id, raw[1:])
}

// LocateForVerify exposes Index.Locate so the verify path (Task 12) can
// Stat packs without decrypting chunk bodies.
func (r *Repo) LocateForVerify(id ID) (packPath string, offset, length int64, err error) {
	return r.idx.Locate(id)
}

// Flush forces any pending pack to upload, then surfaces any deferred
// onFlush error (the packer's callback can only signal failure through
// lastFlushErr because Packer.Add / Flush don't propagate callback errors).
func (r *Repo) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.packer.Flush(); err != nil {
		return err
	}
	if r.lastFlushErr != nil {
		e := r.lastFlushErr
		r.lastFlushErr = nil
		return e
	}
	return nil
}

// PutManifest serialises m and stores it as a chunk. Returns the manifest's
// chunk ID — callers persist this as restore_point.manifest_id via
// db.SetRestorePointManifestID. The item argument is informational; the
// canonical name lives inside m.Item.
//
// Manifests at or below ManifestSegmentSize are stored as a single chunk (v1
// layout). Larger manifests are split via the same content-defined chunker
// used for file data, each chunk stored as a normal dedup'd chunk, with a small
// SegmentedManifest envelope stored as the root — keeping every chunk well under
// the packer's safety bound regardless of how many files the item contains, and
// letting near-identical manifests dedup across snapshots. The approach matches
// restic and borg's handling of large metadata.
func (r *Repo) PutManifest(item string, m Manifest) (ID, error) {
	body, err := m.EncodeJSON()
	if err != nil {
		return ID{}, fmt.Errorf("dedup: encode manifest: %w", err)
	}
	if len(body) <= ManifestSegmentSize {
		return r.Put(body)
	}
	chunker, err := NewChunker(r.splitterKey)
	if err != nil {
		return ID{}, fmt.Errorf("dedup: manifest chunker: %w", err)
	}
	var segments []ID
	if err := chunker.Split(bytes.NewReader(body), func(chunk []byte) error {
		segID, err := r.Put(chunk)
		if err != nil {
			return fmt.Errorf("dedup: put manifest segment: %w", err)
		}
		segments = append(segments, segID)
		return nil
	}); err != nil {
		return ID{}, err
	}
	envelope, err := json.Marshal(SegmentedManifest{Type: segmentedManifestType, Segments: segments})
	if err != nil {
		return ID{}, fmt.Errorf("dedup: encode manifest envelope: %w", err)
	}
	return r.Put(envelope)
}

// GetManifest fetches and parses a manifest stored via PutManifest. It
// transparently handles both layouts: a v1 single-chunk manifest, and a
// SegmentedManifest envelope whose segments are reassembled before decoding.
func (r *Repo) GetManifest(id ID) (Manifest, error) {
	body, err := r.Get(id)
	if err != nil {
		return Manifest{}, err
	}
	if !isSegmentedManifest(body) {
		m, err := DecodeManifest(body)
		if err != nil {
			return Manifest{}, fmt.Errorf("dedup: decode manifest: %w", err)
		}
		return m, nil
	}
	var env SegmentedManifest
	if err := json.Unmarshal(body, &env); err != nil {
		return Manifest{}, fmt.Errorf("dedup: decode manifest envelope: %w", err)
	}
	var buf bytes.Buffer
	for i, segID := range env.Segments {
		seg, err := r.Get(segID)
		if err != nil {
			return Manifest{}, fmt.Errorf("dedup: get manifest segment %d/%d: %w", i+1, len(env.Segments), err)
		}
		buf.Write(seg)
	}
	m, err := DecodeManifest(buf.Bytes())
	if err != nil {
		return Manifest{}, fmt.Errorf("dedup: decode reassembled manifest: %w", err)
	}
	return m, nil
}

// Stats returns a snapshot of dedup metrics for this destination. Cheap;
// safe to call from request handlers.
//
// TotalChunks / TotalPacks / PhysicalBytes / LogicalBytes are read from
// SQL aggregates (db.DedupAggregates) so the values are correct across
// daemon restarts and from any goroutine — not just the one that wrote
// them. WastedBytesEstimate / LastGCAt / LastGCFreedBytes are sourced
// from the latest dedup_gc_runs row (durable across restarts), so these
// fields are visible from any Repo instance — including freshly-opened
// ones on the stats-poll path — not just the instance that ran GC.
func (r *Repo) Stats() Stats {
	out := Stats{}
	if agg, err := r.db.DedupAggregates(r.storageID); err == nil {
		out.TotalChunks = agg.TotalChunks
		out.TotalPacks = agg.TotalPacks
		out.PhysicalBytes = agg.PhysicalBytes
		out.LogicalBytes = agg.LogicalBytes
	}
	if run, found, err := r.db.LatestDedupGCRun(r.storageID); err == nil && found {
		out.WastedBytesEstimate = run.RewritableBytes
		out.LastGCAt = run.CompletedAt
		out.LastGCFreedBytes = run.FreedBytes
	}
	return out
}

// SessionLogicalBytes returns the number of plaintext bytes Put through
// this Repo instance since it was opened. Counts both newly-stored AND
// already-present chunks — so the value reflects the user's
// "would-have-cost-without-dedup" footprint for the current backup pass.
// The runner persists this on restore_points.size_bytes so the API's
// dedup_ratio can compare cumulative logical bytes (sum across snapshots)
// to physical bytes (single shared chunk store).
func (r *Repo) SessionLogicalBytes() int64 {
	r.statsMu.RLock()
	defer r.statsMu.RUnlock()
	return r.stats.LogicalBytes
}
