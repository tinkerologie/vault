package dedup

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// GCResult summarises a garbage-collection run.
type GCResult struct {
	StartedAt       time.Time
	CompletedAt     time.Time
	Reachable       int64
	FreedPacks      int64
	FreedBytes      int64
	RewritableBytes int64
	CompactedPacks  int64 // mixed packs repacked into fresh packs
	ReclaimedBytes  int64 // physical bytes removed by compaction (drained_total − new_total)
	Errors          []string
}

// GCOptions controls optional behaviors of RunGC. The zero value disables
// compaction (delete-only, the v1 default that mirrors RunGC's behavior
// before the compact phase was added).
type GCOptions struct {
	// CompactMinDeadRatio: a mixed pack is repacked when
	// dead_bytes/size_bytes >= this value. Values <= 0 disable compaction
	// (delete-only). 1.0 also disables in practice — no mixed pack is 100%
	// dead, since fully-dead packs are swept by the deletion case.
	CompactMinDeadRatio float64
}

// mixedCandidate is one mixed pack the sweep loop recorded for the optional
// compaction phase. Stored at file scope so RunGC and compactMixedPacks can
// share the type. live[] holds the chunks that must survive the repack;
// dead is the byte total of evicted chunks (for ratio computation).
type mixedCandidate struct {
	pack db.DedupPack
	live []db.DedupChunk
	dead int64 // sum of dead chunks' Length bytes
}

// RunGC runs a mark-and-sweep collection over the repo. `live` is the
// list of manifest IDs referenced by non-deleted restore points; the
// caller assembles this from the DB (Task 11's runner.collectLiveManifestIDs).
//
// The sweep phase removes fully-unreferenced packs (and any empty packs left
// behind by a crashed compaction). Mixed packs (some-live, some-dead) are
// reported as "rewritable bytes" and, when opts.CompactMinDeadRatio is set
// and the ratio is met, repacked in-place: surviving chunks are copied
// verbatim into fresh packs and the old packs are tombstoned + deleted.
func RunGC(r *Repo, live []ID, opts GCOptions) (GCResult, error) {
	res := GCResult{StartedAt: time.Now().UTC()}

	// -- Mark phase -- walk every live manifest, accumulate every reachable chunk.
	reachable := make(map[ID]struct{}, 1024)
	for _, mID := range live {
		reachable[mID] = struct{}{} // the manifest's own chunk is reachable too
		m, err := r.GetManifest(mID)
		if err != nil {
			return res, fmt.Errorf("gc: load manifest %x: %w", mID, err)
		}
		for _, f := range m.Files {
			for _, c := range f.Chunks {
				reachable[c] = struct{}{}
			}
		}
	}
	res.Reachable = int64(len(reachable))

	// -- Sweep phase -- for each pack decide live / dead / mixed.
	packs, err := r.db.ListDedupPacks(r.storageID)
	if err != nil {
		return res, fmt.Errorf("gc: list packs: %w", err)
	}
	var mixed []mixedCandidate
	for _, p := range packs {
		chunks, err := r.db.ListDedupChunksByPack(r.storageID, p.ID)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("list chunks for pack %s: %v", p.ID, err))
			continue
		}
		anyLive, anyDead := false, false
		var liveChunks []db.DedupChunk
		var deadBytes int64
		for _, c := range chunks {
			var id ID
			copy(id[:], c.ChunkID)
			if _, ok := reachable[id]; ok {
				anyLive = true
				liveChunks = append(liveChunks, c)
			} else {
				anyDead = true
				deadBytes += c.Length
			}
		}
		switch {
		case !anyLive:
			// Both fully-dead packs (anyDead=true) and empty packs
			// (anyDead=false, chunk_count=0 — only occurs after a crashed
			// compaction) take the same deletion path: tombstone, storage
			// delete, DB delete. Empty packs would otherwise linger because
			// no chunk references keep them around.
			//
			// Tombstone FIRST — a durable intent that survives a crash before
			// the storage delete. AppendTombstone is idempotent on retry, and
			// RebuildFromStorage applies tombstones to remove pack rows so a
			// rebuild after GC does not resurrect swept packs.
			if err := r.idx.AppendTombstone(p.ID); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("tombstone %s: %v", p.ID, err))
				log.Printf("gc: failed to write tombstone for pack %s: %v", p.ID, err)
				continue
			}
			// Storage delete BEFORE DB delete — if storage delete fails we
			// skip this pack and try again next GC. If we deleted DB first
			// and then storage failed, we'd "lose" the pack from the index
			// and orphan it on disk forever.
			if err := r.adapter.Delete(p.Path); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", p.Path, err))
				log.Printf("gc: failed to delete pack %s: %v", p.Path, err)
				continue
			}
			if err := r.db.DeleteDedupPack(r.storageID, p.ID); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("db delete pack %s: %v", p.ID, err))
				// Pack is gone from storage but row remains; next GC will
				// try the storage delete again (fail-soft, no infinite loop)
				// and the DB delete will succeed once. Acceptable v1 trade-off.
				continue
			}
			res.FreedPacks++
			res.FreedBytes += p.SizeBytes
		case anyLive && anyDead:
			res.RewritableBytes += p.SizeBytes
			mixed = append(mixed, mixedCandidate{pack: p, live: liveChunks, dead: deadBytes})
		}
	}

	// Compaction phase. Skipped when opts disable it (zero, <=0, or >=1.0) or
	// no eligible packs. Survivors are copied via Packer.AddRaw into fresh
	// packs (no decrypt/re-encrypt; chunk crypto is deterministic), chunks
	// are atomically re-pointed in the DB, and old packs are then tombstoned
	// + deleted. Chunks are readable from ≥1 pack at every step.
	if opts.CompactMinDeadRatio > 0 && opts.CompactMinDeadRatio < 1.0 && len(mixed) > 0 {
		if cerr := r.compactMixedPacks(mixed, opts.CompactMinDeadRatio, &res); cerr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("compact: %v", cerr))
		}
	}

	res.CompletedAt = time.Now().UTC()

	// Persist the run so the Storage card (which polls via a *different*,
	// freshly-opened repo instance) sees real reclaimable / last-GC numbers.
	// Best-effort: the sweep already succeeded, so a stats-insert failure must
	// not make the GC look failed — log and carry on.
	//
	if _, err := r.db.InsertDedupGCRun(db.DedupGCRun{
		StorageID:       r.storageID,
		StartedAt:       res.StartedAt,
		CompletedAt:     res.CompletedAt,
		Reachable:       res.Reachable,
		FreedPacks:      res.FreedPacks,
		FreedBytes:      res.FreedBytes,
		RewritableBytes: res.RewritableBytes,
		ErrorCount:      int64(len(res.Errors)),
		CompactedPacks:  res.CompactedPacks,
		ReclaimedBytes:  res.ReclaimedBytes,
	}); err != nil {
		log.Printf("gc: failed to persist gc run for storage %d: %v", r.storageID, err)
	}
	return res, nil
}

// compactMixedPacks repacks mixed packs at/above threshold dead-ratio into
// fresh packs. The flow per batch:
//  1. Write surviving chunks (via Packer.AddRaw — verbatim copy) into a
//     shared packer; the onFlush callback registers each new pack, appends
//     its add-line to the on-storage index, and re-points its moved chunks
//     in one tx (db.RepointDedupChunks).
//  2. After Flush, for each fully-drained old pack: AppendTombstone →
//     adapter.Delete → db.DeleteDedupPack.
//
// Correctness invariant: every moved chunk is readable from at least one
// pack at every observable step. Re-point happens BEFORE the old pack is
// deleted; tombstone is written BEFORE the storage delete. Crash windows:
//   - after new-pack write, before re-point → orphan new pack; re-run
//     repacks idempotently; orphan reaped by the empty-pack GC rule.
//   - after re-point, before old-pack delete → old pack now fully dead →
//     next GC sweep reaps it.
//   - mid old-pack delete (tombstone written, blob/row not yet gone) →
//     idempotent on retry.
//
// Partial-move limitation (v1): if a chunk in pack P fails to move (e.g.
// transient read error) while siblings succeed, P is preserved with the
// stale chunks; the moved siblings now point at the new pack. The bytes
// they used to occupy in P are "dark data" — present on disk but not in
// dedup_chunks. They are invisible to future GC dead-byte detection but
// recoverable via `vault dedup repair` (rebuild from on-storage footers).
// Operator-visible counter `res.Errors` carries the per-chunk failure so
// retry is informed.
func (r *Repo) compactMixedPacks(mixed []mixedCandidate, threshold float64, res *GCResult) error {
	// Filter to eligible packs.
	type drained struct {
		pack  db.DedupPack
		live  []db.DedupChunk
		moved map[ID]struct{}
	}
	var drainList []*drained
	for _, m := range mixed {
		if m.pack.SizeBytes <= 0 {
			continue
		}
		ratio := float64(m.dead) / float64(m.pack.SizeBytes)
		if ratio < threshold {
			continue
		}
		drainList = append(drainList, &drained{
			pack:  m.pack,
			live:  m.live,
			moved: make(map[ID]struct{}, len(m.live)),
		})
	}
	if len(drainList) == 0 {
		return nil
	}

	// Build a chunk→drained map so each new pack's flush can mark which old
	// pack each moved chunk came from.
	chunkOrigin := make(map[ID]*drained)
	for _, d := range drainList {
		for _, c := range d.live {
			var id ID
			copy(id[:], c.ChunkID)
			chunkOrigin[id] = d
		}
	}

	var totalNewBytes int64
	// Capture flush callback errors so we can surface them up via res.Errors.
	pkr := NewPacker(r.adapter, r.master, packsRoot, func(info PackInfo) {
		// Note: Register's UpsertDedupChunk calls for the new pack's chunks are
		// idempotent no-ops here — the rows already exist (pointing at the old
		// pack). The actual location change happens via RepointDedupChunks below.
		if err := r.idx.Register(info); err != nil {
			msg := fmt.Sprintf("compact register pack %s: %v", info.ID, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			return
		}
		if err := r.idx.AppendStorageIndex(info); err != nil {
			msg := fmt.Sprintf("compact index pack %s: %v", info.ID, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			return
		}
		// Re-point each chunk in this new pack to (info.ID, entry.Offset, entry.Length).
		updates := make([]db.DedupChunk, 0, len(info.Entries))
		for _, e := range info.Entries {
			updates = append(updates, db.DedupChunk{
				ChunkID: e.ID[:], StorageID: r.storageID,
				PackID: info.ID, Offset: e.Offset, Length: e.Length,
			})
		}
		if err := r.db.RepointDedupChunks(r.storageID, updates); err != nil {
			msg := fmt.Sprintf("compact repoint for new pack %s: %v", info.ID, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			return
		}
		for _, e := range info.Entries {
			if d, ok := chunkOrigin[e.ID]; ok {
				d.moved[e.ID] = struct{}{}
			}
		}
		totalNewBytes += info.SizeBytes
	})

	for _, d := range drainList {
		for _, c := range d.live {
			rc, err := r.adapter.ReadRange(d.pack.Path, c.Offset, c.Length)
			if err != nil {
				msg := fmt.Sprintf("compact read %s@%d: %v", d.pack.ID, c.Offset, err)
				res.Errors = append(res.Errors, msg)
				log.Printf("gc: %s", msg)
				continue
			}
			raw, rerr := io.ReadAll(rc)
			_ = rc.Close()
			if rerr != nil {
				msg := fmt.Sprintf("compact read body %s@%d: %v", d.pack.ID, c.Offset, rerr)
				res.Errors = append(res.Errors, msg)
				log.Printf("gc: %s", msg)
				continue
			}
			var id ID
			copy(id[:], c.ChunkID)
			if _, err := pkr.AddRaw(id, raw); err != nil {
				msg := fmt.Sprintf("compact addraw %x: %v", id, err)
				res.Errors = append(res.Errors, msg)
				log.Printf("gc: %s", msg)
			}
		}
	}
	if err := pkr.Flush(); err != nil {
		return fmt.Errorf("compact final flush: %w", err)
	}

	// Delete only old packs whose every live chunk was successfully moved.
	// If a chunk failed mid-batch the old pack is preserved (operator can
	// retry; next GC will re-attempt). ReclaimedBytes only counts packs
	// whose full delete (tombstone + storage + DB) succeeded — preserved
	// packs still occupy physical space, so they don't contribute. Same
	// reasoning for the "Reclaimable" stat in RewritableBytes: when we
	// successfully delete a previously-mixed pack, subtract its bytes
	// from RewritableBytes (they're reclaimed, not reclaimable anymore).
	var successfullyDrainedBytes int64
	for _, d := range drainList {
		allMoved := true
		for _, c := range d.live {
			var id ID
			copy(id[:], c.ChunkID)
			if _, ok := d.moved[id]; !ok {
				allMoved = false
				break
			}
		}
		if !allMoved {
			msg := fmt.Sprintf("compact: not all chunks moved from %s, skipping delete", d.pack.ID)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			continue
		}
		if err := r.idx.AppendTombstone(d.pack.ID); err != nil {
			msg := fmt.Sprintf("compact tombstone %s: %v", d.pack.ID, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			continue
		}
		if err := r.adapter.Delete(d.pack.Path); err != nil {
			msg := fmt.Sprintf("compact storage delete %s: %v", d.pack.Path, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			continue
		}
		if err := r.db.DeleteDedupPack(r.storageID, d.pack.ID); err != nil {
			msg := fmt.Sprintf("compact db delete %s: %v", d.pack.ID, err)
			res.Errors = append(res.Errors, msg)
			log.Printf("gc: %s", msg)
			continue
		}
		successfullyDrainedBytes += d.pack.SizeBytes
		// The sweep loop counted this pack as RewritableBytes when it was
		// mixed; now that it's gone, those bytes are reclaimed, not
		// reclaimable. Subtract so the persisted figure (Task 5 wiring,
		// surfaced as "Reclaimable" on the Storage card) reflects bytes
		// still stranded in surviving mixed packs.
		res.RewritableBytes -= d.pack.SizeBytes
		if res.RewritableBytes < 0 {
			res.RewritableBytes = 0
		}
		res.CompactedPacks++
	}
	reclaimed := successfullyDrainedBytes - totalNewBytes
	if reclaimed < 0 {
		reclaimed = 0 // defensive; should not happen since survivors are a strict subset
	}
	res.ReclaimedBytes = reclaimed
	return nil
}
