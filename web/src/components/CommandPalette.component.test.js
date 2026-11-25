// Command palette (#691, #715): the WAI-ARIA combobox-with-listbox pattern,
// an active option that stays in view, a result count for screen readers,
// every sidebar page, and meta text in the locale's own case.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { tick } from 'svelte'

const api = vi.hoisted(() => ({
  replica: false,
  listJobs: vi.fn(async () => [{ id: 1, name: 'Nightly' }]),
  listStorage: vi.fn(async () => []),
  listContainers: vi.fn(async () => ({ items: [{ name: 'plex' }] })),
  listVMs: vi.fn(async () => ({ items: [{ name: 'win11' }] })),
  listFolders: vi.fn(async () => ({ items: [] })),
  getRestorePoints: vi.fn(async () => [{ created_at: '2026-11-25T15:16:00Z' }]),
  runJob: vi.fn(async () => ({})),
  testStorage: vi.fn(async () => ({ success: true })),
}))
vi.mock('../lib/api.js', () => ({ api, isReplicaMode: () => api.replica }))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('../lib/router.svelte.js', () => ({ navigate }))

import CommandPalette from './CommandPalette.svelte'

const PAGES = [
  { path: '/', label: 'Dashboard', icon: 'M1 1' },
  { path: '/jobs', label: 'Jobs', icon: 'M2 2' },
  { path: '/replication', label: 'Replication', icon: 'M3 3' },
  { path: '/recovery', label: 'Disaster Recovery', icon: 'M4 4' },
  { path: '/anomalies', label: 'Anomalies', icon: 'M5 5' },
  { path: '/docs-external', label: 'Documentation', external: true, href: 'https://example.test/docs', icon: 'M6 6' },
]

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await tick()
    await new Promise((r) => setTimeout(r, 0))
  }
}

let scrolled

beforeEach(() => {
  scrolled = []
  Element.prototype.scrollIntoView = function scrollIntoView() { scrolled.push(this.id) }
  api.replica = false
  navigate.mockClear()
})

afterEach(() => {
  delete Element.prototype.scrollIntoView
})

async function open(props = {}) {
  const view = render(CommandPalette, { show: true, pages: PAGES, ...props })
  await flush()
  return view
}

const input = () => screen.getByRole('combobox', { name: 'Search commands' })
const options = () => within(screen.getByRole('listbox')).getAllByRole('option')

describe('CommandPalette', () => {
  it('wires the input to the listbox as a combobox', async () => {
    await open()
    const box = input()
    const list = screen.getByRole('listbox')
    expect(box.getAttribute('aria-controls')).toBe(list.id)
    expect(box.getAttribute('aria-expanded')).toBe('true')
    expect(box.getAttribute('aria-autocomplete')).toBe('list')
    expect(box.getAttribute('aria-activedescendant')).toBe(options()[0].id)
    expect(options()[0].getAttribute('aria-selected')).toBe('true')
    expect(options().filter((o) => o.getAttribute('aria-selected') === 'true')).toHaveLength(1)
  })

  it('groups options under labelled categories', async () => {
    await open()
    const pages = screen.getByRole('group', { name: 'Pages' })
    expect(within(pages).getAllByRole('option').length).toBe(PAGES.length)
  })

  it('moves the active option with the arrow keys and keeps it in view', async () => {
    await open()
    await fireEvent.keyDown(input(), { key: 'ArrowDown' })
    await fireEvent.keyDown(input(), { key: 'ArrowDown' })
    await tick()
    const active = options()[2]
    expect(input().getAttribute('aria-activedescendant')).toBe(active.id)
    expect(active.getAttribute('aria-selected')).toBe('true')
    expect(scrolled.at(-1)).toBe(active.id)
    await fireEvent.keyDown(input(), { key: 'ArrowUp' })
    await tick()
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[1].id)
    expect(scrolled.at(-1)).toBe(options()[1].id)
  })

  it('keeps the active option when items finish loading', async () => {
    let release
    api.listContainers.mockImplementationOnce(() => new Promise((resolve) => { release = () => resolve({ items: [{ name: 'late' }] }) }))
    render(CommandPalette, { show: true, pages: PAGES })
    await tick()
    await fireEvent.keyDown(input(), { key: 'ArrowDown' })
    await tick()
    const before = input().getAttribute('aria-activedescendant')
    release()
    await flush()
    expect(options().some((o) => o.textContent.includes('late'))).toBe(true)
    expect(input().getAttribute('aria-activedescendant')).toBe(before)
    expect(document.getElementById(before).textContent).toContain('Go to Jobs')
  })

  it('starts again at the top when the search changes', async () => {
    await open()
    await fireEvent.keyDown(input(), { key: 'ArrowDown' })
    await fireEvent.input(input(), { target: { value: 'go' } })
    await tick()
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[0].id)
  })

  it('announces how many results match', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    await open()
    await fireEvent.input(input(), { target: { value: 'recovery' } })
    await vi.advanceTimersByTimeAsync(500)
    expect(screen.getByRole('status').textContent.trim()).toBe('1 result')
    await fireEvent.input(input(), { target: { value: 'zzzz' } })
    await vi.advanceTimersByTimeAsync(500)
    expect(screen.getByRole('status').textContent.trim()).toBe('No results')
    expect(input().hasAttribute('aria-activedescendant')).toBe(false)
  })

  it('offers every page it is given, including Replication, Disaster Recovery and Anomalies', async () => {
    await open()
    const labels = options().map((o) => o.textContent.trim())
    for (const name of ['Go to Dashboard', 'Go to Jobs', 'Go to Replication', 'Go to Disaster Recovery', 'Go to Anomalies', 'Open Documentation']) {
      expect(labels.some((l) => l.startsWith(name))).toBe(true)
    }
  })

  it('leaves out pages the shell hides', async () => {
    await open({ pages: PAGES.filter((p) => p.path !== '/anomalies') })
    expect(options().some((o) => o.textContent.includes('Anomalies'))).toBe(false)
  })

  it('runs the active option on Enter and closes', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    await fireEvent.input(input(), { target: { value: 'disaster' } })
    await tick()
    await fireEvent.keyDown(input(), { key: 'Enter' })
    expect(onclose).toHaveBeenCalled()
    expect(navigate).toHaveBeenCalledWith('/recovery')
  })

  it('opens an external page in a new tab', async () => {
    const opened = vi.spyOn(window, 'open').mockImplementation(() => null)
    await open()
    await fireEvent.input(input(), { target: { value: 'documentation' } })
    await tick()
    await fireEvent.keyDown(input(), { key: 'Enter' })
    expect(opened).toHaveBeenCalledWith('https://example.test/docs', '_blank', 'noopener')
  })

  it('runs an option on click', async () => {
    await open()
    await fireEvent.click(options().find((o) => o.textContent.includes('Go to Jobs')))
    expect(navigate).toHaveBeenCalledWith('/jobs')
  })

  it('shows item types in title case and times as the locale writes them', async () => {
    await open()
    const plex = options().find((o) => o.textContent.includes('plex'))
    expect(plex.textContent).toContain('Container')
    const win = options().find((o) => o.textContent.includes('win11'))
    expect(win.textContent).toContain('VM')
    const point = options().find((o) => o.textContent.includes('Restore: Nightly'))
    // No capitalize class: it turned "03:16 pm" into "03:16 Pm".
    expect(point.querySelector('.capitalize')).toBeNull()
  })

  it('still finds an item by its type', async () => {
    await open()
    await fireEvent.input(input(), { target: { value: 'container' } })
    await tick()
    expect(options().map((o) => o.textContent)).toEqual([expect.stringContaining('plex')])
  })

  it('shows the keyboard shortcuts in its footer', async () => {
    await open()
    const footer = screen.getByText('Navigate').closest('div')
    expect(footer.textContent).toMatch(/Open or close/)
  })
})
