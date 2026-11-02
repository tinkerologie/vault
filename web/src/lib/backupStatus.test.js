import { describe, expect, it } from 'vitest'
import { announcementKey, attentionCaption, countRunOutcomes, deriveBackupStatus, healthText, healthTone, isDiscoveryIncomplete } from './backupStatus.js'

// The Dashboard's single verdict (#527).
const SUMMARY = { health_score: 100, protected_items: 7, recent_success: 5, recent_failed: 0, last_success_at: '2026-10-06T02:15:00Z' }
const FRESH = { health_score: 0, protected_items: 0, recent_success: 0, recent_failed: 0, last_success_at: null }
const READY = { hasStorage: true, hasJobs: true, protectedCount: 7, total: 7, summary: SUMMARY }
const hrefs = (status) => status.issues.map((issue) => issue.href)
const keys = (status) => status.issues.map((issue) => issue.key)

describe('deriveBackupStatus', () => {
  it.each([
    ['healthy', READY, { state: 'healthy', tone: 'success', title: 'Backups healthy', hrefs: [] }],
    ['unprotected', { ...READY, unprotected: 3, protectedCount: 4 }, { state: 'attention', tone: 'warning', title: 'Needs attention', hrefs: ['/jobs'] }],
    ['failed jobs', { ...READY, failures: 2 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history'] }],
    ['warnings', { ...READY, warnings: 1 }, { state: 'attention', tone: 'warning', title: 'Needs attention', hrefs: ['/history'] }],
    ['mixed', { ...READY, failures: 1, warnings: 1, unprotected: 2 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history', '/history', '/jobs'] }],
    ['first run', { summary: FRESH }, { state: 'setup', tone: 'info', title: 'Set up your first backup', hrefs: ['/storage'] }],
    ['jobs without storage', { hasJobs: true, summary: FRESH }, { state: 'setup', tone: 'info', title: 'Set up your first backup', hrefs: ['/storage'] }],
    ['storage without jobs', { hasStorage: true, summary: FRESH }, { state: 'setup', tone: 'info', title: 'Create your first backup job', hrefs: ['/jobs'] }],
    ['waiting', { hasStorage: true, hasJobs: true, pending: 3, total: 3, summary: FRESH }, { state: 'waiting', tone: 'info', title: 'No backups completed yet', hrefs: ['/jobs'] }],
    ['loading', { ...READY, loading: true }, { state: 'loading', tone: 'neutral', title: 'Checking backup status…', hrefs: [] }],
    ['API error', { ...READY, error: true }, { state: 'error', tone: 'danger', title: "Couldn't load backup status", hrefs: ['/logs'] }],
    ['null summary', { ...READY, summary: null }, { state: 'unknown', tone: 'neutral', title: 'Backup health unavailable', hrefs: ['/history'] }],
    ['incomplete data', { ...READY, incomplete: true }, { state: 'unknown', tone: 'neutral', title: 'Backup health unavailable', hrefs: ['/history'] }],
    ['replica healthy', { ...READY, replica: true }, { state: 'healthy', tone: 'success', title: 'Backups healthy', hrefs: [] }],
    ['replica ignores unprotected', { ...READY, replica: true, unprotected: 3 }, { state: 'healthy', tone: 'success', title: 'Backups healthy', hrefs: [] }],
    ['replica failures', { ...READY, replica: true, failures: 1 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history'] }],
    ['replica first run', { replica: true }, { state: 'setup', tone: 'info', title: 'No backup data on this replica yet', hrefs: ['/replication'] }],
    ['replica waiting', { hasStorage: true, hasJobs: true, replica: true, pending: 1, total: 1, summary: FRESH }, { state: 'waiting', tone: 'info', title: 'No backups completed yet', hrefs: [] }],
  ])('%s', (_name, input, expected) => {
    const status = deriveBackupStatus(input)
    expect(status.state).toBe(expected.state)
    expect(status.tone).toBe(expected.tone)
    expect(status.title).toBe(expected.title)
    expect(hrefs(status)).toEqual(expected.hrefs)
  })

  it('reproduces the audit: score 100 with 3 of 19 unprotected is not healthy', () => {
    const status = deriveBackupStatus({ ...READY, unprotected: 3, protectedCount: 16, total: 19 })
    expect(status.state).toBe('attention')
    expect(status.detail).toBe('3 of 19 items unprotected')
    expect(`${status.title} ${status.detail}`).not.toMatch(/healthy/i)
  })

  it.each([
    ['loading', { ...READY, loading: true }],
    ['error', { ...READY, error: true }],
    ['null summary', { ...READY, summary: null }],
    ['incomplete', { ...READY, incomplete: true }],
    ['zero protected', { hasStorage: true, hasJobs: true, total: 4, pending: 4, summary: { ...SUMMARY, protected_items: 0 } }],
  ])('never claims healthy while %s', (_name, input) => {
    const status = deriveBackupStatus(input)
    expect(status.state).not.toBe('healthy')
    expect(status.announcement).not.toMatch(/healthy/i)
  })

  it('loading and error win over everything else', () => {
    expect(deriveBackupStatus({ loading: true, error: true }).state).toBe('loading')
    expect(deriveBackupStatus({ error: true, failures: 3 }).state).toBe('error')
  })

  it('does not call items unprotected when the summary failed, since nothing is known', () => {
    const status = deriveBackupStatus({ ...READY, summary: null, unprotected: 7, protectedCount: 0 })
    expect(status.state).toBe('unknown')
    expect(status.detail).not.toMatch(/unprotected/)
  })

  it('still reports known failures when other data is missing, and says what is missing', () => {
    const status = deriveBackupStatus({ ...READY, summary: null, failures: 1 })
    expect(status.state).toBe('attention')
    expect(status.detail).toBe('1 recent failure · some status data is unavailable')
    expect(keys(status)).toEqual(['failures'])
  })

  it('names every problem in order', () => {
    const status = deriveBackupStatus({ ...READY, failures: 2, warnings: 3, unprotected: 1, pending: 2, total: 10 })
    expect(status.detail).toBe('2 recent failures · 3 runs finished with warnings · 1 of 10 items unprotected · 2 items waiting for first backup')
  })

  it('uses singular wording', () => {
    expect(deriveBackupStatus({ ...READY, warnings: 1 }).detail).toBe('1 run finished with warnings')
    expect(deriveBackupStatus({ ...READY, unprotected: 1, total: 1 }).detail).toBe('1 of 1 item unprotected')
  })

  describe('healthy details', () => {
    it('counts the items protected', () => {
      expect(deriveBackupStatus(READY).detail).toBe('All 7 items protected.')
      expect(deriveBackupStatus({ ...READY, protectedCount: 1, total: 1 }).detail).toBe('The 1 tracked item is protected.')
    })

    it('qualifies items waiting for a first backup', () => {
      const status = deriveBackupStatus({ ...READY, protectedCount: 5, pending: 2 })
      expect(status.state).toBe('healthy')
      expect(status.detail).toBe('2 items waiting for first backup')
    })

    it('does not claim all items protected when none were discovered', () => {
      const status = deriveBackupStatus({ ...READY, protectedCount: 0, total: 0 })
      expect(status.state).toBe('healthy')
      expect(status.detail).toBe('Recent backups completed.')
    })
  })

  describe('waiting', () => {
    it('explains itself when no items are listed as pending', () => {
      const status = deriveBackupStatus({ hasStorage: true, hasJobs: true, summary: FRESH })
      expect(status.state).toBe('waiting')
      expect(status.detail).toMatch(/first backup will run/)
    })

    it('says no items are backed up when runs completed without capturing any', () => {
      const status = deriveBackupStatus({ hasStorage: true, hasJobs: true, total: 2, pending: 2, summary: { ...FRESH, recent_success: 1 } })
      expect(status.state).toBe('waiting')
      expect(status.title).toBe('No items backed up yet')
    })

    it('history purged with restore points still present is not "no backups"', () => {
      const status = deriveBackupStatus({ ...READY, summary: { ...FRESH, protected_items: 7 } })
      expect(status.state).toBe('healthy')
    })
  })

  it('announces title and detail, or the title alone', () => {
    expect(deriveBackupStatus(READY).announcement).toBe('Backups healthy. All 7 items protected.')
    expect(deriveBackupStatus({ loading: true }).announcement).toBe('Checking backup status…')
  })
})

describe('announcementKey', () => {
  it('is null while loading, so the first verdict has nothing to differ from', () => {
    expect(announcementKey(deriveBackupStatus({ loading: true }))).toBeNull()
  })

  it('ignores counts', () => {
    const a = deriveBackupStatus({ ...READY, unprotected: 3 })
    const b = deriveBackupStatus({ ...READY, unprotected: 2 })
    expect(a.detail).not.toBe(b.detail)
    expect(announcementKey(a)).toBe(announcementKey(b))
  })

  it('changes with the state and the kinds of problem', () => {
    const healthy = announcementKey(deriveBackupStatus(READY))
    const unprotected = announcementKey(deriveBackupStatus({ ...READY, unprotected: 1 }))
    const mixed = announcementKey(deriveBackupStatus({ ...READY, unprotected: 1, failures: 1 }))
    expect(new Set([healthy, unprotected, mixed]).size).toBe(3)
  })
})

describe('countRunOutcomes', () => {
  it('sorts runs into failed and warnings', () => {
    const runs = ['failed', 'error', 'partial', 'completed', 'success', 'running', 'cancelled'].map((status) => ({ status }))
    expect(countRunOutcomes(runs)).toEqual({ failed: 2, warnings: 1 })
  })

  it('accepts nothing', () => {
    expect(countRunOutcomes(null)).toEqual({ failed: 0, warnings: 0 })
  })
})

describe('isDiscoveryIncomplete', () => {
  const ok = { available: true }
  const off = { available: false }

  it('is false when everything answered', () => {
    expect(isDiscoveryIncomplete({ container: ok, vm: ok, folder: ok }, SUMMARY)).toBe(false)
  })

  it('is true when a request failed', () => {
    expect(isDiscoveryIncomplete({ container: ok, vm: { ...off, failed: true } }, SUMMARY)).toBe(true)
  })

  it('is false for an unavailable type no job uses, such as a server without VMs', () => {
    expect(isDiscoveryIncomplete({ container: ok, vm: off }, { protected_keys: ['container:plex'] })).toBe(false)
  })

  it.each([['protected_keys'], ['pending_keys']])('is true for an unavailable type listed in %s', (field) => {
    expect(isDiscoveryIncomplete({ container: off }, { [field]: ['container:plex'] })).toBe(true)
  })

  it('copes with no summary', () => {
    expect(isDiscoveryIncomplete({ container: off }, null)).toBe(false)
  })
})

describe('healthText', () => {
  it('reproduces the audit: score 100 with items unprotected is qualified', () => {
    const text = healthText(SUMMARY, { unprotected: 3 })
    expect(text).toBe('Backups OK · 3 unprotected')
    expect(text).not.toMatch(/healthy/i)
  })

  it.each([
    ['null summary', null, {}, 'Health unavailable'],
    ['clean high score', SUMMARY, {}, 'All backups healthy'],
    ['one failure', SUMMARY, { failures: 1 }, '1 recent failure'],
    ['several failures', SUMMARY, { failures: 3 }, '3 recent failures'],
    ['failures from the summary when no count is given', { ...SUMMARY, recent_failed: 2 }, {}, '2 recent failures'],
    ['nothing has run', FRESH, {}, 'No backups completed yet'],
    ['a fresh install with unprotected items', FRESH, { unprotected: 2 }, 'No backups completed yet'],
    ['warnings', SUMMARY, { warnings: 1 }, 'Backups OK · 1 with warnings'],
    ['pending', SUMMARY, { pending: 2 }, 'Backups OK · 2 pending'],
    ['every qualifier', SUMMARY, { unprotected: 1, warnings: 2, pending: 3 }, 'Backups OK · 1 unprotected · 2 with warnings · 3 pending'],
    ['a middling score', { ...SUMMARY, health_score: 60 }, {}, 'Backups mostly healthy'],
    ['a middling score with unprotected items', { ...SUMMARY, health_score: 60 }, { unprotected: 2 }, 'Backups mostly healthy · 2 unprotected'],
    ['a low score', { ...SUMMARY, health_score: 10 }, {}, 'Needs attention'],
    ['a missing score', { recent_success: 1 }, {}, 'Needs attention'],
  ])('%s', (_name, summary, counts, expected) => {
    expect(healthText(summary, counts)).toBe(expected)
  })
})

describe('healthTone', () => {
  it.each([
    ['null summary', null, {}, 'unknown'],
    ['clean', SUMMARY, {}, 'success'],
    ['unprotected at 100', SUMMARY, { unprotected: 3 }, 'warning'],
    ['pending at 100', SUMMARY, { pending: 1 }, 'warning'],
    ['warnings at 100', SUMMARY, { warnings: 1 }, 'warning'],
    ['failures at 100', SUMMARY, { failures: 1 }, 'warning'],
    ['failures from the summary', { ...SUMMARY, recent_failed: 1 }, {}, 'warning'],
    ['middling', { ...SUMMARY, health_score: 60 }, {}, 'warning'],
    ['low', { ...SUMMARY, health_score: 49 }, {}, 'danger'],
    ['no score', {}, {}, 'danger'],
  ])('%s', (_name, summary, counts, expected) => {
    expect(healthTone(summary, counts)).toBe(expected)
  })
})

describe('attentionCaption', () => {
  it.each([
    [{ failures: 1, unprotected: 2 }, '1 recent failure · 2 unprotected'],
    [{ failures: 2 }, '2 recent failures · 0 unprotected'],
    [{ fullyProtected: true }, 'No failures · all items protected'],
    [{ fullyProtected: false }, 'No failures'],
    [{ known: false }, 'No failures · protection unknown'],
    [{}, 'No failures'],
  ])('%j', (input, expected) => {
    expect(attentionCaption(input)).toBe(expected)
  })
})
