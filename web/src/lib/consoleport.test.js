import { describe, expect, it, vi } from 'vitest'

import { elementPort, embeddedGeometry, embeddedPort, embeddedScrollTarget } from './consoleport.js'

// A 2,000 px console 300 px down the frame, seen through a 500 px window.
const at = (visibleTop, extra = {}) => ({ consoleTop: 300, consoleHeight: 2000, visibleTop, visibleHeight: 500, ...extra })

describe('embeddedGeometry', () => {
  it('reads as unscrolled while the console starts on screen', () => {
    expect(embeddedGeometry(at(100))).toEqual({ scrollTop: 0, scrollHeight: 2000, clientHeight: 300, viewTop: 300 })
  })

  it('counts how far the console top has passed the top of the screen', () => {
    expect(embeddedGeometry(at(800))).toEqual({ scrollTop: 500, scrollHeight: 2000, clientHeight: 500, viewTop: 800 })
  })

  it('measures the distance left to the console end exactly, wherever it starts', () => {
    for (const visibleTop of [0, 100, 300, 800, 1800]) {
      const g = embeddedGeometry(at(visibleTop))
      expect(g.scrollHeight - g.scrollTop - g.clientHeight).toBe(300 + 2000 - (visibleTop + 500))
    }
  })

  it('has no visible height while the console is still below the screen', () => {
    expect(embeddedGeometry(at(0, { consoleTop: 900 })).clientHeight).toBe(0)
  })
})

describe('embeddedScrollTarget', () => {
  it('puts the requested console offset at the top of the screen', () => {
    expect(embeddedScrollTarget(at(800), 100)).toBe(400)
  })

  it('stops with the console end at the bottom of the screen', () => {
    expect(embeddedScrollTarget(at(800), 99999)).toBe(300 + 2000 - 500)
    expect(embeddedScrollTarget(at(800), -50)).toBe(300)
  })

  it('does not move for a write to where it already is', () => {
    expect(embeddedScrollTarget(at(800), 500.4)).toBeNull()
  })

  it('never scrolls the page just to raise an on-screen console top', () => {
    expect(embeddedScrollTarget(at(100), 0)).toBeNull()
  })

  it('keeps a console shorter than the screen where it is', () => {
    expect(embeddedScrollTarget(at(100, { consoleHeight: 200 }), 150)).toBeNull()
  })

  it('scrolls a short console that starts low just far enough to show its end', () => {
    // Only 128 of its 200 px are on screen; its end is 72 px below.
    const short = { consoleTop: 300, consoleHeight: 200, visibleTop: 0, visibleHeight: 428 }
    expect(embeddedScrollTarget(short, 200)).toBe(72)
    // The console's top stays on screen, so it reads as unscrolled afterwards.
    expect(embeddedGeometry({ ...short, visibleTop: 72 }).scrollTop).toBe(0)
    expect(embeddedScrollTarget({ ...short, visibleTop: 72 }, 200)).toBeNull()
    // A write of 0 asks only for the top, which is already on screen.
    expect(embeddedScrollTarget(short, 0)).toBeNull()
  })
})

function fakeElement(rect = { top: 300, height: 2000 }) {
  return {
    scrollTop: 0,
    scrollHeight: 2000,
    clientHeight: 500,
    getBoundingClientRect: () => rect,
    scrollTo: vi.fn(),
    addEventListener: vi.fn(),
  }
}

describe('elementPort', () => {
  it('passes the scroll box through', () => {
    const el = fakeElement()
    const port = elementPort(el, {})
    port.scrollTop = 42
    expect(el.scrollTop).toBe(42)
    expect([port.scrollTop, port.scrollHeight, port.clientHeight, port.viewTop()]).toEqual([42, 2000, 500, 300])
  })

  it('smooth-scrolls, and reports when the scroll ends', () => {
    const el = fakeElement()
    const settled = vi.fn()
    elementPort(el, { onscrollend: null }).smoothScrollTo(0, settled)
    expect(el.scrollTo).toHaveBeenCalledWith({ top: 0, behavior: 'smooth' })
    expect(el.addEventListener).toHaveBeenCalledWith('scrollend', settled, { once: true })
  })

  it('falls back to a timer without scrollend', () => {
    const el = fakeElement()
    const win = { setTimeout: vi.fn() }
    const settled = vi.fn()
    elementPort(el, win).smoothScrollTo(10, settled)
    expect(win.setTimeout).toHaveBeenCalledWith(settled, 700)
    elementPort(el, win).smoothScrollTo(10)
    expect(win.setTimeout).toHaveBeenCalledTimes(1)
  })
})

describe('embeddedPort', () => {
  const setup = (visibleTop, scrollY = 0) => {
    const host = { visibleArea: () => ({ top: visibleTop, height: 500 }), scrollTo: vi.fn() }
    const port = embeddedPort(fakeElement({ top: 300 - scrollY, height: 2000 }), host, { scrollY })
    return { host, port }
  }

  it('reads the console through the visible part of the frame', () => {
    const { port } = setup(800)
    expect([port.scrollTop, port.scrollHeight, port.clientHeight, port.viewTop()]).toEqual([500, 2000, 500, 800])
  })

  it('works in document coordinates if the frame itself has scrolled', () => {
    const { port } = setup(800, 40)
    expect([port.scrollTop, port.viewTop()]).toEqual([500, 760])
  })

  it('asks the host to scroll the page for a write that moves', () => {
    const { host, port } = setup(800)
    port.scrollTop = 0
    expect(host.scrollTo).toHaveBeenCalledWith(300)
    host.scrollTo.mockClear()
    port.scrollTop = 500
    expect(host.scrollTo).not.toHaveBeenCalled()
  })

  it('treats a smooth scroll as settled at once', () => {
    const { host, port } = setup(800)
    const settled = vi.fn()
    port.smoothScrollTo(port.scrollHeight, settled)
    expect(host.scrollTo).toHaveBeenCalledWith(1800)
    expect(settled).toHaveBeenCalledOnce()
    port.smoothScrollTo(0)
    expect(host.scrollTo).toHaveBeenLastCalledWith(300)
  })
})
