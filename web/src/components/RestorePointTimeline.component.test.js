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
