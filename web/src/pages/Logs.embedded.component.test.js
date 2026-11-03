// Component tests for the embedded Logs console (#526). Embedded in Unraid's
// page the console has no scroll box: the parent page scrolls, its position
// arrives as visible-area updates, and scrolling is asked of the host. The
// geometry arithmetic is unit-tested in lib/consoleport.test.js; these tests
// check that the page is wired to it.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import { createFixtureStore } from '../test-support/logs-store.svelte.js'
import { makeEntries } from '../test-support/logs-entries.js'
import { flush } from '../test-support/scroll-box.js'

const ROW = 20
const CONSOLE_TOP = 300

const current = vi.hoisted(() => ({ fixture: null, area: { top: 0, height: 400 }, listeners: new Set() }))

vi.mock('../lib/unifiedlog.svelte.js', () => ({
  createUnifiedLogStore: () => current.fixture.store,
}))
vi.mock('../lib/clipboard.js', () => ({ copyText: vi.fn(async () => true) }))
vi.mock('../lib/api.js', async () => (await import('../test-support/scroll-box.js')).apiModuleMock())
vi.mock('../lib/runtime-config.js', async (importOriginal) => ({ ...(await importOriginal()), isEmbedded: () => true, getLiveMode: () => 'ws' }))
vi.mock('../lib/embed.js', () => ({
  getVisibleArea: () => current.area,
  onVisibleAreaChange: (fn) => {
    current.listeners.add(fn)
    return () => current.listeners.delete(fn)
  },
  scrollHostTo: vi.fn(),
}))

import Logs from './Logs.svelte'
import { scrollHostTo } from '../lib/embed.js'

let savedRect

beforeEach(() => {
  current.area = { top: 0, height: 400 }
  current.listeners.clear()
  vi.mocked(scrollHostTo).mockClear()
  // jsdom has no layout. The console sits CONSOLE_TOP px down the frame at
  // its natural height, ROW px per rendered line.
  savedRect = Element.prototype.getBoundingClientRect
  Element.prototype.getBoundingClientRect = function () {
    const log = this.getAttribute?.('role') === 'log' ? this : null
    if (log) {
      const height = log.querySelectorAll('[data-entry-id]').length * ROW
      return { top: CONSOLE_TOP, bottom: CONSOLE_TOP + height, height, left: 0, right: 0, width: 0, x: 0, y: CONSOLE_TOP }
    }
    const rows = this.closest?.('[role="log"]')?.querySelectorAll('[data-entry-id]')
    const i = rows ? [...rows].indexOf(this) : -1
    const top = i >= 0 ? CONSOLE_TOP + i * ROW : 0
    return { top, bottom: top + ROW, height: ROW, left: 0, right: 0, width: 0, x: 0, y: top }
  }
})

afterEach(() => {
  Element.prototype.getBoundingClientRect = savedRect
  current.fixture = null
})

async function mountLogs(entries = makeEntries(50)) {
  current.fixture = createFixtureStore({ entries })
  const view = render(Logs)
  await flush()
  return view
}

/** The parent page scrolls so frame position `top` is at the top of the screen. */
async function scrollPage(top) {
  current.area = { ...current.area, top }
  current.listeners.forEach((fn) => fn())
  await flush()
}

const jumpToPresent = () => screen.queryByTitle('Jump to present')

describe('embedded Logs console (#526)', () => {
  it('puts the jump buttons on sticky rails, not on its own corners', async () => {
    await mountLogs()
    const oldest = screen.getByRole('button', { name: 'Jump to oldest log' })
    expect(oldest.parentElement.classList.contains('sticky')).toBe(true)
    expect(jumpToPresent()).toBeNull()
  })

  it('leaves the live edge when the page scrolls into the console, and returns at its top', async () => {
    await mountLogs()
    await scrollPage(CONSOLE_TOP + 200)
    expect(jumpToPresent()).not.toBeNull()
    expect(jumpToPresent().parentElement.classList.contains('sticky')).toBe(true)
    await scrollPage(CONSOLE_TOP)
    expect(jumpToPresent()).toBeNull()
  })

  it('scrolls the page, not the console, to jump to present and to the oldest line', async () => {
    await mountLogs()
    await scrollPage(CONSOLE_TOP + 200)
    await fireEvent.click(screen.getByRole('button', { name: 'Jump to oldest log' }))
    // 50 lines of 20 px seen through a 400 px window end 600 px down the console.
    expect(scrollHostTo).toHaveBeenLastCalledWith(CONSOLE_TOP + 50 * ROW - 400)
    // The page arrives at the end: the oldest line is on screen, so Oldest goes.
    await scrollPage(CONSOLE_TOP + 50 * ROW - 400)
    expect(screen.queryByRole('button', { name: 'Jump to oldest log' })).toBeNull()
    await fireEvent.click(jumpToPresent())
    await flush()
    expect(scrollHostTo).toHaveBeenLastCalledWith(CONSOLE_TOP)
    expect(jumpToPresent()).toBeNull()
  })

  it('counts lines that arrive while paused on the sticky button', async () => {
    await mountLogs()
    await scrollPage(CONSOLE_TOP + 200)
    current.fixture.control.setEntries([...current.fixture.store.entries, ...makeEntries(1, { start: 100 })])
    await flush()
    expect(jumpToPresent().textContent).toContain('1 new entry')
  })

  it('does not drag the page to the console while following', async () => {
    await mountLogs()
    current.fixture.control.setEntries([...makeEntries(1, { start: 100 }), ...current.fixture.store.entries])
    await flush()
    expect(scrollHostTo).not.toHaveBeenCalled()
  })

  it('stops listening to the page when it unmounts', async () => {
    const { unmount } = await mountLogs()
    expect(current.listeners.size).toBe(1)
    unmount()
    expect(current.listeners.size).toBe(0)
  })
})
