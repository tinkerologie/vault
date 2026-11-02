import { afterEach, describe, expect, it, vi } from 'vitest'

import { parseViewportMessage, revealFrameTop, scrollHostTo, startEmbeddedPresentation } from './embed.js'

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
