import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  describeSchedule,
  formatClockTime,
  formatDate,
  formatDateCompact,
  formatDurationFromDates,
  getFailureReason,
  relTime,
  relTimeUntil,
} from './utils.js'

// Additional utils coverage (issue #458). Unlike utils.test.js this file does
// not mock runtime-config: the time format comes from a stubbed
// window.__VAULT_RUNTIME_CONFIG__, exercising the real 12h/24h/auto paths.

function timeFormat(fmt) {
  vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: { timeFormat: fmt } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('formatDate / formatDateCompact', () => {
  const iso = '2026-03-04T15:07:00Z'

  it.each([formatDate, formatDateCompact])('returns a dash for empty or invalid input (%o)', (fn) => {
    expect(fn('')).toBe('–')
    expect(fn(null)).toBe('–')
    expect(fn('not a date')).toBe('–')
  })

  it('passes hour12 through for 12h and 24h formats', () => {
    const spy = vi.spyOn(Date.prototype, 'toLocaleString')
    timeFormat('12h')
    formatDate(iso)
    expect(spy.mock.calls.at(-1)[1]).toMatchObject({ year: 'numeric', hour12: true })
    timeFormat('24h')
    formatDateCompact(iso)
    expect(spy.mock.calls.at(-1)[1]).toMatchObject({ hour: 'numeric', hour12: false })
    spy.mockRestore()
  })

  it('omits hour12 in auto mode so the locale decides', () => {
    const spy = vi.spyOn(Date.prototype, 'toLocaleString')
    timeFormat('auto')
    formatDate(iso)
    expect(spy.mock.calls.at(-1)[1]).not.toHaveProperty('hour12')
    formatDateCompact(iso)
    expect(spy.mock.calls.at(-1)[1]).not.toHaveProperty('hour12')
    spy.mockRestore()
  })

  it('produces a non-empty string for a valid date', () => {
    expect(formatDate(iso)).toMatch(/2026/)
    expect(formatDateCompact(iso)).not.toBe('–')
  })
})

describe('formatClockTime', () => {
  it('pads to HH:MM in 24h mode', () => {
    timeFormat('24h')
    expect(formatClockTime(3, 5)).toBe('03:05')
    expect(formatClockTime(23, 59)).toBe('23:59')
  })

  it.each([
    [0, 0, '12:00 AM'],
    [9, 30, '9:30 AM'],
    [12, 15, '12:15 PM'],
    [13, 5, '1:05 PM'],
  ])('renders %i:%i as %s in 12h mode', (h, m, expected) => {
    timeFormat('12h')
    expect(formatClockTime(h, m)).toBe(expected)
  })

  it('delegates to the browser locale in auto mode', () => {
    timeFormat('auto')
    const spy = vi.spyOn(Date.prototype, 'toLocaleTimeString').mockReturnValue('LOCALE')
    expect(formatClockTime(7, 0)).toBe('LOCALE')
    expect(spy).toHaveBeenCalledWith(undefined, { hour: 'numeric', minute: '2-digit' })
    spy.mockRestore()
  })
})

describe('relTime', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-01T12:00:00Z'))
  })

  it.each([
    ['', '–'],
    ['garbage', '–'],
    ['2026-06-01T11:59:40Z', 'just now'],
    ['2026-06-01T11:15:00Z', '45m ago'],
    ['2026-06-01T07:00:00Z', '5h ago'],
    ['2026-05-29T12:00:00Z', '3d ago'],
  ])('%s -> %s', (input, expected) => {
    expect(relTime(input)).toBe(expected)
  })

  it('falls back to an absolute date beyond 30 days', () => {
    expect(relTime('2026-01-01T00:00:00Z')).toBe(formatDate('2026-01-01T00:00:00Z'))
  })
})

describe('relTimeUntil', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-01T12:00:00Z'))
  })

  it.each([
    [null, null],
    ['garbage', null],
    ['2026-06-01T11:00:00Z', 'overdue'],
    ['2026-06-01T12:00:30Z', 'in < 1m'],
    ['2026-06-01T12:20:00Z', 'in 20m'],
    ['2026-06-01T14:00:00Z', 'in 2h'],
    ['2026-06-01T14:15:00Z', 'in 2h 15m'],
    ['2026-06-04T12:00:00Z', 'in 3d'],
  ])('%s -> %s', (input, expected) => {
    expect(relTimeUntil(input)).toBe(expected)
  })
})

describe('describeSchedule edge cases', () => {
  beforeEach(() => timeFormat('24h'))

  it('returns the raw expression when hour or minute is not numeric', () => {
    expect(describeSchedule('x 2 * * *')).toBe('x 2 * * *')
  })
})

describe('getFailureReason', () => {
  it('ignores successful runs', () => {
    expect(getFailureReason({ status: 'success', log: 'x' })).toBeNull()
  })

  it('handles a missing log', () => {
    expect(getFailureReason({ status: 'failed' })).toBe('Unknown error')
    expect(getFailureReason({ status: 'partial' })).toBeNull()
  })

  it('summarizes structured per-item failures', () => {
    const log = JSON.stringify([
      { status: 'ok' },
      { status: 'failed', error: 'qemu: lock held' },
      { status: 'error', error: 'second' },
    ])
    expect(getFailureReason({ status: 'failed', log })).toBe('qemu: lock held (+1 more failed)')
    const single = JSON.stringify([{ status: 'error', error: 'only' }])
    expect(getFailureReason({ status: 'error', log: single })).toBe('only')
    const noMsg = JSON.stringify([{ status: 'failed' }, { status: 'failed' }])
    expect(getFailureReason({ status: 'failed', log: noMsg })).toBe('2 item(s) failed')
  })

  it('falls back to the generic message for structured logs without failures', () => {
    const log = JSON.stringify([{ status: 'ok' }])
    expect(getFailureReason({ status: 'failed', log })).toBe('Backup failed – see Logs for details')
    expect(getFailureReason({ status: 'partial', log })).toBeNull()
  })

  it('prefers an error line in a plain-text log, else the first line', () => {
    expect(getFailureReason({ status: 'failed', log: 'starting\nERROR: disk full\nend' })).toBe('ERROR: disk full')
    expect(getFailureReason({ status: 'failed', log: '\n  All targets are missing  \nmore' })).toBe('All targets are missing')
    expect(getFailureReason({ status: 'failed', log: 'x'.repeat(800) })).toHaveLength(500)
  })
})

describe('formatDurationFromDates', () => {
  it('returns a dash for missing or invalid dates', () => {
    expect(formatDurationFromDates(null, '2026-01-01')).toBe('–')
    expect(formatDurationFromDates('2026-01-01', '')).toBe('–')
    expect(formatDurationFromDates('bad', '2026-01-01')).toBe('–')
  })

  it('formats the elapsed time between two dates', () => {
    expect(formatDurationFromDates('2026-01-01T00:00:00Z', '2026-01-01T00:11:04Z')).toBe('11m 4s')
  })
})
