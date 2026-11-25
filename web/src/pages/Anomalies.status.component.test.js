// Anomalies page states: a failed load offers Retry (#681), filters live in the
// URL (#693), scope shows names (#694), and with detection off the page explains
// why instead of redirecting (#695).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  listAnomalies: vi.fn(),
  listJobs: vi.fn(async () => [{ id: 1, name: 'Nightly appdata' }]),
  listStorage: vi.fn(async () => [{ id: 3, name: 'Offsite NAS' }]),
}))
vi.mock('../lib/api.js', () => ({ api, isReplicaMode: () => false }))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))
const flags = vi.hoisted(() => ({ enabled: true, loaded: true }))
vi.mock('../lib/settings.svelte.js', () => ({
  getAnomalyEnabled: () => flags.enabled,
  getFeatureFlagsLoaded: () => flags.loaded,
}))

import Anomalies from './Anomalies.svelte'

const row = (id, over = {}) => ({
  id, severity: 'warning', state: 'open', summary: `anomaly ${id}`, detector: 'test',
  scope_kind: 'job', scope_id: 1, first_seen_at: '2026-01-01T00:00:00Z', last_seen_at: '2026-01-01T00:00:00Z',
  ...over,
})

describe('Anomalies page states', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    flags.enabled = true
    flags.loaded = true
    window.location.hash = '#/anomalies'
  })
  afterEach(() => { window.location.hash = '' })

  it('shows Retry when the list fails to load, and reloads only the list', async () => {
    api.listAnomalies.mockRejectedValueOnce(new Error('HTTP 500'))
    render(Anomalies)
    expect(await screen.findByText("Couldn't load anomalies")).toBeTruthy()
    expect(screen.queryByText('No anomalies found')).toBeNull()
    api.listAnomalies.mockResolvedValueOnce({ anomalies: [row(4)], next_cursor: '' })
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByLabelText('Select anomaly 4')).toBeTruthy()
    expect(api.listAnomalies).toHaveBeenCalledTimes(2)
  })

  it('names the job and destination in each row, linking to them', async () => {
    api.listAnomalies.mockResolvedValueOnce({ anomalies: [row(1), row(2, { scope_kind: 'destination', scope_id: 3 })], next_cursor: '' })
    render(Anomalies)
    const job = await screen.findAllByRole('link', { name: 'Nightly appdata' })
    expect(job[0].getAttribute('href')).toBe('#/jobs')
    const dest = screen.getAllByRole('link', { name: 'Offsite NAS' })
    expect(dest[0].getAttribute('href')).toBe('#/storage')
    expect(screen.queryByText('Job #1')).toBeNull()
  })

  it('falls back to the id when a name is unknown', async () => {
    api.listAnomalies.mockResolvedValueOnce({ anomalies: [row(1, { scope_id: 99 })], next_cursor: '' })
    render(Anomalies)
    expect((await screen.findAllByText('Job #99')).length).toBeGreaterThan(0)
  })

  it('reads its filters from the URL', async () => {
    window.location.hash = '#/anomalies?state=all&severity=critical&since=-7d'
    api.listAnomalies.mockResolvedValue({ anomalies: [], next_cursor: '' })
    render(Anomalies)
    await vi.waitFor(() => expect(api.listAnomalies).toHaveBeenCalled())
    expect(api.listAnomalies).toHaveBeenCalledWith(expect.objectContaining({ state: undefined, severity: 'critical', since: '-7d' }))
    expect(/** @type {HTMLSelectElement} */ (screen.getByLabelText('State')).value).toBe('')
  })

  it('writes a changed filter to the URL without adding a history entry', async () => {
    api.listAnomalies.mockResolvedValue({ anomalies: [], next_cursor: '' })
    const replace = vi.spyOn(window.history, 'replaceState')
    render(Anomalies)
    await vi.waitFor(() => expect(api.listAnomalies).toHaveBeenCalled())
    await fireEvent.change(screen.getByLabelText('Severity'), { target: { value: 'critical' } })
    expect(replace.mock.lastCall?.[2]).toBe('#/anomalies?severity=critical')
    await fireEvent.change(screen.getByLabelText('State'), { target: { value: '' } })
    expect(replace.mock.lastCall?.[2]).toBe('#/anomalies?state=all&severity=critical')
    replace.mockRestore()
  })

  it('explains that detection is off and links to Settings', async () => {
    flags.enabled = false
    api.listAnomalies.mockResolvedValue({ anomalies: [], next_cursor: '' })
    render(Anomalies)
    expect(await screen.findByText('Anomaly detection is off')).toBeTruthy()
    expect(screen.getByRole('link', { name: /settings/i }).getAttribute('href')).toBe('#/settings/general')
    expect(screen.queryByLabelText('Severity')).toBeNull()
  })
})
