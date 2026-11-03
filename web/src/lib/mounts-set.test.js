import { describe, expect, it, vi } from 'vitest'

vi.mock('./api.js', () => ({ api: {} }))
const { getMounts, setActiveMounts } = await import('./mounts.svelte.js')

describe('setActiveMounts', () => {
  it('keeps the same list when nothing changed, so polling causes no re-render', () => {
    setActiveMounts([{ id: 1 }])
    const before = getMounts().list
    setActiveMounts([{ id: 1 }])
    expect(getMounts().list).toBe(before)
  })

  it('replaces the list when it changed, and treats nothing as empty', () => {
    setActiveMounts([{ id: 1 }])
    setActiveMounts([{ id: 2 }])
    expect(getMounts().list.map((m) => m.id)).toEqual([2])
    setActiveMounts(null)
    expect(getMounts().list).toEqual([])
  })
})
