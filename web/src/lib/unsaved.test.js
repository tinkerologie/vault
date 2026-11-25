import { describe, it, expect, vi } from 'vitest'
import { snapshot, isDirty, createDialogHistory, guardUnload, guardLinks } from './unsaved.js'

// A history stack with a cursor, firing popstate asynchronously like a browser.
function fakeWindow() {
  const listeners = new Map()
  const entries = [{ state: null }]
  let index = 0
  const win = {
    addEventListener: (type, fn) => {
      if (!listeners.has(type)) listeners.set(type, new Set())
      listeners.get(type).add(fn)
    },
    removeEventListener: (type, fn) => listeners.get(type)?.delete(fn),
    dispatch(type, event = {}) {
      for (const fn of [...(listeners.get(type) || [])]) fn(event)
    },
    listenerCount: (type) => listeners.get(type)?.size ?? 0,
    history: {
      get state() {
        return entries[index].state
      },
      get length() {
        return entries.length
      },
      pushState(state) {
        entries.splice(index + 1)
        entries.push({ state })
        index++
      },
      back() {
        if (index === 0) return
        index--
        queueMicrotask(() => win.dispatch('popstate'))
      },
    },
    get index() {
      return index
    },
  }
  return win
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('snapshot / isDirty', () => {
  it('compares against the opening copy', () => {
    const base = snapshot({ name: 'a', items: [1] })
    expect(isDirty(base, { name: 'a', items: [1] })).toBe(false)
    expect(isDirty(base, { name: 'b', items: [1] })).toBe(true)
    expect(isDirty(null, { name: 'b' })).toBe(false)
    expect(snapshot(undefined)).toBe('null')
  })
})

describe('createDialogHistory', () => {
  it('turns Back into a dialog close and leaves the page entry alone', async () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    const onBack = vi.fn()
    dh.open(onBack)
    expect(win.index).toBe(1)
    expect(dh.active).toBe(true)
    win.history.back()
    await flush()
    expect(onBack).toHaveBeenCalledTimes(1)
    expect(dh.active).toBe(false)
    expect(win.index).toBe(0)
    expect(win.listenerCount('popstate')).toBe(0)
  })

  it('removes its entry when the dialog closes by itself, without calling back', async () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    const onBack = vi.fn()
    dh.open(onBack)
    dh.close()
    await flush()
    expect(onBack).not.toHaveBeenCalled()
    expect(win.index).toBe(0)
    expect(win.listenerCount('popstate')).toBe(0)
  })

  it('re-arms when the dialog must stay open after Back', async () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    const second = vi.fn()
    dh.open(() => dh.keepOpen(second))
    win.history.back()
    await flush()
    expect(dh.active).toBe(true)
    expect(win.index).toBe(1)
    win.history.back()
    await flush()
    expect(second).toHaveBeenCalledTimes(1)
  })

  it('opening twice pushes once', () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    dh.open(() => {})
    dh.open(() => {})
    expect(win.history.length).toBe(2)
  })

  it('does not pop someone else entry on close', async () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    dh.open(() => {})
    win.history.pushState({ other: true })
    dh.close()
    await flush()
    expect(win.index).toBe(2)
    expect(win.listenerCount('popstate')).toBe(0)
  })

  it('ignores stray pops and stops listening on destroy', async () => {
    const win = fakeWindow()
    const dh = createDialogHistory(win)
    dh.close() // nothing open: no-op
    dh.open(() => {})
    dh.destroy()
    expect(dh.active).toBe(false)
    expect(win.listenerCount('popstate')).toBe(0)
  })
})

describe('guardLinks', () => {
  function fakeDoc() {
    const listeners = new Set()
    return {
      addEventListener: (_t, fn) => listeners.add(fn),
      removeEventListener: (_t, fn) => listeners.delete(fn),
      click(href, extra = {}) {
        const a = { getAttribute: () => href }
        const event = { button: 0, defaultPrevented: false, target: { closest: () => (href ? a : null) }, preventDefault() { this.defaultPrevented = true }, ...extra }
        for (const fn of listeners) fn(event)
        return event
      },
      count: () => listeners.size,
    }
  }

  it('holds a link to another page while dirty', () => {
    const doc = fakeDoc()
    let dirty = true
    const blocked = vi.fn()
    const stop = guardLinks('/settings', () => dirty, blocked, doc)
    expect(doc.click('#/jobs').defaultPrevented).toBe(true)
    expect(blocked).toHaveBeenCalledWith('#/jobs')
    dirty = false
    expect(doc.click('#/jobs').defaultPrevented).toBe(false)
    stop()
    expect(doc.count()).toBe(0)
  })

  it('lets same-page links, modified clicks and non-links through', () => {
    const doc = fakeDoc()
    const blocked = vi.fn()
    guardLinks('/settings', () => true, blocked, doc)
    expect(doc.click('#/settings/security').defaultPrevented).toBe(false)
    expect(doc.click('#/settings').defaultPrevented).toBe(false)
    expect(doc.click('#/jobs', { metaKey: true }).defaultPrevented).toBe(false)
    expect(doc.click(null).defaultPrevented).toBe(false)
    expect(blocked).not.toHaveBeenCalled()
  })
})

describe('guardUnload', () => {
  it('asks before unload only while dirty', () => {
    const win = fakeWindow()
    let dirty = false
    const stop = guardUnload(() => dirty, win)
    const clean = { preventDefault: vi.fn() }
    win.dispatch('beforeunload', clean)
    expect(clean.preventDefault).not.toHaveBeenCalled()
    dirty = true
    const event = { preventDefault: vi.fn(), returnValue: undefined }
    win.dispatch('beforeunload', event)
    expect(event.preventDefault).toHaveBeenCalled()
    expect(event.returnValue).toBe('')
    stop()
    expect(win.listenerCount('beforeunload')).toBe(0)
  })
})
