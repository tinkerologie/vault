import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import Settings from './Settings.svelte'
import { navigate, getRoute } from '../lib/router.svelte.js'
import { api } from '../lib/api.js'

// Every Settings section with its own Save reports unsaved edits against its
// last saved value, and leaving the page asks first (#719).

vi.mock('../lib/api.js', () => ({
  isReplicaMode: () => false,
  api: {
    health: vi.fn(async () => ({ version: 'test' })),
    getSettings: vi.fn(async () => ({ replication_enabled: 'false', retry_max_default: '2' })),
    updateSettings: vi.fn(async (patch) => ({ replication_enabled: 'false', retry_max_default: '2', ...patch })),
    getEncryptionStatus: vi.fn(async () => null),
    getStagingInfo: vi.fn(async () => null),
    getDatabaseInfo: vi.fn(async () => null),
    getAPIKeyStatus: vi.fn(async () => null),
    getChangelog: vi.fn(async () => []),
    getLatestRelease: vi.fn(async () => null),
    getAppdataPath: vi.fn(async () => null),
    getRuntime: vi.fn(async () => null),
    listAssistantKeys: vi.fn(async () => []),
  },
}))

async function openSettings() {
  await navigate('/settings')
  render(Settings)
  return screen.findByRole('spinbutton', { name: 'Max retries' })
}

describe('Settings unsaved changes', () => {
  it('is clean after loading, even once the retry delays editor fills its default', async () => {
    await openSettings()
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('Unsaved changes')).toBeNull()
    await navigate('/logs')
    expect(getRoute()).toBe('/logs')
  })

  it('asks before leaving with an unsaved Retry Policy and stays on Keep editing', async () => {
    const max = await openSettings()
    await fireEvent.input(max, { target: { value: '5' } })
    expect(await screen.findByText('Unsaved changes')).toBeTruthy()

    const leaving = navigate('/logs')
    const dialog = await screen.findByRole('dialog', { name: 'Discard changes?' })
    expect(dialog.textContent).toContain('Unsaved changes in Retry Policy will be lost.')
    await fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }))
    await leaving
    expect(getRoute()).toBe('/settings')

    // Switching Settings tabs keeps the edit, so it doesn't ask.
    await navigate('/settings/notifications')
    expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).toBeNull()
    await fireEvent.input(await screen.findByLabelText('Bot Username'), { target: { value: 'Vault bot' } })
    const again = navigate('/logs')
    const dialog2 = await screen.findByRole('dialog', { name: 'Discard changes?' })
    expect(dialog2.textContent).toContain('Retry Policy, Discord')
    await fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    await again
    expect(getRoute()).toBe('/logs')
  })

  it('clears a section once it saves', async () => {
    const max = await openSettings()
    await fireEvent.input(max, { target: { value: '4' } })
    expect(await screen.findByText('Unsaved changes')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Save Retry Policy' }))
    await waitFor(() => expect(screen.queryByText('Unsaved changes')).toBeNull())
    expect(api.updateSettings).toHaveBeenCalledWith(expect.objectContaining({ retry_max_default: '4' }))
    await navigate('/logs')
    expect(getRoute()).toBe('/logs')
  })
})
