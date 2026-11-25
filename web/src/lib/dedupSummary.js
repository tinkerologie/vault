// How a dedup destination's stats are worded on its Storage card (#713).
//
// A new or not-yet-collected repository can hold more bytes than the logical
// data it stores (chunk and pack overhead, metadata, data waiting for garbage
// collection). That is overhead, not savings: it must never be shown as a
// ratio below 1x or as "Saved 0 B" in the success colour.

/**
 * @param {{ logical_bytes?: number, physical_bytes?: number, dedup_ratio?: number, wasted_bytes_estimate?: number }} stats
 * @returns {{
 *   kind: 'empty' | 'savings' | 'even' | 'overhead',
 *   ratio: number,
 *   logical: number,
 *   physical: number,
 *   saved: number,
 *   overhead: number,
 *   storedPct: number,
 *   reclaimable: number,
 * }}
 */
export function dedupSummary(stats) {
  const logical = Math.max(0, Number(stats?.logical_bytes) || 0)
  const physical = Math.max(0, Number(stats?.physical_bytes) || 0)
  const reclaimable = Math.max(0, Number(stats?.wasted_bytes_estimate) || 0)
  const base = { logical, physical, saved: 0, overhead: 0, storedPct: 0, ratio: 1, reclaimable }
  if (logical === 0) return { ...base, kind: 'empty' }
  const ratio = Number(stats?.dedup_ratio) > 0 ? Number(stats.dedup_ratio) : logical / Math.max(physical, 1)
  if (physical > logical) {
    return { ...base, kind: 'overhead', ratio, overhead: physical - logical, storedPct: 100 }
  }
  const storedPct = (physical / logical) * 100
  // Below 1.05x a one-decimal ratio would read "1.0x": no meaningful saving.
  if (ratio < 1.05) return { ...base, kind: 'even', ratio, storedPct }
  return { ...base, kind: 'savings', ratio, saved: logical - physical, storedPct }
}
