import { afterEach, describe, expect, it, vi } from 'vitest'

import { getVisibleArea, onVisibleAreaChange, parseViewportMessage, revealFrameTop, scrollHostTo, startEmbeddedPresentation } from './embed.js'

const ORIGIN = 'http://tower.test'

function fakeWindow({ app = { offsetHeight: 900, scrollHeight: 900 } } = {}) {
  const listeners = new Set()
  const observers = []
  const classes = new Set()
  const props = new Map()
  const parent = { postMessage: vi.fn() }
  const win = {
    location: { origin: ORIGIN },
    parent,
    document: {
      documentElement: {
        classList: { add: (c) => classes.add(c) },
        style: { setProperty: (k, v) => props.set(k, v) },
      },
      getElementById: (id) => (id === 'app' ? app : null),
    },
    addEventListener: vi.fn((_, fn) => listeners.add(fn)),
    removeEventListener: vi.fn((_, fn) => listeners.delete(fn)),
    ResizeObserver: class {
      constructor(cb) {
        this.cb = cb
        this.observe = vi.fn()
        this.disconnect = vi.fn()
        observers.push(this)
      }
    },
  }
  const send = (data, event = {}) => listeners.forEach((fn) => fn({ origin: ORIGIN, source: parent, data, ...event }))
  return { win, parent, classes, props, observers, listeners, send, app }
}

const viewport = (extra = {}) => ({ source: 'vault-embed-host', type: 'viewport', top: 10, height: 400, ...extra })

afterEach(() => vi.unstubAllGlobals())

describe('parseViewportMessage', () => {
  const { win, parent } = fakeWindow()
  const event = (data, extra = {}) => ({ origin: ORIGIN, source: parent, data, ...extra })

  it('accepts a well-formed message from the parent on this origin', () => {
    expect(parseViewportMessage(event(viewport()), win)).toEqual({ top: 10, height: 400 })
  })

  it('rejects another origin or another window', () => {
    expect(parseViewportMessage(event(viewport(), { origin: 'http://evil.test' }), win)).toBeNull()
    expect(parseViewportMessage(event(viewport(), { source: {} }), win)).toBeNull()
  })

  it.each([
    ['null', null],
    ['a string', 'viewport'],
    ['another source', viewport({ source: 'other' })],
    ['another type', viewport({ type: 'size' })],
    ['a string length', viewport({ top: '10' })],
    ['a negative length', viewport({ height: -1 })],
    ['NaN', viewport({ top: NaN })],
    ['Infinity', viewport({ height: Infinity })],
    ['an absurd length', viewport({ height: 1e9 })],
    ['missing lengths', { source: 'vault-embed-host', type: 'viewport' }],
  ])('rejects %s', (_, data) => {
    expect(parseViewportMessage(event(data), win)).toBeNull()
  })
})

describe('startEmbeddedPresentation', () => {
  it('flags the root and reports the content height to this origin only', () => {
    const { win, parent, classes } = fakeWindow()
    startEmbeddedPresentation(win)
    expect(classes.has('vault-embedded')).toBe(true)
    expect(parent.postMessage).toHaveBeenCalledWith({ source: 'vault-embed', type: 'size', height: 900 }, ORIGIN)
  })

  it('counts content that spills out of the box and re-reports only on change', () => {
    const { win, parent, observers, app } = fakeWindow()
    startEmbeddedPresentation(win)
    parent.postMessage.mockClear()
    observers[0].cb()
    expect(parent.postMessage).not.toHaveBeenCalled()
    app.scrollHeight = 1200.4
    observers[0].cb()
    expect(parent.postMessage).toHaveBeenCalledWith({ source: 'vault-embed', type: 'size', height: 1201 }, ORIGIN)
  })

  it('publishes a valid viewport and ignores an invalid one', () => {
    const { win, props, send } = fakeWindow()
    startEmbeddedPresentation(win)
    send(viewport({ origin: 'x', top: 'bad' }))
    expect(props.size).toBe(0)
    send(viewport())
    expect(props.get('--vault-visible-top')).toBe('10px')
    expect(props.get('--vault-visible-height')).toBe('400px')
  })

  it('re-reports once on first hearing from a host that attached late', () => {
    const { win, parent, send } = fakeWindow()
    startEmbeddedPresentation(win)
    parent.postMessage.mockClear()
    send(viewport())
    send(viewport({ top: 20 }))
    expect(parent.postMessage).toHaveBeenCalledTimes(1)
  })

  it('survives a missing #app', () => {
    const { win, parent, send } = fakeWindow({ app: null })
    win.document.getElementById = () => null
    startEmbeddedPresentation(win)
    send(viewport())
    expect(parent.postMessage).not.toHaveBeenCalled()
  })

  it('stops listening and observing', () => {
    const { win, observers, listeners } = fakeWindow()
    const stop = startEmbeddedPresentation(win)
    stop()
    expect(listeners.size).toBe(0)
    expect(observers[0].disconnect).toHaveBeenCalled()
  })

  it('drops the earlier channel and its scroll listeners when it restarts', () => {
    const first = fakeWindow()
    const stopFirst = startEmbeddedPresentation(first.win)
    const stale = vi.fn()
    onVisibleAreaChange(stale)
    const second = fakeWindow()
    const stopSecond = startEmbeddedPresentation(second.win)
    expect(first.listeners.size).toBe(0)
    expect(first.observers[0].disconnect).toHaveBeenCalled()
    const fresh = vi.fn()
    onVisibleAreaChange(fresh)
    second.send(viewport())
    expect(stale).not.toHaveBeenCalled()
    expect(fresh).toHaveBeenCalledOnce()

    // The earlier stop, called late, leaves the replacement running: it still
    // reports grown content before asking the host to scroll.
    stopFirst()
    second.parent.postMessage.mockClear()
    second.app.offsetHeight = 1500
    scrollHostTo(1200, second.win)
    expect(second.parent.postMessage.mock.calls.map(([m]) => m.type)).toEqual(['size', 'scroll'])
    stopSecond()
  })
})

describe('host scrolling', () => {
  it('asks the host to scroll to a clamped, whole position', () => {
    const { win, parent } = fakeWindow()
    scrollHostTo(-5, win)
    expect(parent.postMessage).toHaveBeenLastCalledWith({ source: 'vault-embed', type: 'scroll', top: 0 }, ORIGIN)
    scrollHostTo(12.6, win)
    expect(parent.postMessage).toHaveBeenLastCalledWith({ source: 'vault-embed', type: 'scroll', top: 13 }, ORIGIN)
  })

  it('reveals the frame top only when the page has scrolled past it', () => {
    const { win, parent, send } = fakeWindow()
    startEmbeddedPresentation(win)
    send(viewport({ top: 0 }))
    parent.postMessage.mockClear()
    revealFrameTop(win)
    expect(parent.postMessage).not.toHaveBeenCalled()
    send(viewport({ top: 50 }))
    revealFrameTop(win)
    expect(parent.postMessage).toHaveBeenCalledWith({ source: 'vault-embed', type: 'scroll', top: 0 }, ORIGIN)
  })
})

describe('visible area (#526)', () => {
  it('defaults to the whole frame until the host reports, then follows it', () => {
    const { win, send } = fakeWindow()
    win.innerHeight = 700
    startEmbeddedPresentation(win)
    expect(getVisibleArea(win)).toEqual({ top: 0, height: 700 })
    send(viewport({ top: 120, height: 450 }))
    expect(getVisibleArea(win)).toEqual({ top: 120, height: 450 })
  })

  it('tells subscribers about each valid report until they unsubscribe', () => {
    const { win, send } = fakeWindow()
    startEmbeddedPresentation(win)
    const fn = vi.fn()
    const off = onVisibleAreaChange(fn)
    send(viewport({ top: 'bad' }))
    expect(fn).not.toHaveBeenCalled()
    send(viewport())
    expect(fn).toHaveBeenCalledOnce()
    off()
    send(viewport({ top: 30 }))
    expect(fn).toHaveBeenCalledOnce()
  })

  it('reports grown content before asking the host to scroll, so the page can reach it', () => {
    const { win, parent, app } = fakeWindow()
    const stop = startEmbeddedPresentation(win)
    parent.postMessage.mockClear()
    app.offsetHeight = 1500
    scrollHostTo(1200, win)
    expect(parent.postMessage.mock.calls.map(([m]) => m.type)).toEqual(['size', 'scroll'])
    stop()
    parent.postMessage.mockClear()
    scrollHostTo(10, win)
    expect(parent.postMessage.mock.calls.map(([m]) => m.type)).toEqual(['scroll'])
  })
})
