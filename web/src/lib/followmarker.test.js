import { describe, it, expect } from 'vitest'
import { createFollowMarker, countNewEntries } from './followmarker.js'

const at = (id, ts) => ({ id, ts })
const T1 = '2026-08-18T21:00:00.000Z'
const T2 = '2026-08-18T21:00:01.000Z'
const T3 = '2026-08-18T21:00:02.000Z'

describe('createFollowMarker', () => {
  it('returns null for an empty buffer', () => {
    expect(createFollowMarker([])).toBeNull()
  })

  it('snapshots the newest entry ts and every id in the buffer', () => {
    const entries = [at(1, T1), at(2, T2), at(3, T3)]
    const marker = createFollowMarker(entries)
    expect(marker.ts).toBe(T3)
    expect(marker.ids[1]).toBe(true)
    expect(marker.ids[2]).toBe(true)
    expect(marker.ids[3]).toBe(true)
  })
})

describe('countNewEntries', () => {
  it('counts zero when nothing arrived after the marker', () => {
    const entries = [at(1, T1), at(2, T2), at(3, T3)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries(entries, marker)).toBe(0)
  })

  it('counts entries with a strictly newer timestamp', () => {
    const entries = [at(1, T1), at(2, T2), at(3, T3)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries([...entries, at(4, '2026-08-18T21:00:03.000Z')], marker)).toBe(1)
  })

  it('does not count pre-existing entries that share the marker timestamp', () => {
    // Two entries landed in the same second; the marker is the second one.
    // The first is already visible — the old ts-tie-break counted it as
    // "new" whenever the marker identity changed (the #328 off-by-one).
    const entries = [at(1, T3), at(2, T3)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries(entries, marker)).toBe(0)
  })

  it('counts a genuinely new entry arriving within the marker second', () => {
    const entries = [at(1, T3), at(2, T3)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries([...entries, at(3, T3)], marker)).toBe(1)
  })

  it('does not count an already-seen id even when its ts advances past the marker', () => {
    // A WS run_log can replace an already-seen summary/activity row (same id)
    // while carrying a strictly-newer timestamp. Its id is already in the
    // marker, so it must not read as new — the #328 phantom "1 new entry".
    const entries = [at(1, T1), at(2, T2)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries([at(1, T1), at(2, T3)], marker)).toBe(0)
  })

  it('does not count older entries prepended by load-older', () => {
    const entries = [at(3, T3)]
    const marker = createFollowMarker(entries)
    expect(countNewEntries([at(1, T1), at(2, T2), at(3, T3)], marker)).toBe(0)
  })

  it('returns 0 when there is no marker', () => {
    expect(countNewEntries([at(1, T1)], null)).toBe(0)
  })
})
