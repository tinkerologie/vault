import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  listJobs: vi.fn(),
  listRestoreRequests: vi.fn(async () => ({ requests: [] })),
  getHistory: vi.fn(),
  getHistoryTrend: vi.fn(),
  getRunnerStatus: vi.fn(),
  listAnomalies: vi.fn(),
  acknowledgeRuns: vi.fn(),
  purgeHistory: vi.fn(),
  getRun: vi.fn(),
}))
vi.mock('../lib/api.js', () => ({ api, isReplicaMode: () => false }))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))

import History from './History.svelte'

const failed = { id: 7, job_id: 1, status: 'failed', started_at: '2026-10-01T12:00:00Z' }
const completed = { id: 8, job_id: 2, status: 'completed', started_at: '2026-10-02T12:00:00Z' }
const GB = 1024 ** 3
const TREND = { period: '30d', bucket: 'day', points: ['2026-10-01', '2026-10-02'].map((day) => ({ start: `${day}T00:00:00Z`, total_bytes: GB, categories: { containers: GB } })) }

beforeEach(() => {
  vi.clearAllMocks()
  window.location.hash = '#/history'
  api.listJobs.mockResolvedValue([{ id: 1, name: 'VM Backups' }, { id: 2, name: 'Docker' }])
  api.getHistory.mockResolvedValue([failed, completed])
  api.getHistoryTrend.mockResolvedValue(TREND)
  api.getRunnerStatus.mockResolvedValue({ active: false, queue: [] })
  api.listAnomalies.mockResolvedValue({ anomalies: [] })
  api.listRestoreRequests.mockResolvedValue({ requests: [] })
})

it('keeps selected runs after a failed acknowledgement so the user can retry', async () => {
  window.location.hash = '#/history?review=outstanding'
  api.getHistory.mockReset()
  api.getHistory.mockResolvedValueOnce([failed]).mockResolvedValue([{ ...failed, acknowledged_at: '2026-10-01T12:01:00Z' }])
  api.acknowledgeRuns
    .mockRejectedValueOnce(new Error('Could not save review'))
    .mockResolvedValueOnce({ acknowledged: 1, skipped: 0 })

  render(History)
  const checkbox = await screen.findByRole('checkbox', { name: 'Select run 7 for acknowledgement' })
  await fireEvent.click(checkbox)
  const action = screen.getByRole('button', { name: 'Acknowledge selected' })
  await fireEvent.click(action)
  await screen.findByText('Could not save review')
  expect(checkbox.checked).toBe(true)

  await fireEvent.click(action)
  await screen.findByText('No matching runs')
  expect(api.acknowledgeRuns).toHaveBeenCalledTimes(2)
  expect(api.acknowledgeRuns).toHaveBeenLastCalledWith([7], '')
})

describe('filters in the URL (#693)', () => {
  it('reads every filter from the hash on load', async () => {
    window.location.hash = '#/history?status=failed&job=1&kind=backup&q=VM'
    render(History)
    await screen.findByText('VM Backups', { selector: 'span' })
    expect(screen.getByRole('button', { name: 'Failed', pressed: true })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Backups', pressed: true })).toBeTruthy()
    expect(/** @type {HTMLSelectElement} */ (screen.getByRole('combobox', { name: 'Filter by job' })).value).toBe('1')
    expect(/** @type {HTMLInputElement} */ (screen.getByRole('textbox', { name: 'Search runs' })).value).toBe('VM')
  })

  it('writes filter changes to the hash without adding history entries', async () => {
    const replace = vi.spyOn(window.history, 'replaceState')
    const length = window.history.length
    render(History)
    await screen.findByText('Docker', { selector: 'span' })
    await fireEvent.click(screen.getByRole('button', { name: 'Failed' }))
    await fireEvent.input(screen.getByRole('textbox', { name: 'Search runs' }), { target: { value: 'VM' } })
    await waitFor(() => expect(window.location.hash).toBe('#/history?status=failed&q=VM'))
    expect(replace).toHaveBeenCalled()
    expect(window.history.length).toBe(length)
    replace.mockRestore()
  })

  it('applies a new hash while the page is open', async () => {
    render(History)
    await screen.findByText('Docker', { selector: 'span' })
    window.location.hash = '#/history?status=completed'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Completed', pressed: true })).toBeTruthy())
    expect(screen.queryByText('VM Backups', { selector: 'span' })).toBeNull()
  })
})

describe('empty states (#695)', () => {
  it('offers a setup step on a fresh install', async () => {
    api.listJobs.mockResolvedValue([])
    api.getHistory.mockResolvedValue([])
    render(History)
    expect(await screen.findByText('No runs yet')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Create a job' }).getAttribute('href')).toBe('#/jobs')
    expect(screen.queryByText('No matching runs')).toBeNull()
  })

  it('suggests running a backup once a job exists', async () => {
    api.getHistory.mockResolvedValue([])
    render(History)
    expect(await screen.findByRole('link', { name: 'Run a backup' })).toBeTruthy()
  })

  it('offers Clear filters when filters hide every run', async () => {
    window.location.hash = '#/history?q=nothing-matches'
    render(History)
    await screen.findByText('No matching runs')
    await fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(await screen.findByText('Docker', { selector: 'span' })).toBeTruthy()
    await waitFor(() => expect(window.location.hash).toBe('#/history'))
  })
})

describe('failed loads (#681)', () => {
  it('shows a failed trend fetch in the chart area with Retry', async () => {
    api.getHistoryTrend.mockReset()
    api.getHistoryTrend.mockRejectedValueOnce(new Error('HTTP 500')).mockResolvedValue(TREND)
    render(History)
    expect(await screen.findByText("Couldn't load the size trend for 30d")).toBeTruthy()
    expect(screen.queryByRole('table', { name: 'Backup size by date' })).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('table', { name: 'Backup size by date' })).toBeTruthy()
  })

  it('drops the previous period’s chart when the new period fails', async () => {
    api.getHistoryTrend.mockReset()
    api.getHistoryTrend.mockResolvedValueOnce(TREND).mockRejectedValueOnce(new Error('HTTP 500'))
    render(History)
    await screen.findByRole('table', { name: 'Backup size by date' })
    await fireEvent.click(screen.getByRole('button', { name: '7d' }))
    expect(await screen.findByText("Couldn't load the size trend for 7d")).toBeTruthy()
    expect(screen.queryByRole('table', { name: 'Backup size by date' })).toBeNull()
  })

  it('retries a failed page load', async () => {
    api.getHistory.mockReset()
    api.getHistory.mockRejectedValueOnce(new Error('HTTP 500')).mockResolvedValue([completed])
    render(History)
    expect(await screen.findByText("Couldn't load history")).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Docker', { selector: 'span' })).toBeTruthy()
  })
})

describe('hierarchy (#699)', () => {
  it('shows the bulk toolbar only once a run is selected', async () => {
    render(History)
    const select = await screen.findByRole('checkbox', { name: 'Select run 7 for acknowledgement' })
    expect(screen.queryByRole('region', { name: 'Run acknowledgement' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Select all needing review (1)' })).toBeTruthy()
    await fireEvent.click(select)
    const toolbar = screen.getByRole('region', { name: 'Run acknowledgement' })
    expect(within(toolbar).getByText('1 selected')).toBeTruthy()
    await fireEvent.click(within(toolbar).getByRole('button', { name: 'Clear selection' }))
    expect(screen.queryByRole('region', { name: 'Run acknowledgement' })).toBeNull()
  })

  it('keeps Purge in an overflow menu and returns focus to its trigger', async () => {
    render(History)
    await screen.findByText('Docker', { selector: 'span' })
    expect(screen.queryByRole('menuitem')).toBeNull()
    const more = screen.getByRole('button', { name: 'More actions' })
    expect(more.getAttribute('aria-haspopup')).toBe('menu')
    await fireEvent.click(more)
    expect(more.getAttribute('aria-expanded')).toBe('true')
    const purge = await screen.findByRole('menuitem', { name: 'Purge history…' })
    await waitFor(() => expect(document.activeElement).toBe(purge))
    await fireEvent.keyDown(purge, { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(more)

    await fireEvent.click(more)
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Purge history…' }))
    expect(await screen.findByRole('dialog', { name: 'Purge All History' })).toBeTruthy()
    // The menu stays behind the modal, so cancelling returns focus to Purge.
    expect(screen.getByRole('menu', { hidden: true })).toBeTruthy()
  })
})

describe('labels and error hints (#694, #711)', () => {
  it('labels statuses with the shared words and counts with tabular figures', async () => {
    render(History)
    const run = await screen.findByText('Docker', { selector: 'span' })
    const badge = /** @type {HTMLElement} */ (run.nextElementSibling)
    expect(badge.textContent).toBe('Completed')
    expect(screen.queryByText('completed', { exact: true })).toBeNull()
    const summary = /** @type {HTMLElement} */ (document.querySelector('dl[data-run-summary]'))
    for (const value of summary.querySelectorAll('dd')) expect(value.className).toContain('tabular-nums')
  })

  it('explains a socket error and keeps the raw text under Details', async () => {
    const raw = 'backup chunked: backup volume /var/run/docker.sock: folder: open source root: open /run/docker.sock: no such device or address'
    api.getHistory.mockResolvedValue([{ ...failed, status: 'partial', log: JSON.stringify([{ name: 'portainer', type: 'container', status: 'failed', error: raw }]) }])
    render(History)
    expect(await screen.findByText("The Docker socket can't be backed up.")).toBeTruthy()
    expect(screen.getByText(/Exclude \/var\/run\/docker.sock/)).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Edit VM Backups' }).getAttribute('href')).toBe('#/jobs?edit=1&item=portainer')
    const details = /** @type {HTMLDetailsElement} */ (screen.getByText('Details').closest('details'))
    expect(details.open).toBe(false)
    expect(within(details).getByText(raw)).toBeTruthy()
  })

  it('shows an unknown error as raw text', async () => {
    api.getHistory.mockResolvedValue([{ ...failed, log: JSON.stringify([{ name: 'db', type: 'container', status: 'failed', error: 'checksum mismatch in chunk 42' }]) }])
    render(History)
    expect((await screen.findAllByText('checksum mismatch in chunk 42')).length).toBeGreaterThan(0)
    expect(screen.queryByText('Details')).toBeNull()
  })
})

describe('run links (#720)', () => {
  // Newest first, as the API returns them: run 30 is on top and run 1 is last.
  const LOG = JSON.stringify([{ name: 'plex', type: 'container', status: 'completed' }])
  const many = Array.from({ length: 30 }, (_, i) => ({
    id: 30 - i, job_id: 2, status: 'completed', log: LOG,
    started_at: new Date(Date.UTC(2026, 9, 30 - i, 12)).toISOString(),
  }))
  const runButton = (id) => document.getElementById(`run-${id}`)?.querySelector('button[aria-expanded]')

  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
    api.getHistory.mockResolvedValue(many)
  })

  it('shows, expands and scrolls to a run beyond the first page', async () => {
    window.location.hash = '#/history?run=6'
    render(History)
    await waitFor(() => expect(runButton(6)?.getAttribute('aria-expanded')).toBe('true'))
    expect(document.getElementById('run-details-6')).toBeTruthy()
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalled())
    expect(api.getRun).not.toHaveBeenCalled()
    await waitFor(() => expect(window.location.hash).toBe('#/history'))
  })

  it('keeps the link in the URL until the run is shown', async () => {
    window.location.hash = '#/history?run=6'
    let finish = () => {}
    api.getHistory.mockReturnValue(new Promise((resolve) => { finish = () => resolve(many) }))
    render(History)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(window.location.hash).toBe('#/history?run=6')
    finish()
    await waitFor(() => expect(runButton(6)?.getAttribute('aria-expanded')).toBe('true'))
  })

  it('reveals a run linked while the page is open', async () => {
    render(History)
    await waitFor(() => expect(runButton(30)).toBeTruthy())
    expect(runButton(3)).toBeFalsy()
    window.location.hash = '#/history?run=3'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await waitFor(() => expect(runButton(3)?.getAttribute('aria-expanded')).toBe('true'))
  })

  it('clears filters that would hide the linked run', async () => {
    window.location.hash = '#/history?status=failed&run=6'
    render(History)
    await waitFor(() => expect(runButton(6)?.getAttribute('aria-expanded')).toBe('true'))
    expect(screen.getByRole('button', { name: 'Failed', pressed: false })).toBeTruthy()
  })

  it('fetches a run outside the loaded history on its own', async () => {
    api.getRun.mockResolvedValue({ id: 500, job_id: 1, status: 'failed', log: LOG, started_at: '2025-01-01T12:00:00Z' })
    window.location.hash = '#/history?run=500'
    render(History)
    await waitFor(() => expect(runButton(500)?.getAttribute('aria-expanded')).toBe('true'))
    expect(api.getRun).toHaveBeenCalledWith(500)
    expect(within(/** @type {HTMLElement} */ (document.getElementById('run-500'))).getByText('VM Backups')).toBeTruthy()
  })

  it('says so when the linked run no longer exists', async () => {
    api.getRun.mockRejectedValue(Object.assign(new Error('run not found'), { status: 404 }))
    window.location.hash = '#/history?run=999'
    render(History)
    expect(await screen.findByText(/Run 999 wasn.t found/)).toBeTruthy()
    await waitFor(() => expect(window.location.hash).toBe('#/history'))
    await fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText(/Run 999 wasn.t found/)).toBeNull()
  })

  it('reports a failed run lookup', async () => {
    api.getRun.mockRejectedValue(Object.assign(new Error('HTTP 500'), { status: 500 }))
    window.location.hash = '#/history?run=999'
    render(History)
    expect(await screen.findByText(/Couldn.t load run 999: HTTP 500/)).toBeTruthy()
  })
})
