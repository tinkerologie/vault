import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createDashboardLayout, DEFAULT_LAYOUT, SPAN_OPTIONS } from './dashboardLayout.svelte.js'

// Dashboard layout store tests (issue #458). localStorage is faked.

const KEY = 'vault:dash:layout:v1'
const VALID = [...DEFAULT_LAYOUT, 'extra', 'other']

let data
let failSet

beforeEach(() => {
  data = {}
  failSet = false
  vi.stubGlobal('localStorage', {
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => {
      if (failSet) throw new Error('quota')
      data[k] = v
    },
  })
})

afterEach(() => vi.unstubAllGlobals())

const stored = () => JSON.parse(data[KEY])

describe('loading', () => {
  it('uses the default layout when nothing is stored', () => {
    const l = createDashboardLayout(VALID)
    expect(l.order).toEqual(DEFAULT_LAYOUT)
    expect(l.spans).toEqual({})
    expect(l.editMode).toBe(false)
  })

  it('accepts the legacy bare-array format, dropping unknown and duplicate ids', () => {
    data[KEY] = JSON.stringify(['jobs', 'bogus', 'jobs', 42, 'health'])
    const l = createDashboardLayout(VALID)
    expect(l.order).toEqual(['jobs', 'health'])
    expect(l.spans).toEqual({})
  })

  it('accepts the { order, spans } format and filters invalid spans', () => {
    data[KEY] = JSON.stringify({ order: ['extra'], spans: { extra: 6, health: 5, bogus: 3 } })
    const l = createDashboardLayout(VALID)
    expect(l.order).toEqual(['extra'])
    expect(l.spans).toEqual({ extra: 6 })
  })

  it('falls back to the defaults when the stored order is empty or not an array', () => {
    data[KEY] = JSON.stringify({ order: 'nope' })
    expect(createDashboardLayout(VALID).order).toEqual(DEFAULT_LAYOUT)
    data[KEY] = JSON.stringify(['bogus'])
    expect(createDashboardLayout(VALID).order).toEqual(DEFAULT_LAYOUT)
  })

  it('falls back to the defaults on corrupt JSON', () => {
    data[KEY] = '{not json'
    const l = createDashboardLayout(VALID)
    expect(l.order).toEqual(DEFAULT_LAYOUT)
    expect(l.spans).toEqual({})
  })
})

describe('mutations', () => {
  it('toggles edit mode', () => {
    const l = createDashboardLayout(VALID)
    l.toggleEdit()
    expect(l.editMode).toBe(true)
    l.toggleEdit()
    expect(l.editMode).toBe(false)
  })

  it('adds only valid, not-yet-present tiles and persists', () => {
    const l = createDashboardLayout(VALID)
    l.add('extra')
    expect(l.order.at(-1)).toBe('extra')
    expect(stored().order.at(-1)).toBe('extra')
    l.add('extra')
    l.add('bogus')
    expect(l.order.filter((x) => x === 'extra')).toHaveLength(1)
    expect(l.order).not.toContain('bogus')
  })

  it('removes a tile together with its span override', () => {
    const l = createDashboardLayout(VALID)
    l.resize('jobs', 6, 1)
    expect(l.spans.jobs).toBe(12)
    l.remove('jobs')
    expect(l.order).not.toContain('jobs')
    expect(l.spans.jobs).toBeUndefined()
    l.remove('health')
    expect(stored().order).not.toContain('health')
  })

  it('moves a tile by index and ignores out-of-range or no-op moves', () => {
    const l = createDashboardLayout(VALID)
    l.move(0, 2)
    expect(l.order.slice(0, 3)).toEqual(['protected', 'nextrun', 'health'])
    const before = [...l.order]
    l.move(1, 1)
    l.move(-1, 0)
    l.move(0, 99)
    l.move(99, 0)
    l.move(0, -1)
    expect(l.order).toEqual(before)
  })

  it('nudges a tile up/down and ignores moves past either end or unknown ids', () => {
    const l = createDashboardLayout(VALID)
    l.moveBy('protected', -1)
    expect(l.order.slice(0, 2)).toEqual(['protected', 'health'])
    const before = [...l.order]
    l.moveBy('protected', -1)
    l.moveBy(l.order.at(-1), 1)
    l.moveBy('bogus', 1)
    expect(l.order).toEqual(before)
  })

  it('cycles tile widths through SPAN_OPTIONS, clamped at both ends and by maxSpan', () => {
    const l = createDashboardLayout(VALID)
    l.resize('jobs', 3, -1)
    expect(l.spans.jobs).toBe(3)
    l.resize('jobs', 3, 1)
    expect(l.spans.jobs).toBe(4)
    l.resize('jobs', 12, 1)
    expect(l.spans.jobs).toBe(12)
    l.resize('jobs', 4, 1, 4)
    expect(l.spans.jobs).toBe(4)
    // A non-standard current width snaps to the next allowed option first.
    l.resize('jobs', 5, 1)
    expect(l.spans.jobs).toBe(SPAN_OPTIONS[3])
    // Wider than any option: starts from the narrowest.
    l.resize('jobs', 20, 1)
    expect(l.spans.jobs).toBe(4)
  })

  it('resets to the default layout', () => {
    const l = createDashboardLayout(VALID)
    l.add('extra')
    l.resize('jobs', 6, 1)
    l.reset()
    expect(l.order).toEqual(DEFAULT_LAYOUT)
    expect(l.spans).toEqual({})
    expect(stored()).toEqual({ order: DEFAULT_LAYOUT, spans: {} })
  })

  it('keeps working in memory when persistence fails', () => {
    failSet = true
    const l = createDashboardLayout(VALID)
    expect(() => l.add('extra')).not.toThrow()
    expect(l.order).toContain('extra')
  })
})
