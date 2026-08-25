import { describe, it, expect } from 'vitest'
import { buildRows, rowEntries } from './logrows.js'

const NOW = new Date('2026-08-25T12:00:00')

function entry(id, ts, level = 'info', category = 'backup', message = 'msg') {
  return { id, ts, level, category, message }
}

const opts = { now: NOW }

describe('buildRows ordering', () => {
  it('renders newest first', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'info', 'backup', 'oldest'),
      entry(2, '2026-08-25T10:00:00', 'info', 'backup', 'middle'),
      entry(3, '2026-08-25T11:00:00', 'info', 'backup', 'newest'),
    ], opts)

    const messages = rows.filter(r => r.kind === 'entry').map(r => r.entry.message)
    expect(messages).toEqual(['newest', 'middle', 'oldest'])
  })

  it('leaves the caller’s array untouched', () => {
    const entries = [
      entry(1, '2026-08-25T09:00:00'),
      entry(2, '2026-08-25T10:00:00'),
    ]
    buildRows(entries, opts)
    expect(entries.map(e => e.id)).toEqual([1, 2])
  })

  it('returns no rows for an empty or missing buffer', () => {
    expect(buildRows([], opts)).toEqual([])
    expect(buildRows(null, opts)).toEqual([])
    expect(buildRows(undefined, opts)).toEqual([])
  })
})

describe('date separators', () => {
  it('opens a separator before each calendar day, newest day first', () => {
    const rows = buildRows([
      entry(1, '2026-08-23T09:00:00'),
      entry(2, '2026-08-24T09:00:00'),
      entry(3, '2026-08-25T09:00:00'),
    ], opts)

    expect(rows.filter(r => r.kind === 'date').map(r => r.label))
      .toEqual(['Today', 'Yesterday', '2026-08-23'])
    expect(rows[0].kind).toBe('date')
  })

  it('does not repeat a separator within one day', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'info', 'backup', 'a'),
      entry(2, '2026-08-25T10:00:00', 'info', 'backup', 'b'),
      entry(3, '2026-08-25T11:00:00', 'info', 'backup', 'c'),
    ], opts)
    expect(rows.filter(r => r.kind === 'date')).toHaveLength(1)
  })

  it('counts the entries that fall under each separator', () => {
    const rows = buildRows([
      entry(1, '2026-08-24T09:00:00', 'info', 'backup', 'a'),
      entry(2, '2026-08-25T09:00:00', 'info', 'backup', 'b'),
      entry(3, '2026-08-25T10:00:00', 'info', 'backup', 'c'),
    ], opts)
    const dates = rows.filter(r => r.kind === 'date')
    expect(dates.map(d => [d.label, d.count])).toEqual([['Today', 2], ['Yesterday', 1]])
  })
})

describe('collapsing consecutive duplicates', () => {
  it('folds an identical run into one row carrying the count', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'Health check: plex'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'Health check: plex'),
      entry(3, '2026-08-25T09:00:02', 'warn', 'health', 'Health check: plex'),
    ], opts)

    const entryRows = rows.filter(r => r.kind === 'entry')
    expect(entryRows).toHaveLength(1)
    expect(entryRows[0].repeat).toBe(3)
  })

  it('shows the newest timestamp of the run, since that is the row the user reads', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T09:00:09', 'warn', 'health', 'same'),
    ], opts)
    expect(rows.find(r => r.kind === 'entry').entry.id).toBe(2)
  })

  it('keeps every folded entry reachable for copy and export', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'same'),
    ], opts)
    expect(rowEntries(rows.find(r => r.kind === 'entry')).map(e => e.id)).toEqual([2, 1])
  })

  it('folds lines that differ only in their key=value tail — they read as one line', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'info', 'backup', 'Uploaded recyclarr, file=a.tar'),
      entry(2, '2026-08-25T09:00:01', 'info', 'backup', 'Uploaded recyclarr, file=b.tar'),
      entry(3, '2026-08-25T09:00:02', 'info', 'backup', 'Uploaded recyclarr, file=c.tar'),
    ], opts)

    const entryRows = rows.filter(r => r.kind === 'entry')
    expect(entryRows).toHaveLength(1)
    expect(entryRows[0].repeat).toBe(3)
    // every original line still reachable, so nothing is lost to the fold
    expect(rowEntries(entryRows[0]).map(e => e.id)).toEqual([3, 2, 1])
  })

  it('keeps similar-but-different lines apart — the differing part is the point', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'Health check: plex'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'Health check: sonarr'),
    ], opts)
    expect(rows.filter(r => r.kind === 'entry')).toHaveLength(2)
  })

  it('does not fold across a different level or category', () => {
    const sameText = 'identical text'
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'info', 'health', sameText),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', sameText),
      entry(3, '2026-08-25T09:00:02', 'warn', 'backup', sameText),
    ], opts)
    expect(rows.filter(r => r.kind === 'entry')).toHaveLength(3)
  })

  it('does not fold across a day boundary — a separator sits between them', () => {
    const rows = buildRows([
      entry(1, '2026-08-24T23:59:59', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T00:00:01', 'warn', 'health', 'same'),
    ], opts)
    expect(rows.filter(r => r.kind === 'entry')).toHaveLength(2)
  })

  it('does not fold a run that is interrupted', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'different'),
      entry(3, '2026-08-25T09:00:02', 'warn', 'health', 'same'),
    ], opts)
    const entryRows = rows.filter(r => r.kind === 'entry')
    expect(entryRows).toHaveLength(3)
    expect(entryRows.every(r => r.repeat === 1)).toBe(true)
  })

  it('gives every entry its own row when collapsing is off', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'same'),
      entry(3, '2026-08-25T09:00:02', 'warn', 'health', 'same'),
    ], { ...opts, collapse: false })

    const entryRows = rows.filter(r => r.kind === 'entry')
    expect(entryRows).toHaveLength(3)
    expect(entryRows.every(r => r.repeat === 1)).toBe(true)
  })

  it('counts a folded run once per hidden entry under its date separator', () => {
    const rows = buildRows([
      entry(1, '2026-08-25T09:00:00', 'warn', 'health', 'same'),
      entry(2, '2026-08-25T09:00:01', 'warn', 'health', 'same'),
    ], opts)
    expect(rows.find(r => r.kind === 'date').count).toBe(2)
  })
})

describe('row keys', () => {
  it('are unique so keyed each-blocks do not collide', () => {
    const rows = buildRows([
      entry(1, '2026-08-24T09:00:00', 'info', 'backup', 'a'),
      entry('rl-7', '2026-08-25T09:00:00', 'info', 'backup', 'b'),
      entry(2, '2026-08-25T10:00:00', 'info', 'backup', 'c'),
    ], opts)
    const keys = rows.map(r => r.key)
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('stringify numeric and run-log ids alike', () => {
    const rows = buildRows([entry('rl-7', '2026-08-25T09:00:00')], opts)
    expect(rows.find(r => r.kind === 'entry').key).toBe('rl-7')
  })
})
