import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// Force 24-hour clock so schedule/time strings are deterministic across the
// runner's locale (getHour12() otherwise drives an 'auto' locale branch).
vi.mock('./runtime-config.js', () => ({ getHour12: () => false }))

import {
  formatBytes,
  formatDuration,
  formatSpeed,
  statusColor,
  statusBadge,
  parseConfig,
  describeSchedule,
  relTimeUntil,
  prettyAnomalySummary,
} from './utils.js'

describe('formatBytes', () => {
  it('handles zero / falsy', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(null)).toBe('0 B')
    expect(formatBytes(undefined)).toBe('0 B')
  })
  it('scales units', () => {
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1048576)).toBe('1 MB')
    expect(formatBytes(1073741824)).toBe('1 GB')
  })
})

describe('formatDuration', () => {
  it('rejects nullish / negative', () => {
    expect(formatDuration(null)).toBe('–')
    expect(formatDuration(-5)).toBe('–')
  })
  it('formats seconds / minutes / hours', () => {
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(90)).toBe('1m 30s')
    expect(formatDuration(3661)).toBe('1h 1m')
  })
})

describe('formatSpeed', () => {
  it('returns null when inputs are missing', () => {
    expect(formatSpeed(0, 1)).toBeNull()
    expect(formatSpeed(100, 0)).toBeNull()
  })
  it('formats bytes/sec', () => {
    expect(formatSpeed(1048576, 1)).toBe('1 MB/s')
  })
})

// These two back the cross-cutting "partial status" cluster — lock current
// behaviour so a deliberate change shows up as a failing characterization test.
describe('statusColor / statusBadge', () => {
  it('maps known statuses (case-insensitive)', () => {
    expect(statusColor('partial')).toBe('text-warning')
    expect(statusColor('PENDING')).toBe('text-warning')
    expect(statusColor('failed')).toBe('text-danger')
    expect(statusColor('success')).toBe('text-success')
    expect(statusBadge('partial')).toBe('badge badge-warning')
    expect(statusBadge('success')).toBe('badge badge-success')
  })
  it('falls back for unknown / missing', () => {
    expect(statusColor('nope')).toBe('text-text-muted')
    expect(statusColor(undefined)).toBe('text-text-muted')
    expect(statusBadge('nope')).toBe('badge badge-neutral')
  })
})

describe('parseConfig', () => {
  it('handles empty, object, valid and invalid JSON', () => {
    expect(parseConfig('')).toEqual({})
    expect(parseConfig(null)).toEqual({})
    expect(parseConfig({ a: 1 })).toEqual({ a: 1 })
    expect(parseConfig('{"a":1}')).toEqual({ a: 1 })
    expect(parseConfig('{bad')).toEqual({})
  })
})

describe('describeSchedule', () => {
  it('manual / passthrough', () => {
    expect(describeSchedule('')).toBe('Manual only')
    expect(describeSchedule('not a cron')).toBe('not a cron')
  })
  it('daily / weekly / monthly / yearly', () => {
    expect(describeSchedule('0 2 * * *')).toBe('Daily at 02:00')
    expect(describeSchedule('30 14 * * *')).toBe('Daily at 14:30')
    expect(describeSchedule('0 2 * * 1')).toBe('Weekly on Mon at 02:00')
    expect(describeSchedule('0 2 * * 1,3,5')).toBe('Mon, Wed, Fri at 02:00')
    expect(describeSchedule('0 2 1 * *')).toBe('Monthly on 1st at 02:00')
    expect(describeSchedule('0 2 L * *')).toBe('Monthly on last day at 02:00')
    expect(describeSchedule('0 2 5 6 *')).toBe('Yearly on June 5th at 02:00')
  })
})

describe('relTimeUntil', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
  })
  afterEach(() => vi.useRealTimers())
  const at = (ms) => new Date(Date.now() + ms).toISOString()
  it('handles nullish and past', () => {
    expect(relTimeUntil(null)).toBeNull()
    expect(relTimeUntil(at(-1000))).toBe('overdue')
  })
  it('formats future offsets', () => {
    expect(relTimeUntil(at(30 * 60000))).toBe('in 30m')
    expect(relTimeUntil(at(2 * 3600000))).toBe('in 2h')
    expect(relTimeUntil(at(90 * 60000))).toBe('in 1h 30m')
    expect(relTimeUntil(at(2 * 86400000))).toBe('in 2d')
  })
})

describe('prettyAnomalySummary', () => {
  it('humanizes byte counts and bare-second durations', () => {
    expect(prettyAnomalySummary('size anomaly: 1048576 bytes (0.8x)')).toContain('1 MB')
    expect(prettyAnomalySummary('duration anomaly: 90s (0.8x)')).toContain('1m 30s')
  })

  it('corrects contradictory legacy size summaries without rewriting history', () => {
    expect(prettyAnomalySummary('This backup grew to 2.7 GB, about <1× its usual 2.8 GB.'))
      .toBe('This backup shrank to 2.7 GB, about <1× its usual 2.8 GB.')
  })
})
