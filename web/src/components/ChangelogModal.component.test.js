// Changelog dialog semantics (#692): disclosure state, headings in order, no
// glyphs read aloud, and one scroll container.
import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { tick } from 'svelte'
import ChangelogModal from './ChangelogModal.svelte'

const RELEASES = [
  { version: 'v2026.11.03', date: '2026-11-20', sections: { Fixed: ['A **bold** fix'], Added: ['A thing'] } },
  { version: 'v2026.11.02', date: '2026-11-10', sections: { Changed: ['Something else'] } },
]

async function open() {
  render(ChangelogModal, { show: true, releases: RELEASES, currentVersion: '2026.11.02', latestTag: 'v2026.11.03' })
  await tick()
  await tick()
  return screen.getByRole('dialog', { name: 'Changelog' })
}

describe('ChangelogModal', () => {
  it('puts each release toggle in an h3 and exposes its expanded state', async () => {
    const dialog = await open()
    const latest = within(dialog).getByRole('button', { name: /2026\.11\.03/ })
    const older = within(dialog).getByRole('button', { name: /2026\.11\.02/ })
    expect(latest.getAttribute('aria-expanded')).toBe('true')
    expect(older.getAttribute('aria-expanded')).toBe('false')
    expect(latest.closest('h3')).not.toBeNull()
    await fireEvent.click(older)
    await tick()
    expect(older.getAttribute('aria-expanded')).toBe('true')
    expect(document.getElementById(older.getAttribute('aria-controls'))).not.toBeNull()
  })

  it('keeps headings in order: the h2 title, h3 releases, h4 sections', async () => {
    const dialog = await open()
    expect(within(dialog).getByRole('heading', { level: 2, name: 'Changelog' })).toBeTruthy()
    expect(within(dialog).getAllByRole('heading', { level: 3 })).toHaveLength(2)
    expect(within(dialog).getAllByRole('heading', { level: 4 }).map((h) => h.textContent)).toEqual(['Fixed', 'Added'])
  })

  it('hides the arrow glyphs from screen readers', async () => {
    const dialog = await open()
    const latest = within(dialog).getByRole('button', { name: /2026\.11\.03/ })
    expect(latest.textContent).toContain('▾')
    const glyph = [...latest.querySelectorAll('[aria-hidden="true"]')].find((el) => /[▾▸]/.test(el.textContent))
    expect(glyph).toBeTruthy()
  })

  it('has no scroll container inside the dialog body', async () => {
    const dialog = await open()
    const inner = [...dialog.querySelectorAll('[class*="overflow-y-auto"]')]
    // Only the shared dialog body scrolls.
    expect(inner).toHaveLength(1)
    expect(dialog.querySelector('[class*="max-h-[70vh]"]')).toBeNull()
  })
})
