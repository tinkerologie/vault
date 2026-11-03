// Replication's edit modal now holds a masked API key: the server never sends
// the real one, and it resolves a blank or masked key on save.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({
  listReplicationSources: vi.fn(),
  listStorage: vi.fn(async () => []),
  updateReplicationSource: vi.fn(async () => ({})),
  createReplicationSource: vi.fn(async () => ({})),
  testReplicationSource: vi.fn(async () => ({ status: 'ok' })),
  testReplicationURL: vi.fn(async () => ({ status: 'ok' })),
}))
vi.mock('../lib/api.js', () => ({ api: new Proxy(api, { get: (t, k) => t[k] ?? (async () => []) }), isReplicaMode: () => false }))
vi.mock('../lib/ws.svelte.js', () => ({ onWsMessage: () => () => {} }))

import Replication from './Replication.svelte'

const source = (over = {}) => ({
  id: 7, name: 'peer', type: 'remote_vault', url: 'http://peer.lan:24085',
  config: '{"api_key":"••••••••"}', storage_dest_id: 1, schedule: '0 3 * * *', enabled: true,
  ...over,
})

async function openEdit(src) {
  api.listReplicationSources.mockResolvedValue([src])
  render(Replication)
  await fireEvent.click(await screen.findByRole('button', { name: /edit/i }))
}

describe('Replication edit form with a masked key', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows a blank key field, not the mask', async () => {
    await openEdit(source())
    expect(screen.getByLabelText(/API Key/i).value).toBe('')
  })

  it('tests the stored key through the source endpoint while the URL is unchanged', async () => {
    await openEdit(source())
    await fireEvent.click(screen.getByRole('button', { name: /test connection/i }))
    await vi.waitFor(() => expect(api.testReplicationSource).toHaveBeenCalledWith(7))
    expect(api.testReplicationURL).not.toHaveBeenCalled()
  })

  it('asks for the key before testing a changed URL', async () => {
    await openEdit(source())
    await fireEvent.input(screen.getByLabelText(/Remote Vault URL/i), { target: { value: 'http://other.lan:24085' } })
    await fireEvent.click(screen.getByRole('button', { name: /test connection/i }))
    await screen.findByText(/Enter an API key/i)
    expect(api.testReplicationSource).not.toHaveBeenCalled()
    expect(api.testReplicationURL).not.toHaveBeenCalled()
  })

  it('tests a typed key against the typed URL', async () => {
    await openEdit(source())
    await fireEvent.input(screen.getByLabelText(/API Key/i), { target: { value: 'new-key' } })
    await fireEvent.click(screen.getByRole('button', { name: /test connection/i }))
    await vi.waitFor(() => expect(api.testReplicationURL).toHaveBeenCalledWith('http://peer.lan:24085', 'new-key'))
  })

  it('warns about plain HTTP and locked keys', async () => {
    await openEdit(source({ credentials_locked: true }))
    expect(screen.getByText(/Plain HTTP/)).toBeTruthy()
    expect(screen.getByText(/can't open the stored key/)).toBeTruthy()
  })
})
