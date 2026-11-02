// The status banner (#527): real links, and a live region that only speaks when
// the verdict changes.
import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import { tick } from 'svelte'
import StatusBanner from './StatusBanner.svelte'
import { deriveBackupStatus } from '../lib/backupStatus.js'

const SUMMARY = { health_score: 100, protected_items: 7, recent_success: 5, recent_failed: 0 }
const READY = { hasStorage: true, hasJobs: true, protectedCount: 7, total: 7, summary: SUMMARY }
const live = () => screen.getByRole('status')

describe('StatusBanner', () => {
  it('shows the verdict in a named region with a heading', () => {
    render(StatusBanner, { status: deriveBackupStatus(READY) })
    const region = screen.getByRole('region', { name: 'Backup status' })
    expect(within(region).getByRole('heading', { level: 2, name: 'Backups healthy' })).toBeTruthy()
    expect(within(region).getByText('All 7 items protected.')).toBeTruthy()
    expect(region.dataset.state).toBe('healthy')
  })

  it('renders one real link per action, each with its own href', () => {
    render(StatusBanner, { status: deriveBackupStatus({ ...READY, failures: 1, unprotected: 2 }) })
    const links = screen.getAllByRole('link')
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['Review failures', '#/history'],
      ['Add to a backup job', '#/jobs'],
    ])
  })

  it('renders no links when there is nothing to do', () => {
    render(StatusBanner, { status: deriveBackupStatus(READY) })
    expect(screen.queryAllByRole('link')).toEqual([])
  })

  it('omits the detail line when there is none', () => {
    const { container } = render(StatusBanner, { status: deriveBackupStatus({ loading: true }) })
    expect(container.querySelector('h2 + p')).toBeNull()
  })

  it.each([
    ['success', READY, 'bg-success/10'],
    ['warning', { ...READY, unprotected: 1 }, 'bg-warning/10'],
    ['danger', { ...READY, failures: 1 }, 'bg-danger/10'],
    ['info', {}, 'bg-info/10'],
    ['neutral', { loading: true }, 'bg-surface-3'],
  ])('styles the %s tone', (_tone, input, expected) => {
    render(StatusBanner, { status: deriveBackupStatus(input) })
    expect(screen.getByRole('region').className).toContain(expected)
  })

  it('keeps nothing truncated', () => {
    const { container } = render(StatusBanner, { status: deriveBackupStatus({ ...READY, failures: 1 }) })
    expect(container.querySelector('.truncate')).toBeNull()
  })

  describe('announcements', () => {
    it('is polite, and only the hidden announcer is live', () => {
      render(StatusBanner, { status: deriveBackupStatus(READY) })
      expect(live().getAttribute('aria-live')).toBe('polite')
      expect(screen.getByRole('region').getAttribute('aria-live')).toBeNull()
    })

    it('says nothing while loading or for the first verdict', async () => {
      const { rerender } = render(StatusBanner, { status: deriveBackupStatus({ loading: true }) })
      await tick()
      expect(live().textContent).toBe('')
      await rerender({ status: deriveBackupStatus(READY) })
      await tick()
      expect(live().textContent).toBe('')
    })

    it('says nothing for a verdict already there on arrival', async () => {
      render(StatusBanner, { status: deriveBackupStatus(READY) })
      await tick()
      expect(live().textContent).toBe('')
    })

    it('speaks when the verdict changes, and again when it changes back', async () => {
      const { rerender } = render(StatusBanner, { status: deriveBackupStatus(READY) })
      await tick()
      await rerender({ status: deriveBackupStatus({ ...READY, failures: 1 }) })
      await tick()
      expect(live().textContent).toBe('Needs attention. 1 recent failure')
      await rerender({ status: deriveBackupStatus(READY) })
      await tick()
      expect(live().textContent).toBe('Backups healthy. All 7 items protected.')
    })

    it('stays quiet when only a count changes or a reload repeats the verdict', async () => {
      const { rerender } = render(StatusBanner, { status: deriveBackupStatus(READY) })
      await tick()
      await rerender({ status: deriveBackupStatus({ ...READY, failures: 1 }) })
      await tick()
      await rerender({ status: deriveBackupStatus({ ...READY, failures: 2 }) })
      await tick()
      expect(live().textContent).toBe('Needs attention. 1 recent failure')
      expect(screen.getByText('2 recent failures')).toBeTruthy()
    })
  })
})
