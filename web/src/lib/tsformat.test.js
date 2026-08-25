import { describe, it, expect } from 'vitest'
import { lineTimestamp, rowTime, dayKey, dayLabel } from './tsformat.js'

describe('lineTimestamp', () => {
  it('formats a Date as "YYYY-MM-DD HH:MM:SS" in 24h', () => {
    // Local constructor + local getters → timezone-independent.
    const d = new Date(2026, 7, 19, 14, 23, 1) // Aug 19 2026 14:23:01 local
    expect(lineTimestamp(d)).toBe('2026-08-19 14:23:01')
  })

  it('zero-pads single-digit month/day/hour/minute/second', () => {
    const d = new Date(2026, 0, 5, 9, 3, 7) // Jan 5 2026 09:03:07 local
    expect(lineTimestamp(d)).toBe('2026-01-05 09:03:07')
  })

  it('returns a placeholder for an invalid timestamp', () => {
    expect(lineTimestamp('not-a-date')).toBe('--:--:--')
  })
})

describe('rowTime', () => {
  it('drops the date — the console shows it once per day separator', () => {
    expect(rowTime('2026-08-25T09:05:03')).toBe('09:05:03')
  })

  it('zero-pads so the column stays fixed-width', () => {
    expect(rowTime(new Date(2026, 7, 5, 1, 2, 3))).toBe('01:02:03')
  })

  it('degrades to a placeholder on an unparseable timestamp', () => {
    expect(rowTime('not a date')).toBe('--:--:--')
  })
})

describe('dayKey', () => {
  it('groups by the local calendar day, not UTC', () => {
    expect(dayKey(new Date(2026, 7, 25, 23, 59, 59))).toBe('2026-08-25')
    expect(dayKey(new Date(2026, 7, 26, 0, 0, 1))).toBe('2026-08-26')
  })

  it('returns an empty key for an unparseable timestamp', () => {
    expect(dayKey('nope')).toBe('')
  })
})

describe('dayLabel', () => {
  const now = new Date(2026, 7, 25, 12, 0, 0)

  it('names today and yesterday', () => {
    expect(dayLabel(new Date(2026, 7, 25, 9, 0, 0), now)).toBe('Today')
    expect(dayLabel(new Date(2026, 7, 24, 9, 0, 0), now)).toBe('Yesterday')
  })

  it('falls back to the date for anything older', () => {
    expect(dayLabel(new Date(2026, 7, 23, 9, 0, 0), now)).toBe('2026-08-23')
  })

  it('handles yesterday across a month boundary', () => {
    const firstOfMonth = new Date(2026, 8, 1, 12, 0, 0)
    expect(dayLabel(new Date(2026, 7, 31, 9, 0, 0), firstOfMonth)).toBe('Yesterday')
  })
})
