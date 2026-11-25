import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import LoadError from './LoadError.svelte'

describe('LoadError', () => {
  it('says what failed and why', () => {
    render(LoadError, { title: "Couldn't load anomalies", message: 'HTTP 500' })
    expect(screen.getByText("Couldn't load anomalies")).toBeTruthy()
    expect(screen.getByText('HTTP 500')).toBeTruthy()
  })

  it('offers Retry only when it can retry', () => {
    render(LoadError, { title: "Couldn't load anomalies" })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('calls onretry from the Retry button', async () => {
    const onretry = vi.fn()
    render(LoadError, { title: "Couldn't load anomalies", onretry })
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onretry).toHaveBeenCalledOnce()
  })

  it('disables Retry while retrying', () => {
    render(LoadError, { title: "Couldn't load anomalies", onretry: () => {}, retrying: true })
    const button = /** @type {HTMLButtonElement} */ (screen.getByRole('button', { name: 'Retrying…' }))
    expect(button.disabled).toBe(true)
  })
})
