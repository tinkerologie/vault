import { describe, it, expect } from 'vitest'
import { splitMessageMeta, formatLine, logLine } from './logline.js'

describe('splitMessageMeta', () => {
  it.each([
    // name, message, wantMessage, wantMeta
    ['null message', null, '', ''],
    ['empty message', '', '', ''],
    ['no kv tail stays whole', 'Backup started', 'Backup started', ''],
    ['comma not followed by key= stays whole', 'Error: something bad happened, retrying', 'Error: something bad happened, retrying', ''],
    ['terminal summary splits at first kv', 'Backup finished, job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s', 'Backup finished', 'job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s'],
    ['started line splits at job', 'Backup started, job="USB"', 'Backup started', 'job="USB"'],
    ['per-item backed up line splits', 'Backed up src-a (folder), size=42 KB', 'Backed up src-a (folder)', 'size=42 KB'],
    ['uploaded line splits at file', 'Uploaded Plex, file=volume_0.tar', 'Uploaded Plex', 'file=volume_0.tar'],
    ['health check splits', 'Health check, container=Plex, status=ok', 'Health check', 'container=Plex, status=ok'],
    ['message with comma inside the phrase still splits at kv', 'Backed up "src, old", size=1 KB', 'Backed up "src, old"', 'size=1 KB'],
  ])('%s', (_name, message, wantMessage, wantMeta) => {
    const got = splitMessageMeta(message)
    expect(got.message).toBe(wantMessage)
    expect(got.meta).toBe(wantMeta)
  })
})

describe('formatLine', () => {
  it.each([
    // name, entry, wantMessage, wantMeta
    [
      'run-log row keeps its inline kv tail, no extras',
      { type: 'runlog', message: 'Backup finished, job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s', meta: null },
      'Backup finished',
      'job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s',
    ],
    [
      'activity row appends details extras not in the tail',
      { type: 'activity', message: 'Backup started, job="USB"', meta: { job_name: 'USB', backup_type: 'full', schedule: 'daily' }, jobName: 'USB' },
      'Backup started',
      'job="USB", type=Full, schedule="daily"',
    ],
    [
      'activity row with no kv tail renders extras as the whole meta',
      { type: 'activity', message: 'Backup failed', meta: { job: 'USB' }, jobName: 'USB' },
      'Backup failed',
      'job=USB',
    ],
    [
      'details fields already in the message tail are not duplicated',
      { type: 'activity', message: 'Backup finished, job="USB", status=completed, items=1/1', meta: { job_name: 'USB', backup_type: 'incremental', items: '1/1' }, jobName: 'USB' },
      'Backup finished',
      'job="USB", status=completed, items=1/1, type=Incremental',
    ],
    [
      'activity row without details renders the bare tail',
      { type: 'activity', message: 'Backup finished, status=completed', meta: null, jobName: '' },
      'Backup finished',
      'status=completed',
    ],
  ])('%s', (_name, entry, wantMessage, wantMeta) => {
    const got = formatLine(entry)
    expect(got.message).toBe(wantMessage)
    expect(got.meta).toBe(wantMeta)
  })
})

describe('logLine', () => {
  it('renders the console layout with message | metadata', () => {
    const entry = {
      ts: '2026-08-21T10:45:48',
      level: 'info',
      category: 'backup',
      type: 'runlog',
      message: 'Backup finished, job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s',
    }
    expect(logLine(entry)).toBe('2026-08-21 10:45:48  info  backup  Backup finished | job="USB", status=completed, items=1/1, failed=0, size=3 GB, duration=2m15s')
  })

  it('renders a bare message without a trailing pipe', () => {
    const entry = { ts: '2026-08-21T10:45:48', level: 'info', category: 'backup', type: 'activity', message: 'Backup started' }
    expect(logLine(entry)).toBe('2026-08-21 10:45:48  info  backup  Backup started')
  })

  it('normalizes warning level and unparseable ts', () => {
    const entry = { ts: 'not-a-date', level: 'warning', category: 'system', type: 'activity', message: 'Disk low' }
    expect(logLine(entry)).toBe('--:--:--  warn  system  Disk low')
  })
})
