// The shared dialog's contract (#528). Keyboard behaviour that needs a real
// browser (native inertness, Tab order) is covered in tests/dialog-focus.spec.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { createRawSnippet, tick } from 'svelte'
import Modal from './Modal.svelte'
import { floatAboveDialogs, modalDialog } from '../lib/dialog.js'

const html = (markup) => createRawSnippet(() => ({ render: () => markup }))

let opener
let main

async function open(props = {}, markup = '<button id="a">A</button><button id="b">B</button>') {
  const view = render(Modal, { show: true, title: 'Title', children: html(`<div>${markup}</div>`), ...props })
  await tick()
  return view
}

const flush = async () => {
  await tick()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function pressTab(from, shiftKey = false) {
  from.focus()
  return fireEvent.keyDown(from, { key: 'Tab', shiftKey })
}

beforeEach(() => {
  main = document.createElement('main')
  main.id = 'main-content'
  main.tabIndex = -1
  opener = document.createElement('button')
  opener.textContent = 'Open'
  main.append(opener)
  document.body.append(main)
  opener.focus()
})

afterEach(() => {
  main.remove()
})

describe('Modal', () => {
  it('renders nothing while closed, and a closed modal ignores Escape', async () => {
    const onclose = vi.fn()
    render(Modal, { show: false, title: 'T', onclose, children: html('<div></div>') })
    expect(document.querySelector('dialog')).toBeNull()
    await fireEvent.keyDown(window, { key: 'Escape' })
    expect(onclose).not.toHaveBeenCalled()
  })

  it('calls showModal once per open, not on a rerender', async () => {
    const spy = vi.spyOn(HTMLDialogElement.prototype, 'showModal')
    const { rerender } = await open()
    await rerender({ title: 'Renamed' })
    expect(spy).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('heading').textContent).toBe('Renamed')
  })

  it('names itself from the title, label or labelledby, with a unique title id', async () => {
    const one = await open({ describedby: 'desc' })
    const two = await open({ title: 'Other' })
    const [a, b] = document.querySelectorAll('dialog')
    expect(a.getAttribute('aria-labelledby')).not.toBe(b.getAttribute('aria-labelledby'))
    expect(document.getElementById(a.getAttribute('aria-labelledby')).textContent).toBe('Title')
    expect(a.getAttribute('aria-describedby')).toBe('desc')
    one.unmount()
    two.unmount()

    await open({ title: '', label: 'Palette', placement: 'top' })
    const c = document.querySelector('dialog')
    expect(c.getAttribute('aria-label')).toBe('Palette')
    expect(c.classList.contains('vault-dialog-top')).toBe(true)
    expect(c.querySelector('h2')).toBeNull()
  })

  it('turns a cancel event into one onclose, and keeps the dialog open itself', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    const dialog = document.querySelector('dialog')
    const event = new Event('cancel', { cancelable: true })
    dialog.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(onclose).toHaveBeenCalledTimes(1)
    expect(dialog.open).toBe(true)
  })

  it('treats a native close as a request to close', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    document.querySelector('dialog').dispatchEvent(new Event('close'))
    expect(onclose).toHaveBeenCalledTimes(1)
  })

  it('closes from the header button without submitting a surrounding form', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    const close = screen.getByRole('button', { name: 'Close' })
    expect(close.getAttribute('type')).toBe('button')
    await fireEvent.click(close)
    expect(onclose).toHaveBeenCalledTimes(1)
  })

  it('focuses [data-autofocus], else the first control, else the dialog', async () => {
    const marked = await open({}, '<button>A</button><button id="safe" data-autofocus>B</button>')
    expect(document.activeElement.id).toBe('safe')
    marked.unmount()

    const first = await open()
    expect(document.activeElement.getAttribute('aria-label')).toBe('Close')
    first.unmount()

    await open({ title: '', label: 'Empty' }, '<p>Nothing to press</p>')
    expect(document.activeElement).toBe(document.querySelector('dialog'))
  })

  it('wraps Tab and Shift+Tab, skipping disabled and tabindex=-1 controls', async () => {
    await open({ title: '', label: 'L' }, '<button id="a">A</button><button tabindex="-1">Skip</button><button id="b">B</button><button disabled>C</button>')
    const a = document.getElementById('a')
    const b = document.getElementById('b')
    expect(pressTab(b)).toBe(false)
    expect(document.activeElement).toBe(a)
    expect(pressTab(a, true)).toBe(false)
    expect(document.activeElement).toBe(b)
    expect(pressTab(a)).toBe(true) // mid-dialog: the browser moves focus
  })

  it('wraps Shift+Tab from the dialog itself, and swallows Tab with nothing to focus', async () => {
    const view = await open({ title: '', label: 'L' })
    const dialog = document.querySelector('dialog')
    expect(pressTab(dialog, true)).toBe(false)
    expect(document.activeElement.id).toBe('b')
    view.unmount()

    await open({ title: '', label: 'L' }, '<p>None</p>')
    expect(pressTab(document.querySelector('dialog'))).toBe(false)
  })

  it('treats a radio group as one Tab stop', async () => {
    await open({ title: '', label: 'L' }, '<button id="a">A</button><input type="radio" name="g" id="on" checked><input type="radio" name="g" id="off">')
    expect(pressTab(document.getElementById('on'))).toBe(false)
    expect(document.activeElement.id).toBe('a')
  })

  it('leaves Tab inside a nested dialog to that dialog', async () => {
    await open({ title: '', label: 'Outer' }, '<button id="o">O</button><dialog open id="inner"><button id="i">I</button></dialog>')
    const inner = document.getElementById('i')
    expect(pressTab(inner)).toBe(true)
  })

  it('returns focus to the opener on close', async () => {
    const { unmount } = await open()
    expect(document.activeElement).not.toBe(opener)
    unmount()
    await flush()
    expect(document.activeElement).toBe(opener)
  })

  it('remembers the pressed button where clicking does not focus it', async () => {
    opener.blur()
    await fireEvent.pointerDown(opener)
    const { unmount } = await open()
    unmount()
    await flush()
    expect(document.activeElement).toBe(opener)
  })

  it('falls back to main content when the opener is gone', async () => {
    const { unmount } = await open()
    opener.remove()
    unmount()
    await flush()
    expect(document.activeElement).toBe(main)
  })

  it('falls back to main content when the opener is disabled', async () => {
    const { unmount } = await open()
    opener.disabled = true
    unmount()
    await flush()
    expect(document.activeElement).toBe(main)
  })

  it('moves focus to main content if the restored opener is removed later', async () => {
    const { unmount } = await open()
    unmount()
    await flush()
    expect(document.activeElement).toBe(opener)
    opener.remove()
    await flush()
    expect(document.activeElement).toBe(main)
  })

  it('stops watching the opener once focus has moved on', async () => {
    const other = document.createElement('button')
    main.append(other)
    const { unmount } = await open()
    unmount()
    await flush()
    other.focus()
    main.append(document.createElement('span'))
    await flush()
    opener.remove()
    await flush()
    expect(document.activeElement).toBe(other)
  })

  it('falls back to the remaining dialog when the opener is gone', async () => {
    const parent = await open({ title: '', label: 'Parent' }, '<button id="parent-btn" data-autofocus>P</button>')
    const inner = document.getElementById('parent-btn')
    inner.focus()
    const child = await open({ title: '', label: 'Child' })
    inner.remove()
    parent.rerender({})
    child.unmount()
    await flush()
    expect(document.activeElement).toBe(document.querySelector('dialog'))
  })

  it('does not steal focus from a dialog opened as this one closes', async () => {
    const first = await open({ title: '', label: 'First' })
    first.unmount()
    await open({ title: '', label: 'Second' }, '<button id="second" data-autofocus>S</button>')
    await flush()
    expect(document.activeElement.id).toBe('second')
  })

  it('refocuses the dialog when re-rendered content removes the focused element', async () => {
    await open({ title: '', label: 'L' })
    document.getElementById('a').focus()
    document.getElementById('a').remove()
    await flush()
    expect(document.activeElement).toBe(document.querySelector('dialog'))
  })
})

describe('floatAboveDialogs', () => {
  it('stays put without a dialog, moves into the topmost one, and returns', async () => {
    const home = document.createElement('section')
    const toast = document.createElement('div')
    home.append(toast)
    document.body.append(home)
    const action = floatAboveDialogs(toast)
    expect(toast.parentNode).toBe(home)

    const outer = await open({ title: '', label: 'Outer' })
    const outerDialog = document.querySelector('dialog')
    expect(toast.parentNode).toBe(outerDialog)

    const inner = await open({ title: '', label: 'Inner' })
    const innerDialog = document.querySelectorAll('dialog')[1]
    expect(toast.parentNode).toBe(innerDialog)

    inner.unmount()
    expect(toast.parentNode).toBe(outerDialog)
    outer.unmount()
    expect(toast.parentNode).toBe(home)

    action.destroy()
    expect(toast.isConnected).toBe(false)
    expect(home.childNodes.length).toBe(0)
    home.remove()
    await flush()
  })

  it('moves a node that appears while a dialog is already open', async () => {
    await open()
    const home = document.createElement('section')
    const toast = document.createElement('div')
    home.append(toast)
    document.body.append(home)
    const action = floatAboveDialogs(toast)
    expect(toast.parentNode).toBe(document.querySelector('dialog'))
    action.destroy()
    home.remove()
  })
})

describe('Modal focus edge cases', () => {
  it.each([
    ['hidden', '<div hidden><button id="a">A</button></div><button id="b">B</button>'],
    ['inert', '<div inert><button id="a">A</button></div><button id="b">B</button>'],
  ])('never focuses a control inside a %s subtree', async (_name, markup) => {
    await open({ title: '', label: 'No header' }, markup)
    expect(document.activeElement.id).toBe('b')
  })

  it('skips a control the browser reports as not visible', async () => {
    Element.prototype.checkVisibility = function checkVisibility() { return this.id !== 'a' }
    try {
      await open({ title: '', label: 'No header' })
      expect(document.activeElement.id).toBe('b')
    } finally {
      delete Element.prototype.checkVisibility
    }
  })

  it('keeps watching the opener through unrelated page changes while it holds focus', async () => {
    const { unmount } = await open()
    unmount()
    await flush()
    expect(document.activeElement).toBe(opener)

    main.append(document.createElement('div'))
    await flush()
    expect(document.activeElement).toBe(opener)

    opener.remove()
    await flush()
    expect(document.activeElement).toBe(main)
  })

  it('leaves focus alone when the opener vanishes after focus went somewhere else', async () => {
    const other = document.createElement('button')
    main.append(other)
    const { unmount } = await open()
    unmount()
    await flush()
    other.focus()
    opener.remove()
    await flush()
    expect(document.activeElement).toBe(other)
  })

  it('leaves focus where a route change moved it as the dialog closed', async () => {
    const heading = document.createElement('h1')
    heading.tabIndex = -1
    main.append(heading)
    const { unmount } = await open()
    const dialog = document.querySelector('dialog')
    // Runs after unmount has read the focus, and before focus is restored.
    const observer = new MutationObserver(() => heading.focus())
    observer.observe(dialog, { attributes: true })
    unmount()
    await flush()
    observer.disconnect()
    expect(document.activeElement).toBe(heading)
  })

  it('does not reopen a dialog that is already open, which a browser rejects', () => {
    const dialog = document.createElement('dialog')
    dialog.setAttribute('open', '')
    main.append(dialog)
    const showModal = vi.spyOn(dialog, 'showModal')
    const action = modalDialog(dialog, () => {})
    expect(showModal).not.toHaveBeenCalled()
    action.destroy()
    expect(dialog.open).toBe(false)
  })

  it('does not close again on unmount after a native close', async () => {
    const { unmount } = await open()
    const dialog = document.querySelector('dialog')
    dialog.close()
    const close = vi.spyOn(dialog, 'close')
    unmount()
    await flush()
    expect(close).not.toHaveBeenCalled()
  })

  it('leaves a floated node in place when another joins it', async () => {
    await open()
    const dialog = document.querySelector('dialog')
    const home = document.createElement('section')
    const first = document.createElement('div')
    const second = document.createElement('div')
    home.append(first, second)
    document.body.append(home)
    const firstAction = floatAboveDialogs(first)
    const append = vi.spyOn(dialog, 'append')
    const secondAction = floatAboveDialogs(second)
    expect(append.mock.calls).toEqual([[second]])
    expect(first.parentNode).toBe(dialog)
    firstAction.destroy()
    secondAction.destroy()
    home.remove()
  })
})

describe('Modal backdrop (#530)', () => {
  it('does not dismiss a normal dialog on a backdrop click, so work is not lost', async () => {
    const onclose = vi.fn()
    await open({ onclose })
    await fireEvent.click(document.querySelector('dialog'))
    expect(onclose).not.toHaveBeenCalled()
  })

  it('dismisses a sheet on a backdrop click, only while it is a sheet, and only from outside its panel', async () => {
    const onclose = vi.fn()
    const { rerender } = await open({ placement: 'sheet', onclose })
    const dialog = document.querySelector('dialog')
    await fireEvent.click(dialog.querySelector('#a'))
    expect(onclose).not.toHaveBeenCalled()
    await fireEvent.click(dialog)
    expect(onclose).toHaveBeenCalledOnce()

    await rerender({ placement: 'center' })
    await fireEvent.click(dialog)
    expect(onclose).toHaveBeenCalledOnce()
  })
})
