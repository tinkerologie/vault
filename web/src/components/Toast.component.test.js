// Toast status cues (#524): colour is never the only signal, and text contrast
// never depends on the page under the toast.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { tick } from 'svelte'
import Toast from './Toast.svelte'

afterEach(() => {
  vi.useRealTimers()
})

async function show(props) {
  const view = render(Toast, { key: 1, ...props })
  await tick()
  return view
}

describe('Toast', () => {
  it.each(['success', 'error', 'warning', 'info'])('renders a decorative %s icon with its message', async (type) => {
    const { container } = await show({ type, message: `${type} message` })
    expect(screen.getByRole('alert').textContent).toContain(`${type} message`)
    const icon = container.querySelector(`[data-toast-icon="${type}"]`)
    expect(icon).not.toBeNull()
    expect(icon.getAttribute('aria-hidden')).toBe('true')
  })

  it('gives each type a distinct icon', async () => {
    const paths = new Set()
    for (const type of ['success', 'error', 'warning', 'info']) {
      const { container, unmount } = await show({ type, message: 'm' })
      paths.add(container.querySelector('[data-toast-icon] path').getAttribute('d'))
      unmount()
    }
    expect(paths.size).toBe(4)
  })

  it('falls back to the info icon and colours for an unknown type', async () => {
    const { container } = await show({ type: 'mystery', message: 'm' })
    expect(container.querySelector('[data-toast-icon="info"]')).not.toBeNull()
    expect(container.querySelector('.text-info')).not.toBeNull()
  })

  it('layers the tint over an opaque surface', async () => {
    const { container } = await show({ type: 'error', message: 'm' })
    const tint = container.querySelector('.bg-danger\\/20')
    expect(tint.parentElement.classList.contains('bg-surface-2')).toBe(true)
  })

  it('auto-dismisses non-errors after 4s, and keeps errors', async () => {
    vi.useFakeTimers()
    const ok = await show({ type: 'success', message: 'ok' })
    vi.advanceTimersByTime(4000)
    await tick()
    expect(ok.container.querySelector('[role="alert"]')).toBeNull()
    ok.unmount()

    const err = await show({ type: 'error', message: 'bad' })
    vi.advanceTimersByTime(4000)
    await tick()
    expect(err.container.querySelector('[role="alert"]')).not.toBeNull()
  })

  it('can be dismissed', async () => {
    await show({ type: 'error', message: 'bad' })
    await fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    await tick()
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
