// ConfirmDialog composes Modal (#528). #525 builds typed confirmation on
// `confirmDisabled` and `children`, and relies on Cancel being the safe default.
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { createRawSnippet, tick } from 'svelte'
import ConfirmDialog from './ConfirmDialog.svelte'

const html = (markup) => createRawSnippet(() => ({ render: () => markup }))

async function open(props = {}) {
  const view = render(ConfirmDialog, { show: true, title: 'Delete it', message: 'Sure?', confirmLabel: 'Delete', ...props })
  await tick()
  return view
}

describe('ConfirmDialog', () => {
  it('renders nothing while closed', () => {
    render(ConfirmDialog, { show: false })
    expect(document.querySelector('dialog')).toBeNull()
  })

  it('is one native dialog, named and described by its text', async () => {
    await open()
    const dialog = screen.getByRole('dialog', { name: 'Delete it' })
    expect(dialog.tagName).toBe('DIALOG')
    expect(document.getElementById(dialog.getAttribute('aria-describedby')).textContent.trim()).toBe('Sure?')
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
  })

  it('starts on Cancel, the safe default', async () => {
    await open()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))
  })

  it('fires confirm and cancel once each', async () => {
    const onconfirm = vi.fn()
    const oncancel = vi.fn()
    await open({ onconfirm, oncancel })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(onconfirm).toHaveBeenCalledTimes(1)
    expect(oncancel).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(oncancel).toHaveBeenCalledTimes(1)
  })

  it('maps Escape to cancel', async () => {
    const oncancel = vi.fn()
    await open({ oncancel })
    document.querySelector('dialog').dispatchEvent(new Event('cancel', { cancelable: true }))
    expect(oncancel).toHaveBeenCalledTimes(1)
  })

  it('a disabled confirm does nothing, and can be re-enabled', async () => {
    const onconfirm = vi.fn()
    const { rerender } = await open({ onconfirm, confirmDisabled: true })
    const confirm = screen.getByRole('button', { name: 'Delete' })
    expect(confirm.disabled).toBe(true)
    confirm.click() // a real click: fireEvent would bypass the browser's disabled check
    expect(onconfirm).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))

    await rerender({ confirmDisabled: false })
    await fireEvent.click(confirm)
    expect(onconfirm).toHaveBeenCalledTimes(1)
  })

  it('renders children and a description snippet', async () => {
    await open({ description: html('<span>Delete <strong>plex</strong>?</span>'), children: html('<div><label>Extra <input /></label></div>') })
    expect(screen.getByRole('dialog').querySelector('strong').textContent).toBe('plex')
    expect(screen.queryByText('Sure?')).toBeNull()
    expect(screen.getByLabelText('Extra')).toBeTruthy()
  })

  it('lets a field in children take initial focus', async () => {
    await open({ children: html('<div><input id="typed" data-autofocus /></div>') })
    expect(document.activeElement.id).toBe('typed')
  })

  it('falls back to danger styling for an unknown variant', async () => {
    await open({ variant: 'mystery' })
    expect(screen.getByRole('button', { name: 'Delete' }).className).toContain('bg-danger')
  })
})
