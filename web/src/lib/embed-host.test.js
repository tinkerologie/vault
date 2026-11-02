import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// plugin/pages/include/embed-host.js is a plain browser script, so it runs here
// against a fake page. tests/embedded.spec.ts runs it for real in an iframe.
const source = readFileSync(new URL('../../../plugin/pages/include/embed-host.js', import.meta.url), 'utf8')

const ORIGIN = 'http://tower.test'

function setup({ frameTop = 100, innerHeight = 800, chrome = {} } = {}) {
  const winListeners = new Map()
  const frameListeners = new Map()
  const docListeners = new Map()
  const child = { postMessage: vi.fn(), location: { reload: vi.fn() } }
  const doc = {
    hidden: false,
    addEventListener: (t, fn) => docListeners.set(t, fn),
    removeEventListener: (t) => docListeners.delete(t),
    querySelector: (sel) => chrome[sel]?.el ?? null,
  }
  const win = {
    location: { origin: ORIGIN },
    innerHeight,
    pageYOffset: 0,
    document: doc,
    addEventListener: (t, fn) => winListeners.set(t, fn),
    removeEventListener: (t) => winListeners.delete(t),
    requestAnimationFrame: (fn) => { win.raf = fn },
    scrollTo: vi.fn(),
    setTimeout: (fn) => fn(),
    getComputedStyle: (el) => ({ position: el.position }),
  }
  doc.defaultView = win
  const frame = {
    ownerDocument: doc,
    contentWindow: child,
    style: {},
    src: '/app?embed=1#/',
    top: frameTop,
    getAttribute: () => '/app?embed=1#/',
    addEventListener: (t, fn) => frameListeners.set(t, fn),
    removeEventListener: (t) => frameListeners.delete(t),
    getBoundingClientRect() {
      const height = parseInt(this.style.height, 10) || 0
      return { top: this.top, bottom: this.top + height, height }
    },
  }
  new Function('window', source)(win)
  const detach = win.VaultEmbedHost.attach(frame)
  const message = (data, extra = {}) => winListeners.get('message')({ origin: ORIGIN, source: child, data, ...extra })
  const size = (height) => message({ source: 'vault-embed', type: 'size', height })
  return { win, doc, frame, child, detach, message, size, winListeners, frameListeners, docListeners }
}

const lastViewport = (child) => child.postMessage.mock.calls.at(-1)[0]

beforeEach(() => vi.useFakeTimers({ now: 1_000_000 }))
afterEach(() => vi.useRealTimers())

describe('frame height', () => {
  it('starts at the viewport floor, never below 480', () => {
    expect(setup().frame.style.height).toBe('676px')
    expect(setup({ frameTop: 500 }).frame.style.height).toBe('480px')
  })

  it('follows content above the floor and shrinks back to it', () => {
    const { frame, size } = setup()
    size(2000.2)
    expect(frame.style.height).toBe('2001px')
    size(100)
    expect(frame.style.height).toBe('676px')
  })

  it('recomputes the floor on parent resize', () => {
    const { win, frame, winListeners } = setup()
    win.innerHeight = 1000
    winListeners.get('resize')()
    expect(frame.style.height).toBe('876px')
  })
})

describe('message validation', () => {
  it('ignores another origin or another window', () => {
    const { frame, message, win } = setup()
    const data = { source: 'vault-embed', type: 'size', height: 3000 }
    message(data, { origin: 'http://evil.test' })
    message(data, { source: {} })
    message({ source: 'vault-embed', type: 'scroll', top: 10 }, { origin: 'http://evil.test' })
    expect(frame.style.height).toBe('676px')
    expect(win.scrollTo).not.toHaveBeenCalled()
  })

  it.each([
    ['null', null],
    ['a string', 'size'],
    ['another source', { source: 'x', type: 'size', height: 3000 }],
    ['an unknown type', { source: 'vault-embed', type: 'other', height: 3000 }],
    ['a string height', { source: 'vault-embed', type: 'size', height: '3000' }],
    ['a negative height', { source: 'vault-embed', type: 'size', height: -1 }],
    ['NaN', { source: 'vault-embed', type: 'size', height: NaN }],
    ['Infinity', { source: 'vault-embed', type: 'size', height: Infinity }],
    ['an absurd height', { source: 'vault-embed', type: 'size', height: 1e9 }],
    ['a bad scroll', { source: 'vault-embed', type: 'scroll', top: 'x' }],
  ])('ignores %s', (_, data) => {
    const { frame, message, win } = setup()
    message(data)
    expect(frame.style.height).toBe('676px')
    expect(win.scrollTo).not.toHaveBeenCalled()
  })
})

describe('visible area', () => {
  it('reports the part of the frame on screen, to this origin only', () => {
    const { child, size } = setup()
    size(2000)
    expect(child.postMessage).toHaveBeenLastCalledWith(
      { source: 'vault-embed-host', type: 'viewport', top: 0, height: 628 },
      ORIGIN,
    )
  })

  it('throttles scroll to an animation frame and tracks the scrolled position', () => {
    const { win, frame, child, size, winListeners } = setup()
    size(2000)
    child.postMessage.mockClear()
    frame.top = -300
    winListeners.get('scroll')()
    winListeners.get('scroll')()
    expect(child.postMessage).not.toHaveBeenCalled()
    win.raf()
    expect(child.postMessage).toHaveBeenCalledTimes(1)
    expect(lastViewport(child)).toMatchObject({ top: 300, height: 728 })
  })

  it('sends nothing when the area has not changed, until a new document loads', () => {
    const { child, size, frameListeners } = setup()
    size(2000)
    child.postMessage.mockClear()
    size(2000)
    expect(child.postMessage).not.toHaveBeenCalled()
    frameListeners.get('load')()
    expect(child.postMessage).toHaveBeenCalledTimes(1)
  })

  it('subtracts pinned Unraid chrome, but not a full-height side menu or static chrome', () => {
    const chrome = {
      '#menu': { el: { position: 'fixed', getBoundingClientRect: () => ({ top: 0, bottom: 60, height: 60 }) } },
      '#footer': { el: { position: 'fixed', getBoundingClientRect: () => ({ top: 700, bottom: 800, height: 100 }) } },
      '#header': { el: { position: 'static', getBoundingClientRect: () => ({ top: 0, bottom: 90, height: 90 }) } },
    }
    const { frame, child, size, win } = setup({ chrome })
    size(2000)
    frame.top = -300
    win.scrollTo = vi.fn()
    size(2000.5)
    expect(lastViewport(child)).toMatchObject({ top: 360, height: 640 })

    const sidebar = {
      '#menu': { el: { position: 'fixed', getBoundingClientRect: () => ({ top: 0, bottom: 800, height: 800 }) } },
    }
    const other = setup({ chrome: sidebar })
    other.size(2000)
    expect(lastViewport(other.child)).toMatchObject({ top: 0, height: 628 })
  })

  it('stops when the frame has no window', () => {
    const { frame, child, frameListeners } = setup()
    child.postMessage.mockClear()
    frame.contentWindow = null
    frameListeners.get('load')()
    expect(child.postMessage).not.toHaveBeenCalled()
  })
})

describe('scroll requests', () => {
  it('scrolls the page to a position in the frame, clear of pinned chrome', () => {
    const chrome = { '#menu': { el: { position: 'fixed', getBoundingClientRect: () => ({ top: 0, bottom: 60, height: 60 }) } } }
    const { win, message } = setup({ chrome })
    win.pageYOffset = 50
    message({ source: 'vault-embed', type: 'scroll', top: 200 })
    expect(win.scrollTo).toHaveBeenCalledWith(0, 290)
  })

  it('never scrolls above the page top', () => {
    const { win, message } = setup({ frameTop: 0 })
    message({ source: 'vault-embed', type: 'scroll', top: 0 })
    expect(win.scrollTo).toHaveBeenCalledWith(0, 0)
  })
})

describe('reload after a long absence', () => {
  const hide = ({ doc, docListeners }) => {
    doc.hidden = true
    docListeners.get('visibilitychange')()
  }
  const show = ({ doc, docListeners }) => {
    doc.hidden = false
    docListeners.get('visibilitychange')()
  }

  it('leaves a short absence alone', () => {
    const page = setup()
    hide(page)
    vi.setSystemTime(1_000_000 + 60_000)
    show(page)
    expect(page.child.location.reload).not.toHaveBeenCalled()
  })

  it('reloads the document after more than ten minutes, once', () => {
    const page = setup()
    hide(page)
    vi.setSystemTime(1_000_000 + 11 * 60_000)
    show(page)
    expect(page.child.location.reload).toHaveBeenCalledTimes(1)
    show(page)
    expect(page.child.location.reload).toHaveBeenCalledTimes(1)
  })

  it('falls back to reassigning src, keeping embed=1, when reload throws', () => {
    const page = setup()
    page.child.location.reload.mockImplementation(() => { throw new Error('SecurityError') })
    hide(page)
    vi.setSystemTime(1_000_000 + 11 * 60_000)
    show(page)
    expect(page.frame.src).toBe('/app?embed=1#/')
  })
})

describe('detach', () => {
  it('removes every listener', () => {
    const { detach, winListeners, frameListeners, docListeners } = setup()
    detach()
    expect(winListeners.size + frameListeners.size + docListeners.size).toBe(0)
  })
})
