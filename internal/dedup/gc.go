package dedup

import (
	"fmt"
	"log"
	"time"
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
		case !anyLive && anyDead:
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
	r.statsMu.Lock()
	r.stats.LastGCAt = res.CompletedAt
	r.stats.LastGCFreedBytes = res.FreedBytes
	r.stats.WastedBytesEstimate = res.RewritableBytes
	// FreedPacks subtract from TotalPacks; FreedBytes subtract from PhysicalBytes.
	r.stats.TotalPacks -= res.FreedPacks
	r.stats.PhysicalBytes -= res.FreedBytes
	r.statsMu.Unlock()
	return res, nil
}
