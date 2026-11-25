// Replication's list states (#681, #692, #694): a failed load is an error with
// Retry, never "No replication targets"; row actions name their target; the Jobs
// toggle reports its state; Last Sync reads as relative time.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  listReplicationSources: vi.fn(),
  listReplicatedJobs: vi.fn(async () => []),
}))
vi.mock('../lib/api.js', () => ({ api: new Proxy(api, { get: (t, k) => t[k] ?? (async () => []) }), isReplicaMode: () => false }))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))

import Replication from './Replication.svelte'

const source = (over = {}) => ({
  id: 7, name: 'peer', type: 'remote_vault', url: 'http://peer.lan:24085',
  config: '{}', storage_dest_id: 1, schedule: '0 3 * * *', enabled: true, created_at: '2026-01-01T00:00:00Z',
  ...over,
})

describe('Replication list states', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows an error with Retry instead of the empty state when loading fails', async () => {
    api.listReplicationSources.mockRejectedValueOnce(new Error('HTTP 500'))
    render(Replication)
    expect(await screen.findByText("Couldn't load replication targets")).toBeTruthy()
    expect(screen.queryByText('No replication targets')).toBeNull()
    expect(screen.queryByRole('button', { name: /add target/i })).toBeNull()

    api.listReplicationSources.mockResolvedValueOnce([source()])
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('peer')).toBeTruthy()
    expect(screen.queryByText("Couldn't load replication targets")).toBeNull()
  })

  it('still shows the empty state when the list really is empty', async () => {
    api.listReplicationSources.mockResolvedValueOnce([])
    render(Replication)
    expect(await screen.findByText('No replication targets')).toBeTruthy()
  })

  it('names each row action after its target', async () => {
    api.listReplicationSources.mockResolvedValueOnce([source(), source({ id: 8, name: 'offsite' })])
    render(Replication)
    expect(await screen.findByRole('button', { name: 'Edit: peer' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Delete: offsite' })).toBeTruthy()
  })

  it('reports whether the Jobs panel is expanded', async () => {
    api.listReplicationSources.mockResolvedValueOnce([source()])
    render(Replication)
    const toggle = await screen.findByRole('button', { name: 'Jobs: peer' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    const panel = document.getElementById(/** @type {string} */ (toggle.getAttribute('aria-controls')))
    expect(panel).toBeTruthy()
  })

  it('shows Last Sync as relative time with the date in the title', async () => {
    const at = new Date(Date.now() - 2 * 3600_000).toISOString()
    api.listReplicationSources.mockResolvedValueOnce([source({ last_sync_at: at, last_sync_status: 'success' })])
    render(Replication)
    const value = await screen.findByText('2h ago')
    expect(value.getAttribute('title')).toBeTruthy()
  })
})
