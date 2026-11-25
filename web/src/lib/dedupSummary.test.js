import { describe, it, expect } from 'vitest'
import { dedupSummary } from './dedupSummary.js'

const MB = 1024 * 1024
const GB = 1024 * MB

describe('dedupSummary', () => {
  it('reports real savings above 1x as before', () => {
    const s = dedupSummary({ logical_bytes: 10 * GB, physical_bytes: 4 * GB, dedup_ratio: 2.5 })
    expect(s.kind).toBe('savings')
    expect(s.ratio).toBe(2.5)
    expect(s.saved).toBe(6 * GB)
    expect(s.storedPct).toBeCloseTo(40)
  })

  it('treats more stored than logical data as overhead, never savings', () => {
    // The live case from #713: "0.1x (451.3 MB -> 4.3 GB) · Saved 0 B".
    const s = dedupSummary({ logical_bytes: 451.3 * MB, physical_bytes: 4.3 * GB, dedup_ratio: 0.1, wasted_bytes_estimate: GB })
    expect(s.kind).toBe('overhead')
    expect(s.saved).toBe(0)
    expect(s.overhead).toBeCloseTo(4.3 * GB - 451.3 * MB)
    expect(s.reclaimable).toBe(GB)
  })

  it('does not call a ratio that rounds to 1.0x a saving', () => {
    expect(dedupSummary({ logical_bytes: 100, physical_bytes: 98, dedup_ratio: 1.02 }).kind).toBe('even')
  })

  it('has nothing to say before the first backup', () => {
    expect(dedupSummary({ logical_bytes: 0, physical_bytes: 4096 }).kind).toBe('empty')
    expect(dedupSummary(undefined).kind).toBe('empty')
  })

  it('derives the ratio when the server omits it', () => {
    expect(dedupSummary({ logical_bytes: 300, physical_bytes: 100 }).ratio).toBe(3)
  })
})
