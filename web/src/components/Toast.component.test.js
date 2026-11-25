// Toasts: colour is never the only cue (#524), and none of them vanishes while
// it still matters (#685): warnings and errors stay, a later toast stacks
// instead of replacing one, and hover or focus holds the timer.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
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

/** Raises the next toast on a mounted Toast, the way pages bump `key`. */
async function next(view, key, type, message) {
  await view.rerender({ key, type, message })
  await tick()
}

const toasts = (container) => [...container.querySelectorAll('[data-toast]')]

describe('Toast', () => {
  it.each(['success', 'error', 'warning', 'info'])('renders a decorative %s icon with its message', async (type) => {
    const { container } = await show({ type, message: `${type} message` })
    expect(toasts(container)[0].textContent).toContain(`${type} message`)
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

  it('shows nothing before the first toast', async () => {
    const { container } = render(Toast, { key: 0, message: '' })
    await tick()
    expect(toasts(container)).toHaveLength(0)
  })

  describe('roles', () => {
    it.each([['error', 'alert'], ['warning', 'alert'], ['success', 'status'], ['info', 'status']])('a %s toast is a %s, with no aria-live of its own', async (type, role) => {
      await show({ type, message: `${type} message` })
      const toast = screen.getByRole(role)
      expect(toast.textContent).toContain(`${type} message`)
      expect(toast.hasAttribute('aria-live')).toBe(false)
    })
  })

  describe('lifetime', () => {
    it('auto-dismisses success and info after 4 s', async () => {
      vi.useFakeTimers()
      for (const type of ['success', 'info']) {
        const view = await show({ type, message: 'ok' })
        vi.advanceTimersByTime(3999)
        await tick()
        expect(toasts(view.container)).toHaveLength(1)
        vi.advanceTimersByTime(1)
        await tick()
        expect(toasts(view.container)).toHaveLength(0)
        view.unmount()
      }
    })

    it.each(['error', 'warning'])('keeps a %s until it is dismissed', async (type) => {
      vi.useFakeTimers()
      const view = await show({ type, message: 'needs reading' })
      vi.advanceTimersByTime(60_000)
      await tick()
      expect(toasts(view.container)).toHaveLength(1)
      await fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
      await tick()
      expect(toasts(view.container)).toHaveLength(0)
    })

    it('keeps an error visible after a later success', async () => {
      vi.useFakeTimers()
      const view = await show({ type: 'error', message: 'Restore failed' })
      await next(view, 2, 'success', 'Saved')
      expect(screen.getByRole('alert').textContent).toContain('Restore failed')
      expect(screen.getByRole('status').textContent).toContain('Saved')
      vi.advanceTimersByTime(4000)
      await tick()
      expect(toasts(view.container).map((t) => t.textContent.trim())).toEqual(['Restore failed'])
    })

    it('stacks newest last and dismisses only the one asked', async () => {
      const view = await show({ type: 'warning', message: 'first' })
      await next(view, 2, 'error', 'second')
      expect(toasts(view.container).map((t) => t.textContent.trim())).toEqual(['first', 'second'])
      await fireEvent.click(within(toasts(view.container)[0]).getByRole('button', { name: 'Dismiss' }))
      await tick()
      expect(toasts(view.container).map((t) => t.textContent.trim())).toEqual(['second'])
    })

    it('caps the stack, dropping the oldest passing toast before any warning', async () => {
      const view = await show({ type: 'warning', message: 'w1' })
      for (let k = 2; k <= 6; k++) await next(view, k, 'success', `s${k}`)
      const shown = toasts(view.container).map((t) => t.textContent.trim())
      expect(shown).toHaveLength(5)
      expect(shown[0]).toBe('w1')
      expect(shown).not.toContain('s2')
    })

    it('ignores a message change that comes without a new key', async () => {
      const view = await show({ type: 'success', message: 'one' })
      await view.rerender({ key: 1, type: 'success', message: 'two' })
      await tick()
      expect(toasts(view.container).map((t) => t.textContent.trim())).toEqual(['one'])
    })
  })

  describe('pausing', () => {
    it('holds the timer while the pointer is over the stack', async () => {
      vi.useFakeTimers()
      const view = await show({ type: 'success', message: 'ok' })
      vi.advanceTimersByTime(3000)
      await fireEvent.mouseEnter(toasts(view.container)[0].parentElement)
      vi.advanceTimersByTime(10_000)
      await tick()
      expect(toasts(view.container)).toHaveLength(1)
      await fireEvent.mouseLeave(toasts(view.container)[0].parentElement)
      vi.advanceTimersByTime(999)
      await tick()
      expect(toasts(view.container)).toHaveLength(1)
      vi.advanceTimersByTime(1)
      await tick()
      expect(toasts(view.container)).toHaveLength(0)
    })

    it('holds the timer while focus is inside the stack', async () => {
      vi.useFakeTimers()
      const view = await show({ type: 'info', message: 'fyi' })
      const dismiss = screen.getByRole('button', { name: 'Dismiss' })
      await fireEvent.focusIn(dismiss)
      vi.advanceTimersByTime(10_000)
      await tick()
      expect(toasts(view.container)).toHaveLength(1)
      await fireEvent.focusOut(dismiss)
      vi.advanceTimersByTime(4000)
      await tick()
      expect(toasts(view.container)).toHaveLength(0)
    })

    it('keeps holding while focus is in the stack after the pointer leaves', async () => {
      vi.useFakeTimers()
      const view = await show({ type: 'success', message: 'ok' })
      const stack = toasts(view.container)[0].parentElement
      await fireEvent.mouseEnter(stack)
      await fireEvent.focusIn(screen.getByRole('button', { name: 'Dismiss' }))
      await fireEvent.mouseLeave(stack)
      vi.advanceTimersByTime(10_000)
      await tick()
      expect(toasts(view.container)).toHaveLength(1)
    })
  })
})
