// Component tests for Logs.svelte's scroll and lifecycle wiring (#509).
//
// The window logic itself lives in lib/logwindow.svelte.js and is unit-tested
// there. These tests render the real page against a controllable store
// fixture and stubbed console geometry, and drive it only through rendered
// events (scroll, click) and store updates — no private handler is exported
// or called.
//
// Defensive guards that rendered interactions cannot reach, and the caller
// that screens each one:
//   - loadOlderRows: `!box`, `store.loadingOlder` and `!store.hasMore` at
//     entry. Its only caller, handleScroll, has already returned when there
//     is no box or a load is in flight, and scrollAction only asks for
//     loadOlder when hasMore is true; nothing awaits in between.
//   - jumpToOldest: `!box` at entry. Its button renders inside the same
//     {:else} branch as the console, so the box always exists when it is
//     clicked.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import { createFixtureStore } from '../test-support/logs-store.svelte.js'
import { makeEntries } from '../test-support/logs-entries.js'
import { flush, installScrollBox, offsetFromBottom, userScroll } from '../test-support/scroll-box.js'

const current = vi.hoisted(() => ({ fixture: null }))

vi.mock('../lib/unifiedlog.svelte.js', () => ({
  createUnifiedLogStore: () => current.fixture.store,
}))
vi.mock('../lib/clipboard.js', () => ({ copyText: vi.fn(async () => true) }))
vi.mock('../lib/api.js', async () => (await import('../test-support/scroll-box.js')).apiModuleMock())

import Logs from './Logs.svelte'
import { copyText } from '../lib/clipboard.js'
import { logLine } from '../lib/logline.js'

let geo

beforeEach(() => {
  geo = installScrollBox({ clientHeight: 400, rowHeight: 20 })
})

afterEach(() => {
  geo.restore()
  current.fixture = null
})

/** Render Logs against a fresh fixture store; `before` can prime it first. */
async function mountLogs({ entries = makeEntries(50), hasMore = false, before } = {}) {
  current.fixture = createFixtureStore({ entries, hasMore })
  before?.(current.fixture.control)
  const view = render(Logs)
  await flush()
  return { ...view, ...current.fixture, console: () => document.querySelector('[role="log"]') }
}

/** Number of log rows currently rendered. */
const rowCount = () => document.querySelectorAll('[data-entry-id]').length
/** The "Jump to present" button, or null while following the live edge. */
const jumpToPresent = () => screen.queryByTitle('Jump to present')

describe('mount without a console', () => {
  it('renders the empty state and never scroll-loads', async () => {
    const { store, console: box } = await mountLogs({ entries: [], hasMore: true })
    expect(screen.getByText('No logs')).toBeTruthy()
    expect(box()).toBeNull()
    expect(store.setupWs).toHaveBeenCalledOnce()
    expect(store.loadAll).toHaveBeenCalledOnce()
    expect(store.loadOlder).not.toHaveBeenCalled()
  })

  it('renders the error state and never scroll-loads', async () => {
    const { store, console: box } = await mountLogs({
      hasMore: true,
      before: (c) => c.setError('Failed to load logs'),
    })
    expect(screen.getByText('Failed to load logs')).toBeTruthy()
    expect(box()).toBeNull()
    expect(store.setupWs).toHaveBeenCalledOnce()
    expect(store.loadOlder).not.toHaveBeenCalled()
  })
})

describe('handleScroll', () => {
  it('leaves and re-enters the live edge as the reader scrolls', async () => {
    const { console: box } = await mountLogs()
    expect(jumpToPresent()).toBeNull()

    await userScroll(geo, box(), 200)
    expect(jumpToPresent()).toBeTruthy()

    await userScroll(geo, box(), 10) // inside the 48px live-edge band
    expect(jumpToPresent()).toBeNull()
  })

  it('loads one older page near the oldest edge when more history exists', async () => {
    const { store, console: box } = await mountLogs({ hasMore: true })
    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(store.loadOlder).toHaveBeenCalledOnce()
  })

  it('does not fetch at the end of history and shows the end marker', async () => {
    const { store, console: box } = await mountLogs({ hasMore: false })
    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(store.loadOlder).not.toHaveBeenCalled()
    expect(document.querySelector('[data-end-marker]')).toBeTruthy()
  })

  it('does not start a second fetch while one is in flight', async () => {
    const { store, control, console: box } = await mountLogs({ hasMore: true })
    control.holdOlder()
    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(store.loadingOlder).toBe(true)

    await userScroll(geo, box(), offsetFromBottom(geo, box(), 50))
    expect(store.loadOlder).toHaveBeenCalledOnce()

    control.releaseOlder()
    await flush()
  })

  it('still renders loaded rows hidden below while a fetch is in flight', async () => {
    const { store, control, console: box } = await mountLogs({ entries: makeEntries(500), hasMore: true })
    const before = rowCount()
    expect(before).toBeLessThan(500)

    // The background full-history load keeps loadingOlder set for long
    // stretches; reaching the window's edge must not wait for it.
    control.holdOlder()
    store.loadOlder()
    await flush()
    expect(store.loadingOlder).toBe(true)

    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(rowCount()).toBeGreaterThan(before)
    expect(store.loadOlder).toHaveBeenCalledOnce() // only the background call

    control.releaseOlder()
    await flush()
  })

  it('ignores the scroll event of its own programmatic write', async () => {
    const { store, console: box } = await mountLogs({ entries: makeEntries(500), hasMore: true })
    await fireEvent.click(screen.getByLabelText('Jump to oldest log'))
    await flush()
    expect(jumpToPresent()).toBeTruthy()

    // jumpToOldest wrote scrollTop itself. The browser's scroll event for
    // that write lands near the oldest edge with more history available, yet
    // must neither fetch nor change follow.
    await fireEvent.scroll(box())
    await flush()
    expect(store.loadOlder).not.toHaveBeenCalled()
    expect(jumpToPresent()).toBeTruthy()
  })
})

describe('loadOlderRows', () => {
  it('tolerates the console disappearing while the fetch is in flight', async () => {
    const { store, control, console: box } = await mountLogs({ hasMore: true })
    control.holdOlder()
    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(store.loadOlder).toHaveBeenCalledOnce()

    // The fetch fails while pending: the error state replaces the console,
    // so the post-fetch `if (box)` sees no box.
    control.setError('Failed to load older logs')
    await flush()
    expect(box()).toBeNull()

    control.releaseOlder()
    await flush()
    expect(screen.getByText('Failed to load older logs')).toBeTruthy()
  })
})

describe('scroll compensation', () => {
  it('pins the live edge while following', async () => {
    const { control } = await mountLogs()
    geo.writes.length = 0
    control.addNewer(makeEntries(1, { start: 1000 }))
    await flush()
    expect(geo.writes).toContain(0)
  })

  it('keeps the reading position when a line lands above it', async () => {
    const { control, console: box } = await mountLogs()
    await userScroll(geo, box(), 300)
    expect(jumpToPresent()).toBeTruthy()
    geo.writes.length = 0

    // A streamed run-log line is inserted in chronological position, inside
    // the rendered window and above the viewport, growing it by one row.
    const [mid] = makeEntries(1, { start: 47 })
    mid.ts = new Date(Date.parse(mid.ts) + 500).toISOString()
    mid.message = 'Inserted line'
    const list = [...storeEntries()]
    list.push(mid)
    list.sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts))
    control.setEntries(list)
    await flush()

    expect(geo.writes).toEqual([300 + geo.rowHeight])
    expect(box().scrollTop).toBe(300 + geo.rowHeight)

    // The browser's scroll event for that write is swallowed: follow stays off.
    await fireEvent.scroll(box())
    await flush()
    expect(jumpToPresent()).toBeTruthy()
  })

  it('does not write when nothing grew or the reader is at the top', async () => {
    const { control, console: box } = await mountLogs()
    await userScroll(geo, box(), 300)
    geo.writes.length = 0

    // Same number of rows: no growth.
    control.setEntries(storeEntries().map((e) => ({ ...e })))
    await flush()
    expect(geo.writes).toEqual([])

    // Growth with the reader at scrollTop 0 (follow already off).
    geo.position(box(), 0)
    const [mid] = makeEntries(1, { start: 47 })
    mid.ts = new Date(Date.parse(mid.ts) + 500).toISOString()
    control.setEntries([...storeEntries(), mid].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts)))
    await flush()
    expect(geo.writes).toEqual([])
  })

  it('leaves scrollTop alone while an older batch appends below', async () => {
    const { store, control, console: box } = await mountLogs({ hasMore: true })
    control.holdOlder()
    control.setOlderBatch(makeEntries(30, { start: -30 }))
    await userScroll(geo, box(), offsetFromBottom(geo, box(), 100))
    expect(store.loadOlder).toHaveBeenCalledOnce()
    geo.writes.length = 0

    control.releaseOlder()
    await flush()
    expect(rowCount()).toBe(80)
    expect(geo.writes).toEqual([])
  })
})

describe('jumpToOldest', () => {
  it('renders the oldest rows and turns follow off when they were not on the page', async () => {
    await mountLogs({ entries: makeEntries(500) })
    expect(screen.queryByText('Log line 0')).toBeNull()

    await fireEvent.click(screen.getByLabelText('Jump to oldest log'))
    await flush()
    expect(screen.getByText('Log line 0')).toBeTruthy()
    expect(jumpToPresent()).toBeTruthy()
    expect(geo.scrollToCalls).toEqual([])
  })

  it('smooth-scrolls to the bottom when the whole history is rendered', async () => {
    const { console: box } = await mountLogs()
    await fireEvent.click(screen.getByLabelText('Jump to oldest log'))
    await flush()
    expect(geo.scrollToCalls).toEqual([{ top: box().scrollHeight, behavior: 'smooth' }])
  })
})

describe('lifecycle', () => {
  it('unsubscribes and disposes the store on unmount', async () => {
    const { store, control, unmount } = await mountLogs()
    unmount()
    expect(control.unsubscribe).toHaveBeenCalledOnce()
    expect(store.dispose).toHaveBeenCalledOnce()
  })

  it('stops the mount sequence when unmounted during the initial load', async () => {
    let held
    const { store, unmount } = await mountLogs({
      hasMore: true,
      before: (c) => { held = c.holdLoad() },
    })
    unmount()
    held.resolve()
    await flush()
    expect(store.dispose).toHaveBeenCalledOnce()
    expect(store.loadOlder).not.toHaveBeenCalled() // fillViewport never ran
    expect(store.loadAll).not.toHaveBeenCalled()
    expect(store.setupWs).not.toHaveBeenCalled()
  })
})

describe('lifecycle while filling the viewport', () => {
  it('stops the mount sequence when unmounted mid-fill', async () => {
    // A short history leaves the console unscrollable, so the mount fills it
    // from older pages; unmounting during that fill must stop the sequence.
    current.fixture = createFixtureStore({ entries: makeEntries(5), hasMore: true })
    current.fixture.control.holdOlder()
    const { unmount } = render(Logs)
    await flush()
    const { store, control } = current.fixture
    expect(store.loadOlder).toHaveBeenCalledOnce()

    unmount()
    control.releaseOlder()
    await flush()
    expect(store.loadOlder).toHaveBeenCalledOnce()
    expect(store.loadAll).not.toHaveBeenCalled()
    expect(store.setupWs).not.toHaveBeenCalled()
  })
})

describe('problem rows (#534)', () => {
  const problems = () => makeEntries(3).map((e, i) => ({
    ...e,
    level: i === 1 ? 'warning' : 'warn',
    category: 'health',
    message: `Health check, container=c${i}, status=unhealthy, error=Timed out, retrying`,
    meta: { container_name: `c${i}`, status: 'unhealthy', error: 'SENTINEL-STRUCTURED' },
  }))
  const text = () => document.querySelector('[role="log"]').textContent
  const row = (entry) => document.querySelector(`[data-entry-id="${entry.id}"]`)

  it('shows the summary with Details off and the canonical text with it on', async () => {
    await mountLogs({ entries: problems() })
    expect(text()).toContain('Health check · c0 · unhealthy · Timed out, retrying')
    await fireEvent.click(screen.getByRole('button', { name: /^Details:/ }))
    await flush()
    expect(text()).not.toContain('Health check · c0')
    expect(text()).toContain('container=c0, status=unhealthy, error=Timed out, retrying')
  })

  it('never renders structured details, in the row or opened', async () => {
    const entries = problems()
    await mountLogs({ entries })
    await fireEvent.click(row(entries[2]).querySelector('[aria-expanded]'))
    await flush()
    expect(document.querySelector('[data-log-details]')).toBeTruthy()
    expect(document.body.innerHTML).not.toContain('SENTINEL')
  })

  it('opens one row at a time, and closes it again', async () => {
    const entries = problems()
    await mountLogs({ entries })
    const toggle = row(entries[1]).querySelector('[aria-expanded]')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(toggle.hasAttribute('aria-controls')).toBe(false)

    await fireEvent.click(toggle)
    await flush()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(document.getElementById(toggle.getAttribute('aria-controls'))).toBeTruthy()
    expect(row(entries[0]).querySelector('[aria-expanded]').getAttribute('aria-expanded')).toBe('false')
    expect(document.querySelectorAll('[data-log-details]')).toHaveLength(1)

    await fireEvent.click(toggle)
    await flush()
    expect(document.querySelectorAll('[data-log-details]')).toHaveLength(0)
  })

  it('offers no toggle on a bare row', async () => {
    const entries = makeEntries(2)
    await mountLogs({ entries })
    expect(row(entries[0]).querySelector('[aria-expanded]')).toBeNull()
  })

  it('pauses auto-follow on opening, and holds the anchor line as lines arrive', async () => {
    const entries = problems()
    const { control } = await mountLogs({ entries })
    expect(jumpToPresent()).toBeNull()
    await fireEvent.click(row(entries[1]).querySelector('[aria-expanded]'))
    await flush()
    expect(jumpToPresent()).toBeTruthy()

    geo.writes.length = 0
    control.addNewer(makeEntries(1, { start: 1000 }))
    await flush()
    expect(geo.writes).not.toContain(0) // the follow pin is off
    expect(screen.getByText('1 new entry')).toBeTruthy()
  })

  it('falls back to growth compensation when the anchor line has gone', async () => {
    const entries = problems()
    const { control, console: box } = await mountLogs({ entries: [...makeEntries(50), ...entries] })
    await fireEvent.click(row(entries[1]).querySelector('[aria-expanded]'))
    await flush()
    await userScroll(geo, box(), 300)

    // The whole buffer is replaced: nothing it anchored on is left.
    control.setEntries(makeEntries(80, { start: 2000 }))
    await flush()
    expect(rowCount()).toBe(80)
  })

  it('lists a long fold in steps', async () => {
    const entries = makeEntries(130).map((e) => ({ ...e, message: 'Tick' }))
    await mountLogs({ entries })
    await fireEvent.click(screen.getByRole('button', { name: /^Details for Tick, .*, 130 entries$/ }))
    await flush()
    expect(document.querySelectorAll('[data-log-details] li')).toHaveLength(100)
    await fireEvent.click(screen.getByRole('button', { name: 'Show 30 more of 30' }))
    await flush()
    expect(document.querySelectorAll('[data-log-details] li')).toHaveLength(130)
  })

  it('marks the row by pointer and by focus, and triple-click selects it', async () => {
    const entries = problems()
    await mountLogs({ entries })
    const target = row(entries[0])
    await fireEvent.pointerDown(target)
    await flush()
    expect(target.className).toContain('bg-vault/15')

    await fireEvent.focusIn(row(entries[1]).querySelector('[aria-label="Copy log line"]'))
    await flush()
    expect(row(entries[1]).className).toContain('bg-vault/15')
    expect(target.className).not.toContain('bg-vault/15')

    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((fn) => { fn(); return 1 })
    const selected = vi.spyOn(window, 'getSelection')
    await fireEvent.click(target, { detail: 1 })
    expect(selected).not.toHaveBeenCalled()
    await fireEvent.click(target, { detail: 3 })
    expect(window.getSelection().toString()).toContain('Health check')
    await fireEvent.click(document.querySelector('[data-end-marker]'), { detail: 3 })
    raf.mockRestore()
    selected.mockRestore()
  })
})

describe('copy stays verbatim (#534)', () => {
  const folded = () => makeEntries(3).map((e, i) => ({ ...e, level: 'warn', message: `Health check, container=plex, attempt=${i}, status=failed, error=refused` }))

  it('row copy yields every folded line, newest first', async () => {
    const entries = folded()
    copyText.mockClear()
    await mountLogs({ entries })
    await fireEvent.click(screen.getByRole('button', { name: 'Copy 3 log lines' }))
    await flush()
    expect(copyText).toHaveBeenCalledWith([...entries].reverse().map(logLine).join('\n'))
    expect(document.querySelector('.log-row-copy .text-success')).toBeTruthy()
  })

  it('Copy all holds every filtered entry, outside the render window too', async () => {
    const entries = makeEntries(500)
    copyText.mockClear()
    await mountLogs({ entries })
    expect(rowCount()).toBeLessThan(500)
    await fireEvent.click(screen.getByTitle('Copy all filtered logs'))
    await flush()
    expect(copyText).toHaveBeenCalledWith(entries.map(logLine).join('\n'))
  })
})

describe('Problems filter (#534)', () => {
  const entries = () => makeEntries(4).map((e, i) => ({ ...e, level: ['info', 'warn', 'warning', 'error'][i] }))
  const toggle = () => screen.getByRole('button', { name: 'Problems' })

  it('toggles the level filter on and off, in step with the select', async () => {
    const { store } = await mountLogs({ entries: entries() })
    const select = screen.getByRole('combobox', { name: 'Level' })
    await fireEvent.click(toggle())
    await flush()
    expect(store.levelFilter).toBe('problems')
    expect(toggle().getAttribute('aria-pressed')).toBe('true')
    expect(select.value).toBe('problems')
    expect(rowCount()).toBe(3)

    await fireEvent.click(toggle())
    await flush()
    expect(store.levelFilter).toBe('')
    expect(toggle().getAttribute('aria-pressed')).toBe('false')
    expect(rowCount()).toBe(4)
  })

  it('clears in one click through Reset filters', async () => {
    await mountLogs({ entries: entries() })
    await fireEvent.click(toggle())
    await flush()
    await fireEvent.click(screen.getByRole('button', { name: 'Reset filters' }))
    await flush()
    expect(rowCount()).toBe(4)
    expect(screen.queryByRole('button', { name: 'Reset filters' })).toBeNull()
  })

  it('says so when there are no problems', async () => {
    await mountLogs({ entries: makeEntries(2) })
    await fireEvent.click(toggle())
    await flush()
    expect(screen.getByText('No problems')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Reset filters' })).toBeTruthy()
  })

  it('does not say so over a partly loaded buffer', async () => {
    await mountLogs({ entries: makeEntries(2), hasMore: true })
    await fireEvent.click(toggle())
    await flush()
    expect(screen.queryByText('No problems')).toBeNull()
    expect(screen.getByText('Checking older logs for problems…')).toBeTruthy()
  })

  it('offers a retry when older logs could not be loaded, since the list may be incomplete', async () => {
    const { store } = await mountLogs({
      entries: makeEntries(2),
      hasMore: true,
      before: (c) => c.setHistoryStopped(true),
    })
    await fireEvent.click(toggle())
    await flush()
    expect(screen.getByText('No problems in the logs loaded so far')).toBeTruthy()
    expect(screen.queryByText('No problems')).toBeNull()

    const loads = store.loadAll.mock.calls.length
    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(store.loadAll).toHaveBeenCalledTimes(loads + 1)
  })
})

describe('jumpToPresent', () => {
  it('smooth-scrolls to the top, and pins the live edge only once the scroll settles', async () => {
    const { control, console: box } = await mountLogs()
    await userScroll(geo, box(), 200)
    await fireEvent.click(jumpToPresent())
    await flush()
    expect(geo.scrollToCalls).toEqual([{ top: 0, behavior: 'smooth' }])
    expect(jumpToPresent()).toBeNull()

    // A pin now would cancel the animation.
    geo.writes.length = 0
    control.addNewer(makeEntries(1, { start: 1000 }))
    await flush()
    expect(geo.writes).not.toContain(0)

    await fireEvent(box(), new Event('scrollend'))
    control.addNewer(makeEntries(1, { start: 1001 }))
    await flush()
    expect(geo.writes).toContain(0)
  })

  it('pins straight away when already at the top, where no scrollend would ever fire', async () => {
    const entries = makeEntries(3).map((e) => ({ ...e, level: 'warn', message: `Health check, container=${e.id}, status=unhealthy` }))
    const { control } = await mountLogs({ entries })
    // Opening a row at the live edge pauses follow without moving the console.
    await fireEvent.click(document.querySelector('[data-entry-id] [aria-expanded]'))
    await flush()
    await fireEvent.click(jumpToPresent())
    await flush()
    expect(geo.scrollToCalls).toEqual([])
    expect(jumpToPresent()).toBeNull()

    geo.writes.length = 0
    control.addNewer(makeEntries(1, { start: 1000 }))
    await flush()
    expect(geo.writes).toContain(0)
  })
})

describe('anchoring an open row', () => {
  const problems = () => makeEntries(3).map((e, i) => ({ ...e, level: 'warn', message: `Health check, container=c${i}, status=unhealthy` }))

  /** Lay rows out top to bottom under the console's scroll position, or `rowTop` for all. */
  function stubRowTops(rowTop) {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function rect() {
      if (!this.dataset?.entryId) return { top: 0 }
      if (rowTop !== undefined) return { top: rowTop }
      const log = this.closest('[role="log"]')
      return { top: [...log.querySelectorAll('[data-entry-id]')].indexOf(this) * geo.rowHeight - log.scrollTop }
    })
  }

  it('holds the topmost visible line once the focused line has gone', async () => {
    const entries = problems()
    const { control, console: box } = await mountLogs({ entries: [...makeEntries(50), ...entries] })
    stubRowTops()
    await fireEvent.click(document.querySelector(`[data-entry-id="${entries[1].id}"] [aria-expanded]`))
    await flush()
    await userScroll(geo, box(), 300)
    control.setEntries(makeEntries(60, { start: 2000 }))
    await flush()
    await userScroll(geo, box(), 300)

    control.addNewer(makeEntries(1, { start: 3000 }))
    await flush()
    expect(box().scrollTop).toBe(320)
  })

  it('still keeps the reading position when no line is visible to hold', async () => {
    const entries = problems()
    const { control, console: box } = await mountLogs({ entries: [...makeEntries(50), ...entries] })
    await fireEvent.click(document.querySelector(`[data-entry-id="${entries[1].id}"] [aria-expanded]`))
    await flush()
    await userScroll(geo, box(), 300)
    control.setEntries(makeEntries(60, { start: 2000 }))
    await flush()
    await userScroll(geo, box(), 300)

    // Every row now starts above the console's top edge.
    stubRowTops(-100)
    control.addNewer(makeEntries(1, { start: 3000 }))
    await flush()
    expect(box().scrollTop).toBe(320)
  })
})

describe('row marking and display', () => {
  it('does not mark a row for a press outside every row', async () => {
    const { console: box } = await mountLogs({ entries: makeEntries(2) })
    await fireEvent.pointerDown(box().querySelector('[data-end-marker]'))
    await flush()
    expect(box().querySelector('.bg-vault\\/15')).toBeNull()
  })

  it('highlights the search box while a search is active, until it is cleared', async () => {
    await mountLogs({ entries: makeEntries(2) })
    const input = screen.getByPlaceholderText('Filter logs…')
    expect(input.className).not.toContain('border-vault')
    await fireEvent.input(input, { target: { value: 'Log line 1' } })
    await flush()
    expect(input.className).toContain('border-vault')
    expect(rowCount()).toBe(1)

    await fireEvent.click(screen.getByRole('button', { name: 'Clear search' }))
    await flush()
    expect(input.className).not.toContain('border-vault')
    expect(rowCount()).toBe(2)
  })

  it('wraps message text while Wrap is on', async () => {
    await mountLogs({ entries: makeEntries(1) })
    const message = () => screen.getByTitle('Log line 0')
    expect(message().className).toContain('whitespace-nowrap')
    await fireEvent.click(screen.getByRole('button', { name: /^Wrap:/ }))
    await flush()
    expect(message().className).toContain('whitespace-pre-wrap')
    expect(message().className).not.toContain('whitespace-nowrap')
  })

  it('sets a run summary line apart from ordinary lines', async () => {
    const [plain, summary] = makeEntries(2)
    await mountLogs({ entries: [plain, { ...summary, type: 'summary', message: 'Backup finished' }] })
    expect(screen.getByTitle('Backup finished').className).toContain('font-medium')
    expect(screen.getByTitle(plain.message).className).not.toContain('font-medium')
  })
})

/** The entries the current fixture store holds, oldest first. */
function storeEntries() {
  return current.fixture.store.entries
}
