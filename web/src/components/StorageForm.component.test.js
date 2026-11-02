// Test Connection while editing posts the row's id, so the server can use the
// stored secret in place of the mask.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'

const api = vi.hoisted(() => ({ testStorageConfig: vi.fn(async () => ({ success: true })) }))
vi.mock('../lib/api.js', () => ({ api: new Proxy(api, { get: (t, k) => t[k] ?? (async () => []) }) }))

import StorageForm from './StorageForm.svelte'

describe('StorageForm Test Connection', () => {
  it('sends the destination id when editing', async () => {
    render(StorageForm, { props: { initial: { id: 5, name: 'nas', type: 'smb', config: '{"host":"nas","password":"••••••••"}' } } })
    await fireEvent.click(screen.getByRole('button', { name: /test/i }))
    await vi.waitFor(() => expect(api.testStorageConfig).toHaveBeenCalled())
    expect(api.testStorageConfig.mock.calls[0][0].id).toBe(5)
  })

  it('sends no id when adding', async () => {
    api.testStorageConfig.mockClear()
    render(StorageForm, { props: { initial: null } })
    await fireEvent.click(screen.getByRole('button', { name: /test/i }))
    await vi.waitFor(() => expect(api.testStorageConfig).toHaveBeenCalled())
    expect(api.testStorageConfig.mock.calls[0][0].id).toBeUndefined()
  })
})
