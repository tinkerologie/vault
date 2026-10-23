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
vi.mock('../lib/api.js', async () => (await import('../test-support/scroll-box.js')).apiModuleMock())

import Logs from './Logs.svelte'

let geo

beforeEach(() => {
  geo = installScrollBox({ clientHeight: 400, rowHeight: 20 })
})

afterEach(() => {
  geo.restore()
  current.fixture = null
})

async function mountLogs({ entries = makeEntries(50), hasMore = false, before } = {}) {
  current.fixture = createFixtureStore({ entries, hasMore })
  before?.(current.fixture.control)
  const view = render(Logs)
  await flush()
  return { ...view, ...current.fixture, console: () => document.querySelector('[role="log"]') }
}

const rowCount = () => document.querySelectorAll('[data-entry-id]').length
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
    const list = [...control_entries()]
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
    control.setEntries(control_entries().map((e) => ({ ...e })))
    await flush()
    expect(geo.writes).toEqual([])

    // Growth with the reader at scrollTop 0 (follow already off).
    geo.position(box(), 0)
    const [mid] = makeEntries(1, { start: 47 })
    mid.ts = new Date(Date.parse(mid.ts) + 500).toISOString()
    control.setEntries([...control_entries(), mid].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts)))
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

function control_entries() {
  return current.fixture.store.entries
}
