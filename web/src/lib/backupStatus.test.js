import { describe, expect, it } from 'vitest'
import { announcementKey, attentionCaption, countRunOutcomes, deriveBackupStatus, healthText, healthTone, isDiscoveryIncomplete, mergeRunCounts, unhealthyStorage } from './backupStatus.js'

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
    ['failed jobs', { ...READY, failures: 2 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history?status=failed&review=outstanding'] }],
    ['warnings', { ...READY, warnings: 1 }, { state: 'attention', tone: 'warning', title: 'Needs attention', hrefs: ['/history?status=partial&review=outstanding'] }],
    ['mixed', { ...READY, failures: 1, warnings: 1, unprotected: 2 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history?status=failed&review=outstanding', '/history?status=partial&review=outstanding', '/jobs'] }],
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
    ['replica failures', { ...READY, replica: true, failures: 1 }, { state: 'attention', tone: 'danger', title: 'Needs attention', hrefs: ['/history?status=failed&review=outstanding'] }],
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
    expect(countRunOutcomes(runs)).toEqual({ failed: 2, warnings: 1, skipped: 0, cancelled: 1 })
  })

  it('accepts nothing', () => {
    expect(countRunOutcomes(null)).toEqual({ failed: 0, warnings: 0, skipped: 0, cancelled: 0 })
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
    ['a middling score', { ...SUMMARY, health_score: 60 }, {}, 'Needs attention'],
    ['a middling score with unprotected items', { ...SUMMARY, health_score: 60 }, { unprotected: 2 }, 'Needs attention · 2 unprotected'],
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
    ['high, with no failure count anywhere', { health_score: 100, protected_items: 7, recent_success: 5 }, {}, 'success'],
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

describe('false "Backups healthy" (#527)', () => {
  const STALE = { id: 3, name: 'Photos', enabled: false, last_success_at: '2025-03-12T02:00:00Z' }

  it.each([
    ['skipped runs', { skipped: 2 }, 'warning', ['/history?status=skipped&review=outstanding'], '2 runs skipped'],
    ['cancelled runs', { cancelled: 1 }, 'warning', ['/history?status=cancelled&review=outstanding'], '1 run cancelled'],
    ['one unhealthy destination', { unhealthy: ['NAS'] }, 'danger', ['/storage'], 'Storage "NAS" is unhealthy'],
    ['several unhealthy destinations', { unhealthy: ['NAS', 'S3'] }, 'danger', ['/storage'], '2 storage destinations are unhealthy'],
    ['a disabled job gone stale', { summary: { ...SUMMARY, stale_jobs: [STALE] } }, 'warning', ['/jobs'], 'Disabled job "Photos" last backed up 2025-03-12'],
    ['an enabled job gone stale', { summary: { ...SUMMARY, stale_jobs: [{ ...STALE, enabled: true }] } }, 'warning', ['/jobs'], 'Job "Photos" last backed up 2025-03-12'],
    ['several stale jobs', { summary: { ...SUMMARY, stale_jobs: [STALE, STALE] } }, 'warning', ['/jobs'], '2 jobs have no recent backup'],
    ['a low score', { summary: { ...SUMMARY, health_score: 40 } }, 'warning', ['/history'], 'Health score 40 of 100'],
    ['a middling score', { summary: { ...SUMMARY, health_score: 79 } }, 'warning', ['/history'], 'Health score 79 of 100'],
    ['a missing score', { summary: { protected_items: 7, recent_success: 5 } }, 'warning', ['/history'], 'Health score 0 of 100'],
  ])('%s needs attention, names the problem and links to it', (_name, input, tone, expectedHrefs, detail) => {
    const status = deriveBackupStatus({ ...READY, ...input })
    expect(status.state).toBe('attention')
    expect(status.tone).toBe(tone)
    expect(hrefs(status)).toEqual(expectedHrefs)
    expect(status.detail).toBe(detail)
  })

  it('reproduces the review: skipped and cancelled runs, score 40, last success in 2025', () => {
    const summary = { ...SUMMARY, health_score: 40, last_success_at: '2025-03-12T02:00:00Z', recent_skipped: 5, recent_cancelled: 1, stale_jobs: [STALE] }
    const counts = mergeRunCounts(countRunOutcomes([]), summary)
    const status = deriveBackupStatus({ ...READY, ...counts, summary })
    expect(status.state).toBe('attention')
    expect(status.detail).toContain('5 runs skipped')
    expect(status.detail).toContain('1 run cancelled')
    expect(status.detail).toContain('last backed up 2025-03-12')
    expect(status.detail).toContain('Health score 40')
    expect(healthText(summary, counts)).toMatch(/^Needs attention/)
  })

  it('a replica links stale jobs to History, since it has no Jobs page', () => {
    const status = deriveBackupStatus({ ...READY, replica: true, summary: { ...SUMMARY, stale_jobs: [STALE] } })
    expect(hrefs(status)).toEqual(['/history'])
  })

  it('a score with no runs behind it is not a low score', () => {
    const summary = { ...SUMMARY, health_score: 40, recent_success: 0, last_success_at: null }
    expect(deriveBackupStatus({ ...READY, summary }).state).toBe('healthy')
    expect(healthText(summary)).toBe('Backups OK · no recent runs')
    expect(healthTone(summary)).toBe('warning')
  })

  describe('mergeRunCounts', () => {
    const NONE = { failed: 0, warnings: 0, skipped: 0, cancelled: 0 }

    it('takes what only the server saw, such as an eleventh job', () => {
      const merged = mergeRunCounts(NONE, { recent_failed: 1, recent_partial: 2, recent_skipped: 3, recent_cancelled: 4 })
      expect(merged).toEqual({ failures: 1, warnings: 2, skipped: 3, cancelled: 4 })
    })

    it('uses outstanding counters where present and falls back per field', () => {
      const summary = { recent_failed: 4, recent_partial: 3, recent_skipped: 2, outstanding_failed: 0, outstanding_partial: 1 }
      expect(mergeRunCounts(NONE, summary)).toEqual({ failures: 0, warnings: 1, skipped: 2, cancelled: 0 })
    })

    it('clears cancellation and low-score attention when all scored runs were reviewed', () => {
      const summary = { protected_items: 5, recent_success: 0, recent_cancelled: 2, outstanding_cancelled: 0, health_score: 40 }
      const counts = mergeRunCounts(NONE, summary)
      expect(deriveBackupStatus({ ...READY, ...counts, summary }).issues).toEqual([])
      expect(healthText(summary, counts)).toBe('Backups OK · no scored runs')
    })

    it('keeps what only the sample saw, and copes with no summary', () => {
      const sample = { failed: 1, warnings: 2, skipped: 3, cancelled: 4 }
      expect(mergeRunCounts(sample, null)).toEqual({ failures: 1, warnings: 2, skipped: 3, cancelled: 4 })
    })
  })

  describe('countRunOutcomes', () => {
    it('ignores reviewed cancellations and restore failures while counting new failures', () => {
      const runs = [
        { status: 'cancelled', acknowledged_at: '2026-11-08T00:00:00Z' },
        { status: 'failed', run_type: 'restore', acknowledged_at: '2026-11-08T00:00:00Z' },
        { status: 'failed', acknowledged_at: null },
      ]
      expect(countRunOutcomes(runs)).toEqual({ failed: 1, warnings: 0, skipped: 0, cancelled: 0 })
    })

    it('counts skipped and cancelled backups', () => {
      const runs = [{ status: 'skipped' }, { status: 'skipped', run_type: 'backup' }, { status: 'cancelled' }]
      expect(countRunOutcomes(runs)).toMatchObject({ skipped: 2, cancelled: 1 })
    })

    it('ignores a cancelled or partial restore, but not a failed one', () => {
      const runs = ['cancelled', 'partial', 'skipped', 'failed'].map((status) => ({ status, run_type: 'restore' }))
      expect(countRunOutcomes(runs)).toEqual({ failed: 1, warnings: 0, skipped: 0, cancelled: 0 })
    })
  })

  describe('unhealthyStorage', () => {
    it('names destinations with a failed check or an open breaker', () => {
      const storage = [
        { name: 'ok', last_health_check_status: 'ok', breaker_state: 'closed' },
        { name: 'failed', last_health_check_status: 'failed' },
        { name: 'open', last_health_check_status: 'ok', breaker_state: 'open' },
        { name: 'unchecked', last_health_check_status: '' },
      ]
      expect(unhealthyStorage(storage)).toEqual(['failed', 'open'])
    })

    it('accepts nothing', () => {
      expect(unhealthyStorage(null)).toEqual([])
    })
  })

  describe('the banner and the health tile never conflict', () => {
    const SUMMARIES = [
      SUMMARY,
      { ...SUMMARY, health_score: 79 },
      { ...SUMMARY, health_score: 40 },
      { ...SUMMARY, health_score: 0, recent_success: 0, last_success_at: null },
      { ...SUMMARY, stale_jobs: [STALE] },
      { ...FRESH, recent_success: 1 },
      FRESH,
    ]
    const COUNTS = [{}, { failures: 1 }, { warnings: 1 }, { skipped: 1 }, { cancelled: 1 }, { unhealthy: ['NAS'] }, { incomplete: true }, { unprotected: 1 }, { pending: 1 }]

    const cases = SUMMARIES.flatMap((summary) => COUNTS.map((counts) => [summary, counts]))

    it.each(cases)('%j with %j', (summary, counts) => {
      const status = deriveBackupStatus({ ...READY, ...counts, summary })
      const text = healthText(summary, { ...counts, unhealthy: counts.unhealthy?.length ?? 0 })
      if (status.state === 'healthy') expect(text).not.toMatch(/needs attention|failure|skipped|cancelled|overdue|unhealthy/i)
      if (text === 'All backups healthy') expect(status.state).toBe('healthy')
      if (/^Needs attention/.test(text)) expect(status.state).toBe('attention')
    })
  })
})

describe('attentionCaption with other issues', () => {
  it.each([
    [{ other: 2 }, 'No failures · 2 other issues'],
    [{ failures: 1, other: 1 }, '1 recent failure · 0 unprotected · 1 other issue'],
  ])('%j', (input, expected) => {
    expect(attentionCaption(input)).toBe(expected)
  })
})
