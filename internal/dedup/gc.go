package dedup

import (
	"fmt"
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
	Errors          []string
}

// RunGC runs a mark-and-sweep collection over the repo. `live` is the
// list of manifest IDs referenced by non-deleted restore points; the
// caller assembles this from the DB (Task 11's runner.collectLiveManifestIDs).
//
// v1 only sweeps fully-unreferenced packs. Mixed-content packs are
// reported as "rewritable bytes" for the Storage page stats but left in
// place. Compaction is a follow-up.
func RunGC(r *Repo, live []ID) (GCResult, error) {
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
	for _, p := range packs {
		chunks, err := r.db.ListDedupChunksByPack(r.storageID, p.ID)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("list chunks for pack %s: %v", p.ID, err))
			continue
		}
		anyLive, anyDead := false, false
		for _, c := range chunks {
			var id ID
			copy(id[:], c.ChunkID)
			if _, ok := reachable[id]; ok {
				anyLive = true
			} else {
				anyDead = true
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
		}
	}

	res.CompletedAt = time.Now().UTC()

	// Persist the run so the Storage card (which polls via a *different*,
	// freshly-opened repo instance) sees real reclaimable / last-GC numbers.
	// Best-effort: the sweep already succeeded, so a stats-insert failure must
	// not make the GC look failed — log and carry on.
	if _, err := r.db.InsertDedupGCRun(db.DedupGCRun{
		StorageID:       r.storageID,
		StartedAt:       res.StartedAt,
		CompletedAt:     res.CompletedAt,
		Reachable:       res.Reachable,
		FreedPacks:      res.FreedPacks,
		FreedBytes:      res.FreedBytes,
		RewritableBytes: res.RewritableBytes,
		ErrorCount:      int64(len(res.Errors)),
	}); err != nil {
		log.Printf("gc: failed to persist gc run for storage %d: %v", r.storageID, err)
	}
	return res, nil
}
