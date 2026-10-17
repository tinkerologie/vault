import { describe, it, expect } from 'vitest'
import { createLogWindow, scrollAction } from './logwindow.svelte.js'

const ROW = 20 // px per rendered row in the fake scroll box

const mk = (n, prefix = 'k') => Array.from({ length: n }, (_, i) => ({ kind: 'entry', key: `${prefix}${i}` }))

// A window over `all` with a fake scroll box whose height follows the
// rendered rows, like the real console. `all` can be swapped to simulate new
// lines arriving.
function setup(n, { withBox = true } = {}) {
  const state = { all: mk(n), ticks: 0 }
  let win
  const el = {
    clientHeight: 400,
    st: 0,
    writes: 0,
    get scrollHeight() {
      return win.rows.length * ROW
    },
    get scrollTop() {
      return this.st
    },
    set scrollTop(v) {
      this.writes++
      this.st = Math.max(0, Math.min(v, this.scrollHeight - this.clientHeight))
    },
  }
  win = createLogWindow({
    rows: () => state.all,
    box: () => (withBox ? el : null),
    tick: async () => { state.ticks++ },
  })
  return { state, el, win }
}

// The key of the row at the top of the viewport.
const rowAtTop = (el, win) => win.rows[Math.floor(el.st / ROW)]?.key

describe('scrollAction', () => {
  const base = { atTop: false, nearOldest: false, follow: true, hiddenAbove: false, hiddenBelow: false, hasMore: false, loadingOlder: false }

  it('raises when the top is reached with newer rows hidden, and does nothing else', () => {
    expect(scrollAction({ ...base, atTop: true, hiddenAbove: true, follow: false, nearOldest: true, hiddenBelow: true })).toEqual({ raise: true })
  })

  it('enters the live edge at the top when not following', () => {
    expect(scrollAction({ ...base, atTop: true, follow: false })).toEqual({ enterLive: true })
    expect(scrollAction({ ...base, atTop: true, follow: true })).toEqual({})
  })

  it('leaves the live edge away from the top while following', () => {
    expect(scrollAction({ ...base, follow: true })).toEqual({ leaveLive: true })
    expect(scrollAction({ ...base, follow: false })).toEqual({})
  })

  it('renders hidden rows before fetching, and fetches only when it can', () => {
    expect(scrollAction({ ...base, follow: false, nearOldest: true, hiddenBelow: true, hasMore: true })).toEqual({ grow: true })
    expect(scrollAction({ ...base, follow: false, nearOldest: true, hasMore: true })).toEqual({ loadOlder: true })
    expect(scrollAction({ ...base, follow: false, nearOldest: true, hasMore: true, loadingOlder: true })).toEqual({})
    expect(scrollAction({ ...base, follow: false, nearOldest: true, hasMore: false })).toEqual({})
  })
})

describe('createLogWindow', () => {
  it('renders the newest 200 rows by default', () => {
    const { win } = setup(1000)
    expect(win.rows).toHaveLength(200)
    expect(win.rows[0].key).toBe('k0')
    expect(win.hiddenBelow).toBe(true)
    expect(win.hiddenAbove).toBe(false)
  })

  describe('write', () => {
    it('arms the suppress flag only for a write that moves', () => {
      const { el, win } = setup(1000)
      win.write(0) // already there: no write, no flag
      expect(el.writes).toBe(0)
      expect(win.consumeSuppressed()).toBe(false)

      win.write(100)
      expect(el.st).toBe(100)
      expect(win.consumeSuppressed()).toBe(true)
      expect(win.consumeSuppressed()).toBe(false) // one-shot

      win.write(1e9) // clamped to the bottom
      expect(el.st).toBe(200 * ROW - 400)
    })

    it('does nothing without a box', async () => {
      const { win } = setup(1000, { withBox: false })
      win.write(100)
      expect(win.consumeSuppressed()).toBe(false)
      await win.grow()
      await win.raise()
      expect(await win.jumpToOldest()).toBe(false)
      expect(win.rows).toHaveLength(200)
    })
  })

  describe('grow', () => {
    it('extends by 200, then slides at 600 keeping the reading row in place', async () => {
      const { el, win } = setup(2000)
      await win.grow()
      expect(win.rows).toHaveLength(400)
      await win.grow()
      expect(win.rows).toHaveLength(600)

      el.st = 580 * ROW // reading near the bottom of the window
      const reading = rowAtTop(el, win)
      await win.grow()
      expect(win.rows).toHaveLength(600) // capped: slid, not grew
      expect(win.rows[0].key).toBe('k200')
      expect(win.hiddenAbove).toBe(true)
      expect(rowAtTop(el, win)).toBe(reading)
      expect(win.consumeSuppressed()).toBe(true)
    })

    it('ignores a concurrent call', async () => {
      const { win } = setup(2000)
      await Promise.all([win.grow(), win.grow()])
      expect(win.rows).toHaveLength(400)
    })
  })

  describe('raise', () => {
    it('renders newer rows above, keeps the reading row, and trims the bottom', async () => {
      const { el, win } = setup(2000)
      await win.jumpToOldest()
      expect(win.rows[win.rows.length - 1].key).toBe('k1999')
      for (let i = 0; i < 3; i++) {
        el.st = 0
        const reading = rowAtTop(el, win)
        await win.raise()
        expect(rowAtTop(el, win)).toBe(reading)
      }
      expect(win.rows.length).toBeLessThanOrEqual(600)
      expect(win.hiddenBelow).toBe(true)
    })

    it('pins the top edge to the newest row when it gets there', async () => {
      const { state, el, win } = setup(300)
      await win.jumpToOldest() // rows 100..299
      el.st = 0
      await win.raise() // back to row 0, pinned there
      expect(win.rows[0].key).toBe('k0')
      expect(win.hiddenAbove).toBe(false)
      // New lines arriving stay above the window instead of rendering.
      state.all = [...mk(5, 'new'), ...state.all]
      expect(win.rows[0].key).toBe('k0')
      expect(win.hiddenAbove).toBe(true)
    })

    it('ignores a concurrent call', async () => {
      const { el, win } = setup(2000)
      await win.jumpToOldest()
      el.st = 0
      await Promise.all([win.raise(), win.raise()])
      expect(win.rows).toHaveLength(400)
    })

    it('copes with every row disappearing', async () => {
      const { state, win } = setup(300)
      await win.jumpToOldest()
      state.all = []
      await win.raise()
      expect(win.rows).toEqual([])
    })
  })

  it('never collapses to an empty window when anchors are re-keyed', async () => {
    const { state, win } = setup(2000)
    await win.jumpToOldest() // top k1800, bottom k1999
    // A collapse toggle folds the top anchor away and moves the bottom
    // anchor's row far up the list.
    state.all = [...mk(900, 'x'), { kind: 'entry', key: 'k1999' }, ...mk(299, 'y')]
    expect(win.rows).toHaveLength(200)
    expect(win.rows[win.rows.length - 1].key).toBe('k1999')
  })

  describe('jumpToOldest', () => {
    it('renders only the oldest rows and scrolls to the bottom', async () => {
      const { el, win } = setup(2000)
      expect(await win.jumpToOldest()).toBe(true)
      expect(win.rows).toHaveLength(200)
      expect(win.rows[0].key).toBe('k1800')
      expect(win.hiddenBelow).toBe(false)
      expect(el.st).toBe(el.scrollHeight - el.clientHeight)
    })

    it('leaves scrolling to the caller when nothing is hidden', async () => {
      const { win } = setup(150)
      expect(await win.jumpToOldest()).toBe(false)
    })
  })

  describe('pin and reset', () => {
    it('pins both edges so arriving lines neither render nor push rows out', () => {
      const { state, win } = setup(1000)
      win.pin()
      state.all = [...mk(30, 'new'), ...state.all]
      expect(win.rows[0].key).toBe('k0')
      expect(win.rows[win.rows.length - 1].key).toBe('k199')
      expect(win.hiddenAbove).toBe(true)

      win.reset()
      expect(win.rows[0].key).toBe('new0')
      expect(win.rows).toHaveLength(200)
    })

    it('keeps existing anchors and skips an empty window', async () => {
      const { state, win } = setup(1000)
      await win.grow()
      win.pin()
      expect(win.rows).toHaveLength(400)
      const empty = setup(0)
      empty.win.pin()
      expect(empty.win.rows).toEqual([])
      state.all = []
      win.pin()
      expect(win.rows).toEqual([])
    })
  })

  describe('date rows (#507)', () => {
    // Newest-first like buildRows: today's header, then today's entries.
    const day = (n) => [{ kind: 'date', key: 'day-today' }, ...mk(n)]

    it('anchors the pinned top on an entry, so same-day lines stay above', () => {
      const { state, win } = setup(0)
      state.all = day(500)
      win.pin()
      // A new line for today lands under the header, at index 1.
      state.all = [state.all[0], { kind: 'entry', key: 'new' }, ...state.all.slice(1)]
      expect(win.rows.map((r) => r.key)).not.toContain('new')
      expect(win.rows[0].key).toBe('day-today') // header still shown as context
      expect(win.hiddenAbove).toBe(true)
    })

    it('does not count a context date header as hidden newer rows', async () => {
      const { state, el, win } = setup(0)
      state.all = day(300)
      await win.jumpToOldest()
      el.st = 0
      await win.raise() // back to the first entry, under today's header
      expect(win.rows[0].key).toBe('day-today')
      expect(win.rows[1].key).toBe('k0')
      expect(win.hiddenAbove).toBe(false)
    })

    it('keeps an existing top anchor when pinning again', async () => {
      const { state, win } = setup(0)
      state.all = day(500)
      win.pin()
      const first = win.rows.map((r) => r.key)
      state.all = [state.all[0], { kind: 'entry', key: 'new' }, ...state.all.slice(1)]
      win.pin() // top already set: unchanged, "new" still hidden
      expect(win.rows.map((r) => r.key)).toEqual(first)
    })
  })

  describe('afterOlderFetch', () => {
    it('grows only when the reader is at the edge and rows are hidden', async () => {
      const { win } = setup(1000)
      await win.afterOlderFetch(false)
      expect(win.rows).toHaveLength(200)
      await win.afterOlderFetch(true)
      expect(win.rows).toHaveLength(400)
      const small = setup(100)
      await small.win.afterOlderFetch(true)
      expect(small.win.rows).toHaveLength(100)
    })
  })
})
