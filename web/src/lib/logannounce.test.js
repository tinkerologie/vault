import { describe, expect, it, vi } from 'vitest'
import { arrivalText, createArrivalAnnouncer } from './logannounce.js'

function setup(interval = 10000) {
  let t = 0
  const timers = []
  const announce = vi.fn()
  const a = createArrivalAnnouncer({
    interval,
    announce,
    now: () => t,
    schedule: (fn, ms) => { const h = { fn, at: t + ms }; timers.push(h); return h },
    cancel: (h) => { const i = timers.indexOf(h); if (i >= 0) timers.splice(i, 1) },
  })
  const advance = (ms) => {
    t += ms
    for (const h of [...timers]) if (h.at <= t) { timers.splice(timers.indexOf(h), 1); h.fn() }
  }
  return { a, announce, advance, timers }
}

describe('arrivalText', () => {
  it('counts entries', () => {
    expect(arrivalText(1)).toBe('1 new log entry')
    expect(arrivalText(12)).toBe('12 new log entries')
  })
})

describe('createArrivalAnnouncer', () => {
  it('announces the first arrival straight away', () => {
    const { a, announce } = setup()
    a.add(2)
    expect(announce).toHaveBeenCalledWith('2 new log entries')
  })

  it('batches arrivals within the interval into one later announcement', () => {
    const { a, announce, advance } = setup()
    a.add(1)
    advance(1000)
    a.add(3)
    advance(1000)
    a.add(2)
    expect(announce).toHaveBeenCalledTimes(1)
    advance(7999)
    expect(announce).toHaveBeenCalledTimes(1)
    advance(1)
    expect(announce).toHaveBeenLastCalledWith('5 new log entries')
  })

  it('never announces more often than the interval', () => {
    const { a, announce, advance } = setup()
    const times = []
    announce.mockImplementation(() => times.push(Date.now()))
    for (let i = 0; i < 100; i++) { a.add(1); advance(500) }
    expect(announce.mock.calls.length).toBeLessThanOrEqual(6)
  })

  it('ignores zero arrivals', () => {
    const { a, announce } = setup()
    a.add(0)
    expect(announce).not.toHaveBeenCalled()
  })

  it('drops a pending announcement when disposed', () => {
    const { a, announce, advance, timers } = setup()
    a.add(1)
    a.add(1)
    a.dispose()
    expect(timers).toHaveLength(0)
    advance(20000)
    expect(announce).toHaveBeenCalledTimes(1)
  })
})
