import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Settings from './Settings.svelte'
import { api } from '../lib/api.js'
import { copyText } from '../lib/clipboard.js'

const token = 'vault_ak_' + 'A'.repeat(43)
const record = { id: 1, name: 'Recovery advisor', created_at: '2026-11-12T00:00:00Z' }

vi.mock('../lib/api.js', () => ({
  isReplicaMode: () => false,
  api: {
    health: vi.fn(async () => ({ version: 'test' })),
    getSettings: vi.fn(async () => ({ replication_enabled: 'false' })),
    getEncryptionStatus: vi.fn(async () => null),
    getStagingInfo: vi.fn(async () => null),
    getDatabaseInfo: vi.fn(async () => null),
    getAPIKeyStatus: vi.fn(async () => null),
    getChangelog: vi.fn(async () => []),
    getLatestRelease: vi.fn(async () => null),
    getAppdataPath: vi.fn(async () => null),
    getRuntime: vi.fn(async () => null),
    listAssistantKeys: vi.fn(async () => []),
    createAssistantKey: vi.fn(async () => ({ ...record, assistant_key: token })),
    revokeAssistantKey: vi.fn(async () => null),
    setAssistantRestoreRequest: vi.fn(async (_id, enabled) => ({ ...record, restore_request: enabled })),
  },
}))

vi.mock('../lib/clipboard.js', () => ({ copyText: vi.fn(async () => true) }))

describe('Settings Assistant Keys', () => {
  it('shows a new key once and confirms revocation by name', async () => {
    render(Settings)
    await fireEvent.click(await screen.findByRole('tab', { name: 'Security' }))
    const section = await screen.findByRole('region', { name: 'Assistant Keys' })
    await within(section).findByText('No Assistant Keys')

    await fireEvent.input(within(section).getByRole('textbox', { name: 'Assistant name' }), {
      target: { value: record.name },
    })
    await fireEvent.click(within(section).getByRole('button', { name: 'Create Assistant Key' }))
    expect(api.createAssistantKey).toHaveBeenCalledWith(record.name)
    expect(await within(section).findByText(token)).toBeTruthy()
    await fireEvent.click(within(section).getByRole('button', { name: 'Copy Assistant Key' }))
    expect(copyText).toHaveBeenCalledWith(token)
    await fireEvent.click(within(section).getByRole('button', { name: 'Dismiss Assistant Key' }))
    expect(within(section).queryByText(token)).toBeNull()

    const permission = within(section).getByRole('checkbox', { name: 'Allow Restore Requests for Recovery advisor' })
    expect(permission.checked).toBe(false)
    await fireEvent.click(permission)
    await waitFor(() => expect(permission.disabled).toBe(false))
    expect(api.setAssistantRestoreRequest).toHaveBeenLastCalledWith(record.id, true)
    expect(permission.checked).toBe(true)
    await fireEvent.click(permission)
    await waitFor(() => expect(permission.disabled).toBe(false))
    expect(api.setAssistantRestoreRequest).toHaveBeenLastCalledWith(record.id, false)
    expect(permission.checked).toBe(false)

    await fireEvent.click(within(section).getByRole('button', { name: 'Revoke Recovery advisor' }))
    const dialog = await screen.findByRole('dialog', { name: 'Revoke Assistant Key' })
    expect(within(dialog).getByText(record.name)).toBeTruthy()
    expect(api.revokeAssistantKey).not.toHaveBeenCalled()
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke key' }))
    expect(api.revokeAssistantKey).toHaveBeenCalledWith(record.id)
    expect(await within(section).findByText('No Assistant Keys')).toBeTruthy()
  })
})
