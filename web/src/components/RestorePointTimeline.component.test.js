import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const embedded = vi.hoisted(() => ({ value: false }))
const scrollHostTo = vi.hoisted(() => vi.fn())
vi.mock('../lib/runtime-config.js', async (original) => ({ ...(await original()), isEmbedded: () => embedded.value }))
vi.mock('../lib/embed.js', async (original) => ({ ...(await original()), scrollHostTo }))

import RestorePointTimeline from './RestorePointTimeline.svelte'

const point = (id, created_at) => ({ id, created_at, size_bytes: 1024, backup_type: 'full', job_name: 'Job', metadata: '{}' })
const POINTS = [point(1, '2026-10-30T10:00:00Z'), point(2, '2026-11-01T10:00:00Z')]

describe('RestorePointTimeline day jumps', () => {
  beforeEach(() => {
    scrollHostTo.mockClear()
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('scrolls the host page when embedded', async () => {
    embedded.value = true
    render(RestorePointTimeline, { points: POINTS })
    const [day] = screen.getByRole('group', { name: 'Backup activity by day' }).querySelectorAll('button')
    await fireEvent.click(day)
    expect(scrollHostTo).toHaveBeenCalledTimes(1)
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
  })

  it('scrolls the element itself when standalone', async () => {
    embedded.value = false
    render(RestorePointTimeline, { points: POINTS })
    const [day] = screen.getByRole('group', { name: 'Backup activity by day' }).querySelectorAll('button')
    await fireEvent.click(day)
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1)
    expect(scrollHostTo).not.toHaveBeenCalled()
  })
})

describe('RestorePointTimeline points (#704)', () => {
  const job = (id, created_at, extra = {}) => ({ ...point(id, created_at), jobId: 7, jobName: 'Docker & Appdata', ...extra })

  it('exposes the chosen point as pressed and keeps actions outside the point button', async () => {
    const onSelect = vi.fn()
    const onDelete = vi.fn()
    render(RestorePointTimeline, { points: [job(1, '2026-11-01T10:00:00Z')], selectedId: 1, onSelect, onDelete })
    const choose = screen.getByRole('button', { name: /^Restore point .*Docker & Appdata/ })
    expect(choose.getAttribute('aria-pressed')).toBe('true')
    const del = screen.getByRole('button', { name: /^Delete restore point/ })
    expect(choose.contains(del)).toBe(false)
    expect(choose.querySelector('button, input, a')).toBeNull()

    await fireEvent.click(del)
    expect(onDelete).toHaveBeenCalledTimes(1)
    expect(onSelect).not.toHaveBeenCalled()

    await fireEvent.click(choose)
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('labels a day with h3 and names the backup types and count in the strip', () => {
    render(RestorePointTimeline, {
      points: [
        job(3, '2026-11-01T12:00:00Z', { backup_type: 'incremental' }),
        job(2, '2026-11-01T10:00:00Z'),
        job(1, '2026-10-30T10:00:00Z', { backup_type: 'incremental' }),
      ],
    })
    expect(screen.getAllByRole('heading', { level: 3 })).toHaveLength(2)
    expect(screen.queryAllByRole('heading', { level: 4 })).toHaveLength(0)
    const bars = screen.getByRole('group', { name: 'Backup activity by day' }).querySelectorAll('button')
    expect(bars[0].getAttribute('aria-label')).toMatch(/1 restore point \(incremental\)/)
    expect(bars[1].getAttribute('aria-label')).toMatch(/2 restore points \(1 full, 1 incremental\)/)
  })

  it('pluralises the coverage count', () => {
    const item = { name: 'plex', jobs: [{ id: 7 }] }
    render(RestorePointTimeline, {
      points: [job(1, '2026-11-01T10:00:00Z', { metadata: JSON.stringify({ item_sizes: { plex: 1 } }) })],
      selectedItems: [item],
    })
    expect(screen.getByText('Contains the selected item')).toBeTruthy()
  })
})
