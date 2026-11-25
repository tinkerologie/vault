// The one switch (#698): both markups keep their semantics, the caller owns
// the state, and every part carries its 3:1 edge token.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import Switch from './Switch.svelte'

describe('Switch (button)', () => {
  it('is a named switch that reports its state', () => {
    render(Switch, { checked: true, label: 'Toggle notifications' })
    const sw = screen.getByRole('switch', { name: 'Toggle notifications' })
    expect(sw.getAttribute('aria-checked')).toBe('true')
    expect(sw.getAttribute('type')).toBe('button')
  })

  it('asks the caller to change state instead of flipping itself', async () => {
    const onchange = vi.fn()
    render(Switch, { checked: false, label: 'Toggle replication', onchange })
    const sw = screen.getByRole('switch')
    await fireEvent.click(sw)
    expect(onchange).toHaveBeenCalledWith(true)
    expect(sw.getAttribute('aria-checked')).toBe('false')
  })

  it('flips itself when no handler is given', async () => {
    render(Switch, { label: 'Local' })
    const sw = screen.getByRole('switch')
    await fireEvent.click(sw)
    expect(sw.getAttribute('aria-checked')).toBe('true')
  })

  it('stops the click reaching a clickable row when asked', async () => {
    const row = vi.fn()
    // Svelte delegates click handlers to the mount target, so the row listens
    // one level above it, as the Jobs row's own handler effectively does.
    const rowEl = document.createElement('div')
    const mount = document.createElement('div')
    rowEl.append(mount)
    rowEl.addEventListener('click', row)
    document.body.append(rowEl)
    const onchange = vi.fn()
    render(Switch, { target: mount, props: { label: 'Enable Job', stopPropagation: true, onchange } })
    await fireEvent.click(screen.getByRole('switch'))
    expect(onchange).toHaveBeenCalledWith(true)
    expect(row).not.toHaveBeenCalled()
    rowEl.remove()
  })

  it('does nothing while disabled', async () => {
    const onchange = vi.fn()
    render(Switch, { label: 'Busy', disabled: true, onchange })
    const sw = screen.getByRole('switch')
    expect(sw.disabled).toBe(true)
    await fireEvent.click(sw)
    expect(onchange).not.toHaveBeenCalled()
  })

  it('passes extra attributes such as data-testid and title to the control', () => {
    render(Switch, { label: 'X', title: 'Only for remote storage', 'data-testid': 'sw' })
    const sw = screen.getByTestId('sw')
    expect(sw.getAttribute('role')).toBe('switch')
    expect(sw.getAttribute('title')).toBe('Only for remote storage')
  })

  it('draws the off and on edges from the switch tokens', () => {
    const { container, rerender } = render(Switch, { label: 'X', checked: false })
    const track = container.querySelector('.switch-track')
    const thumb = track.firstElementChild
    expect(track.className).toContain('bg-surface-4')
    expect(track.className).toContain('var(--color-switch-edge)')
    expect(thumb.className).toContain('bg-switch-thumb')
    expect(track.getAttribute('aria-hidden')).toBe('true')
    return rerender({ checked: true }).then(() => {
      expect(track.className).toContain('bg-vault')
      expect(track.className).toContain('var(--color-focus-ring)')
      expect(thumb.className).toContain('var(--color-on-vault)')
    })
  })

  it('offers a larger size', () => {
    const { container } = render(Switch, { label: 'X', size: 'md' })
    expect(container.querySelector('.switch-track').className).toContain('w-11 h-6')
  })
})

describe('Switch (native checkbox)', () => {
  it('is a checkbox labelled from outside, with its track after it', async () => {
    const onchange = vi.fn()
    const { container } = render(Switch, { native: true, label: 'Only back up when idle', onchange })
    const box = screen.getByRole('checkbox', { name: 'Only back up when idle' })
    expect(box.classList.contains('peer')).toBe(true)
    expect(box.nextElementSibling.classList.contains('switch-track')).toBe(true)
    await fireEvent.click(box)
    expect(box.checked).toBe(true)
    expect(onchange).toHaveBeenCalledWith(true)
    expect(container.querySelector('.switch-track').className).toContain('bg-vault')
  })

  it('keeps aria-labelledby, id and the title on the wrapping label', () => {
    const { container } = render(Switch, { native: true, id: 'job-defer', 'aria-labelledby': 'lbl', title: 'Remote only', disabled: true })
    const box = container.querySelector('input')
    expect(box.id).toBe('job-defer')
    expect(box.getAttribute('aria-labelledby')).toBe('lbl')
    expect(box.disabled).toBe(true)
    expect(container.querySelector('label').getAttribute('title')).toBe('Remote only')
  })
})
