// The More sheet's contract (#530). Focus containment, native Escape and geometry
// need a real browser and live in tests/mobile-nav.spec.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { tick } from 'svelte'
import MoreSheet from './MoreSheet.svelte'

const items = [
  { path: '/storage', label: 'Storage', icon: 'M0', section: 'Protect' },
  { path: '/recovery', label: 'Recovery', icon: 'M0', section: 'Recover' },
  { path: '/logs', label: 'Logs', icon: 'M0', section: 'System' },
  { path: '/docs-external', label: 'Documentation', icon: 'M0', section: 'System', external: true, href: 'https://example.test/docs' },
]

let opener

async function open(props = {}) {
  const view = render(MoreSheet, { show: true, id: 'more-sheet', items, route: '/jobs', ...props })
  await tick()
  return view
}

const dialog = () => document.querySelector('dialog')
const flush = async () => {
  await tick()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

beforeEach(() => {
  opener = document.createElement('button')
  document.body.append(opener)
  opener.focus()
})

afterEach(() => {
  opener.remove()
})

describe('MoreSheet', () => {
  it('renders nothing while closed', () => {
    render(MoreSheet, { show: false, items, route: '/jobs' })
    expect(dialog()).toBeNull()
  })

  it('is a named modal on the bottom edge, with the requested id', async () => {
    await open()
    expect(dialog().id).toBe('more-sheet')
    expect(dialog().classList.contains('vault-dialog-sheet')).toBe(true)
    expect(screen.getByRole('heading', { name: 'More' }).id).toBe(dialog().getAttribute('aria-labelledby'))
  })

  it('lists real links, grouped by section, with the external one in a new tab', async () => {
    await open()
    const nav = within(dialog()).getByRole('navigation', { name: 'More pages' })
    expect(within(nav).getByRole('link', { name: 'Storage' }).getAttribute('href')).toBe('#/storage')
    const docs = within(nav).getByRole('link', { name: 'Documentation (opens in a new tab)' })
    expect(docs.getAttribute('href')).toBe('https://example.test/docs')
    expect(docs.getAttribute('target')).toBe('_blank')
    expect(nav.textContent).toMatch(/Protect\s*Storage\s*Recover\s*Recovery\s*System\s*Logs\s*Documentation/)
  })

  it('moves focus to the first entry', async () => {
    await open()
    expect(document.activeElement.textContent.trim()).toBe('Storage')
  })

  it('marks only the current page', async () => {
    await open({ route: '/logs' })
    const current = dialog().querySelectorAll('[aria-current="page"]')
    expect([...current].map((el) => el.textContent.trim())).toEqual(['Logs'])
  })

  it('marks Recovery while the recover wizard is open', async () => {
    await open({ route: '/recover' })
    expect(dialog().querySelector('[aria-current="page"]').textContent.trim()).toBe('Recovery')
  })

  it('closes when any entry is chosen', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    for (const link of within(dialog()).getAllByRole('link')) {
      link.addEventListener('click', (e) => e.preventDefault())
      await fireEvent.click(link)
    }
    expect(onclose).toHaveBeenCalledTimes(items.length)
  })

  it('closes on a backdrop click, not on a click in the panel', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    await fireEvent.click(screen.getByRole('heading', { name: 'More' }))
    expect(onclose).not.toHaveBeenCalled()
    await fireEvent.click(dialog())
    expect(onclose).toHaveBeenCalledTimes(1)
  })

  it('returns focus to its opener when it closes', async () => {
    const view = await open()
    await view.rerender({ show: false })
    await flush()
    expect(document.activeElement).toBe(opener)
  })

  it('leaves focus alone if something else took it while closing', async () => {
    const heading = document.createElement('h1')
    heading.tabIndex = -1
    document.body.append(heading)
    const view = await open()
    await view.rerender({ show: false })
    heading.focus()
    await flush()
    expect(document.activeElement).toBe(heading)
    heading.remove()
  })
})
