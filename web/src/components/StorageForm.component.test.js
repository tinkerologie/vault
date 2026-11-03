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

// The SFTP form lets users set the host key. A refused connection tells them
// to do this (#551).
const sftp = (config) => ({ name: 'nas', type: 'sftp', config: JSON.stringify(config) })

describe('StorageForm SFTP host key', () => {
  it('shows the stored fingerprint when editing', () => {
    render(StorageForm, { initial: sftp({ host: 'nas', host_key: 'SHA256:abc' }) })
    expect(document.getElementById('cfg_shostkey').value).toBe('SHA256:abc')
  })

  it('accepts a new fingerprint', async () => {
    render(StorageForm, { initial: sftp({ host: 'nas' }) })
    const input = document.getElementById('cfg_shostkey')
    await fireEvent.input(input, { target: { value: 'SHA256:new' } })
    expect(input.value).toBe('SHA256:new')
  })
})
