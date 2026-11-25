import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createConnectionBanner, CONNECTION_BANNER_DELAY_MS, isConnectionLost } from './connection-banner.js'

// The connection-lost banner (#686): a short drop shows nothing, and a long
// one shows the banner once and keeps it up through every retry.

describe('isConnectionLost', () => {
  it.each([
    ['disconnected', true],
    ['reconnecting', true],
    ['connecting', false],
    ['connected', false],
    ['polling', false],
  ])('%s -> %s', (status, lost) => {
    expect(isConnectionLost(status)).toBe(lost)
  })
})

describe('createConnectionBanner', () => {
  let shown
  let banner

  beforeEach(() => {
    vi.useFakeTimers()
    shown = []
    banner = createConnectionBanner((value) => shown.push(value))
  })

  afterEach(() => {
    banner.dispose()
    vi.useRealTimers()
  })

  it('waits three seconds by default', () => {
    expect(CONNECTION_BANNER_DELAY_MS).toBe(3000)
  })

  it('shows nothing for a drop shorter than the delay', () => {
    banner.update(true)
    vi.advanceTimersByTime(CONNECTION_BANNER_DELAY_MS - 1)
    banner.update(false)
    vi.advanceTimersByTime(60_000)
    expect(shown).toEqual([])
  })

  it('shows once after the delay and stays through repeated lost updates', () => {
    banner.update(true)
    vi.advanceTimersByTime(CONNECTION_BANNER_DELAY_MS)
    for (let i = 0; i < 5; i++) {
      banner.update(true)
      vi.advanceTimersByTime(10_000)
    }
    expect(shown).toEqual([true])
  })

  it('does not restart the delay on each lost update', () => {
    banner.update(true)
    vi.advanceTimersByTime(2000)
    banner.update(true)
    vi.advanceTimersByTime(1000)
    expect(shown).toEqual([true])
  })

  it('hides as soon as the connection is back, and waits again on the next drop', () => {
    banner.update(true)
    vi.advanceTimersByTime(CONNECTION_BANNER_DELAY_MS)
    banner.update(false)
    expect(shown).toEqual([true, false])
    banner.update(true)
    vi.advanceTimersByTime(CONNECTION_BANNER_DELAY_MS - 1)
    expect(shown).toEqual([true, false])
    vi.advanceTimersByTime(1)
    expect(shown).toEqual([true, false, true])
  })

  it('reports nothing after dispose', () => {
    banner.update(true)
    banner.dispose()
    vi.advanceTimersByTime(60_000)
    expect(shown).toEqual([])
  })

  it('takes a custom delay', () => {
    const quick = []
    const b = createConnectionBanner((v) => quick.push(v), 10)
    b.update(true)
    vi.advanceTimersByTime(10)
    expect(quick).toEqual([true])
    b.dispose()
  })
})
