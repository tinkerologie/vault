import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import ApiKeyPrompt from './ApiKeyPrompt.svelte'
import ApiKeyLocalOnlyNotice from './ApiKeyLocalOnlyNotice.svelte'
import { getApiKey } from '../lib/auth.svelte.js'

describe('ApiKeyPrompt', () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.stubGlobal('location', { reload: vi.fn() })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('stores a trimmed key for this tab and reloads', async () => {
    render(ApiKeyPrompt)
    expect(screen.queryByRole('alert')).toBeNull()
    await fireEvent.input(screen.getByLabelText('API key'), { target: { value: '  abc  ' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Continue', hidden: true }))
    expect(getApiKey()).toBe('abc')
    expect(location.reload).toHaveBeenCalledOnce()
  })

  it('points a browser with no key at every way to get one', () => {
    render(ApiKeyPrompt)
    const text = document.body.textContent
    expect(text).toContain('Unraid web UI')
    expect(text).toContain('SSH tunnel')
    // This prompt appears on a LAN-IP bind, where nothing listens on the host's
    // loopback, so the server's own address must be the form it leads with.
    expect(text).toContain('ssh -L 24085:<server-ip>:24085 root@<server-ip>')
    expect(text).toContain('ssh -L 24085:127.0.0.1:24085 root@<server-ip>')
    expect(text).toContain('bound to loopback')
    expect(text).toContain('vault api-key generate')
  })

  it('is a real dialog that Escape and the close button cannot dismiss', async () => {
    render(ApiKeyPrompt)
    const dialog = screen.getByRole('dialog', { hidden: true })
    expect(dialog.tagName).toBe('DIALOG')
    expect(dialog.getAttribute('aria-labelledby')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Close', hidden: true }))
    await fireEvent(dialog, new Event('cancel', { cancelable: true }))
    expect(document.body.contains(dialog)).toBe(true)
  })

  it('names the subject in the local-only notice', () => {
    render(ApiKeyLocalOnlyNotice, { subject: 'vault.key' })
    const text = document.body.textContent
    expect(text).toContain('You can only get to vault.key')
    expect(text).not.toContain('vault api-key')
    expect(text).not.toContain('API Access')
  })

  it('ignores an empty submission', async () => {
    render(ApiKeyPrompt)
    await fireEvent.click(screen.getByRole('button', { name: 'Continue', hidden: true }))
    expect(getApiKey()).toBe('')
    expect(location.reload).not.toHaveBeenCalled()
  })

  it('says so when a stored key was refused', () => {
    sessionStorage.setItem('vault-api-key', 'stale')
    render(ApiKeyPrompt)
    expect(screen.getByRole('alert').textContent).toContain('not accepted')
  })

  it('says so when storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    render(ApiKeyPrompt)
    await fireEvent.input(screen.getByLabelText('API key'), { target: { value: 'abc' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Continue', hidden: true }))
    expect(screen.getByRole('alert').textContent).toContain('session storage')
    expect(location.reload).not.toHaveBeenCalled()
  })
})
