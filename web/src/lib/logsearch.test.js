import { describe, it, expect } from 'vitest'
import { matchesSearch } from './logsearch.js'

const entry = {
  message: 'Anomaly detected in backup',
  level: 'warn',
  category: 'health',
  jobName: 'docker',
}

describe('matchesSearch', () => {
  it('matches an empty or whitespace-only query', () => {
    expect(matchesSearch(entry, '')).toBe(true)
    expect(matchesSearch(entry, '   ')).toBe(true)
  })

  it('matches message text case-insensitively', () => {
    expect(matchesSearch(entry, 'anomaly')).toBe(true)
    expect(matchesSearch(entry, 'ANOMALY')).toBe(true)
  })

  it('matches the level field', () => {
    expect(matchesSearch(entry, 'warn')).toBe(true)
  })

  it('matches the category field', () => {
    expect(matchesSearch(entry, 'health')).toBe(true)
  })

  it('matches the job name field', () => {
    expect(matchesSearch(entry, 'docker')).toBe(true)
  })

  it('does not match an absent term', () => {
    expect(matchesSearch(entry, 'restore')).toBe(false)
  })

  it('matches the meta fields rendered on the row', () => {
    const withMeta = { ...entry, meta: { backup_type: 'differential', destination: 'hdd', items: 15 } }
    expect(matchesSearch(withMeta, 'diff')).toBe(true)
    expect(matchesSearch(withMeta, 'differential')).toBe(true)
    expect(matchesSearch(withMeta, 'hdd')).toBe(true)
    expect(matchesSearch(withMeta, '15')).toBe(true)
    expect(matchesSearch({ ...withMeta, meta: { backup_type: 'full' } }, 'diff')).toBe(false)
  })

  it('handles entries without meta', () => {
    expect(matchesSearch(entry, 'anomaly')).toBe(true)
    expect(matchesSearch(entry, 'diff')).toBe(false)
  })

  it('does not match a substring that is not present', () => {
    expect(matchesSearch(entry, 'anomx')).toBe(false)
  })
})
