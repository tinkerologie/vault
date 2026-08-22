import { describe, it, expect } from 'vitest'
import { lineTimestamp } from './tsformat.js'

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
