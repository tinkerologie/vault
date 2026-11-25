// Logs page: the console is a labelled region, not a live log (#686); live
// arrivals are announced, throttled; filters live in the URL (#693); a failed
// load offers Retry (#681).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import { createFixtureStore } from '../test-support/logs-store.svelte.js'
import { makeEntries } from '../test-support/logs-entries.js'
import { flush, installScrollBox } from '../test-support/scroll-box.js'
import { tick } from 'svelte'

// flush() waits on setTimeout, which fake timers hold.
const settle = async () => { await tick(); await Promise.resolve(); await tick() }

const current = vi.hoisted(() => ({ fixture: null, initial: null }))

vi.mock('../lib/unifiedlog.svelte.js', () => ({
  createUnifiedLogStore: (initial) => { current.initial = initial; return current.fixture.store },
}))
vi.mock('../lib/clipboard.js', () => ({ copyText: vi.fn(async () => true) }))
vi.mock('../lib/api.js', async () => (await import('../test-support/scroll-box.js')).apiModuleMock())

import Logs from './Logs.svelte'

let geo
beforeEach(() => {
  geo = installScrollBox({ clientHeight: 400, rowHeight: 20 })
  window.location.hash = '#/logs'
})
afterEach(() => {
  geo.restore()
  vi.useRealTimers()
  current.fixture = null
  window.location.hash = ''
})

async function mountLogs(entries = makeEntries(30)) {
  current.fixture = createFixtureStore({ entries })
  const view = render(Logs)
  await flush()
  return { ...view, ...current.fixture }
}

const announcer = () => /** @type {HTMLElement} */ (document.querySelector('[data-log-announcer]'))

describe('Logs console semantics', () => {
  it('is a labelled region with no live log role', async () => {
    await mountLogs()
    expect(document.querySelector('[role="log"]')).toBeNull()
    expect(screen.getByRole('region', { name: 'Log console' })).toBeTruthy()
    expect(document.querySelector('[data-log-console] [aria-live]')).toBeNull()
  })

  it('announces live arrivals in one hidden status, at most every ten seconds', async () => {
    const { control } = await mountLogs()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    expect(announcer().getAttribute('role')).toBe('status')
    expect(announcer().textContent).toBe('')

    control.liveArrive(makeEntries(2, { start: 5000 }))
    await settle()
    expect(announcer().textContent).toBe('2 new log entries')

    control.liveArrive(makeEntries(1, { start: 6000 }))
    control.liveArrive(makeEntries(2, { start: 7000 }))
    await settle()
    // Held back: the status changed less than ten seconds ago.
    expect(announcer().textContent).toBe('2 new log entries')
    vi.advanceTimersByTime(9999)
    await settle()
    expect(announcer().textContent).toBe('2 new log entries')
    vi.advanceTimersByTime(1)
    await settle()
    expect(announcer().textContent).toBe('3 new log entries')
    vi.advanceTimersByTime(10000)
    control.liveArrive(makeEntries(4, { start: 8000 }))
    await settle()
    expect(announcer().textContent).toBe('4 new log entries')
  })

  it('says nothing for rows loaded by scrolling', async () => {
    const { control } = await mountLogs()
    control.addNewer(makeEntries(5, { start: 9000 }))
    await flush()
    expect(announcer().textContent).toBe('')
  })
})

describe('Logs filters in the URL', () => {
  it('starts with the filters the URL names', async () => {
    window.location.hash = '#/logs?level=error&category=health&job=docker&q=disk'
    await mountLogs()
    expect(current.initial).toEqual({ level: 'error', category: 'health', job: 'docker', search: 'disk' })
  })

  it('writes a changed filter back without adding a history entry', async () => {
    const replace = vi.spyOn(window.history, 'replaceState')
    const { store } = await mountLogs()
    store.setLevelFilter('error')
    await flush()
    expect(replace.mock.lastCall?.[2]).toBe('#/logs?level=error')
    store.setSearchFilter('disk full')
    await flush()
    expect(replace.mock.lastCall?.[2]).toBe('#/logs?level=error&q=disk+full')
    replace.mockRestore()
  })

  it('applies filters from a link while the page is open', async () => {
    const { store } = await mountLogs()
    window.location.hash = '#/logs?level=warn&category=backup'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await flush()
    expect(store.setLevelFilter).toHaveBeenCalledWith('warn')
    expect(store.setCategory).toHaveBeenCalledWith('backup')
  })
})

describe('Logs load failure', () => {
  it('shows Retry, which reloads the logs', async () => {
    const { control, store } = await mountLogs()
    control.setError('HTTP 500')
    await flush()
    expect(screen.getByText("Couldn't load logs")).toBeTruthy()
    store.load.mockImplementationOnce(async () => { control.setError('') })
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await flush()
    expect(store.load).toHaveBeenCalledTimes(2)
    expect(store.loadAll).toHaveBeenCalledTimes(2)
  })
})
