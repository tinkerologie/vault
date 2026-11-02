import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import ApiKeyPrompt from './ApiKeyPrompt.svelte'
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
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(getApiKey()).toBe('abc')
    expect(location.reload).toHaveBeenCalledOnce()
  })

  it('points a browser with no key at every way to get one', () => {
    render(ApiKeyPrompt)
    const text = document.body.textContent
    expect(text).toContain('Unraid web UI')
    expect(text).toContain('SSH tunnel')
    expect(text).toContain('vault api-key generate')
  })

  it('ignores an empty submission', async () => {
    render(ApiKeyPrompt)
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
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
    await fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(screen.getByRole('alert').textContent).toContain('session storage')
    expect(location.reload).not.toHaveBeenCalled()
  })
})
