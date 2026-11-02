import { describe, it, expect } from 'vitest'
import { splitMessageMeta, formatLine, logLine, parseInlineMeta, problemFields, problemSummary, problemFoldKey, rowDetailsLabel, rowStamp } from './logline.js'

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

describe('parseInlineMeta', () => {
  it.each([
    ['nothing', '', {}],
    ['null', null, {}],
    ['plain pairs', 'container=plex, status=ok', { container: 'plex', status: 'ok' }],
    ['quoted with an escaped quote', 'job="a \\"b\\", c", status=x', { job: 'a "b", c', status: 'x' }],
    ['unquoted value with a comma', 'container=a, b, status=x', { container: 'a, b', status: 'x' }],
    ['error keeps the rest of the tail', 'status=failed, error=dial tcp, refused, code=7', { status: 'failed', error: 'dial tcp, refused, code=7' }],
    ['a wholly quoted error is unquoted', 'error="bad, thing"', { error: 'bad, thing' }],
    ['a partly quoted error stays verbatim', 'error="bad" and, x=1', { error: '"bad" and, x=1' }],
    ['empty value is absent', 'status=, container=plex', { container: 'plex' }],
    ['first occurrence wins', 'status=a, status=b', { status: 'a' }],
    ['stops at text that is not a pair', 'not a pair', {}],
  ])('%s', (_name, meta, want) => {
    expect(parseInlineMeta(meta)).toEqual(want)
  })
})

describe('problemFields', () => {
  it.each([
    ['inline container', { message: 'Health check, container=plex, status=unhealthy, error=Timed out' }, { item: 'plex', status: 'unhealthy', error: 'Timed out' }],
    ['inline destination', { message: 'Storage health check, destination=NAS, status=failed' }, { item: 'NAS', status: 'failed', error: '' }],
    ['inline job, quoted', { message: 'Backup finished, job="USB", status=failed' }, { item: 'USB', status: 'failed', error: '' }],
    ['structured wins over inline', { message: 'Health check, container=a, status=b', meta: { container_name: 'x', status: 'y' } }, { item: 'x', status: 'y', error: '' }],
    ['structured destination id', { message: 'Storage health check', meta: { storage_dest_id: 3 } }, { item: '3', status: '', error: '' }],
    ['non-scalar and non-object details are ignored', { message: 'x', meta: { container_name: {}, status: '' } }, { item: '', status: '', error: '' }],
    ['a details string is ignored', { message: 'x', meta: 'text' }, { item: '', status: '', error: '' }],
  ])('%s', (_name, entry, want) => {
    expect(problemFields(entry)).toEqual(want)
  })

  it('never takes error text from structured details, which Details does not show', () => {
    const entry = { level: 'warn', type: 'activity', message: 'Post-script failed: job', meta: { error: 'SENTINEL', message: 'SENTINEL', script: 'SENTINEL', status: 'failed' } }
    expect(problemFields(entry).error).toBe('')
    expect(problemSummary(entry)).toBe('Post-script failed: job · failed')
  })
})

describe('problemSummary', () => {
  const summary = (level, message, extra = {}) => problemSummary({ level, message, type: 'activity', ...extra })

  it.each(['info', 'debug', 'success', '', undefined])('is null for level %s', (level) => {
    expect(summary(level, 'Health check, container=plex, status=ok')).toBeNull()
  })

  it.each(['warn', 'warning', 'error'])('joins item, status and error for level %s', (level) => {
    expect(summary(level, 'Health check, container=plex, status=unhealthy, error=Timed out, retrying'))
      .toBe('Health check · plex · unhealthy · Timed out, retrying')
  })

  it('leaves out what is missing', () => {
    expect(summary('warn', 'Health check, status=failed')).toBe('Health check · failed')
    expect(summary('warn', 'Health check, container=plex')).toBe('Health check · plex')
  })

  it('keeps the whole tail when no field is recognised', () => {
    expect(summary('warn', 'Failed: plex (container): boom, item=1/3')).toBe('Failed: plex (container): boom · item=1/3')
    expect(summary('warn', 'Backup failed', { meta: { job: 'USB' } })).toBe('Backup failed · job=USB')
  })

  it('leaves a bare message unchanged', () => {
    expect(summary('error', 'Storage capacity above 75%')).toBe('Storage capacity above 75%')
  })
})

describe('problemFoldKey', () => {
  it('is empty for routine rows and separates item from status otherwise', () => {
    expect(problemFoldKey({ level: 'info', message: 'x, container=a' })).toBe('')
    const key = (message) => problemFoldKey({ level: 'warn', message })
    expect(key('x, container=a, status=b')).not.toBe(key('x, container=ab, status='))
  })

  it('keeps different causes apart, since a fold shows only one (#534)', () => {
    const key = (message) => problemFoldKey({ level: 'warn', message })
    expect(key('x, container=a, status=b, error=one')).not.toBe(key('x, container=a, status=b, error=two'))
    expect(key('x, container=a, status=b, error=one')).toBe(key('x, container=a, status=b, error=one'))
    expect(key('Upload failed, file=a.tar, error=disk full')).not.toBe(key('Upload failed, file=b.tar, error=permission denied'))
    expect(key('Upload failed, file=a.tar, error=disk full')).toBe(key('Upload failed, file=a.tar, error=disk full'))
  })
})

describe('copied and exported text', () => {
  it('stays byte-identical for problem lines', () => {
    const entry = { ts: '2026-08-21T10:45:48', level: 'warning', category: 'health', type: 'activity', message: 'Health check, container=plex, status=unhealthy, error=Timed out, retrying', meta: { container_name: 'plex', status: 'unhealthy' } }
    expect(formatLine(entry)).toEqual({ message: 'Health check', meta: 'container=plex, status=unhealthy, error=Timed out, retrying' })
    expect(logLine(entry)).toBe('2026-08-21 10:45:48  warn  health  Health check | container=plex, status=unhealthy, error=Timed out, retrying')
  })
})

describe('rowDetailsLabel', () => {
  const entry = (message, extra = {}) => ({ level: 'warn', type: 'activity', message, ...extra })

  it('tells apart rows that share a message', () => {
    const plex = rowDetailsLabel(entry('Health check, container=plex, status=unhealthy'), '10:00:01')
    const sonarr = rowDetailsLabel(entry('Health check, container=sonarr, status=unhealthy'), '10:00:01')
    const later = rowDetailsLabel(entry('Health check, container=plex, status=unhealthy'), '10:00:02')
    expect(plex).toBe('Details for Health check · plex, 10:00:01')
    expect(new Set([plex, sonarr, later]).size).toBe(3)
  })

  it('tells apart rows at the same time on different days', () => {
    const first = rowStamp('2026-11-01T10:00:01', '10:00:01')
    const second = rowStamp('2026-11-02T10:00:01', '10:00:01')
    expect(first).not.toBe(second)
    expect(first).toContain('10:00:01')
    expect(rowStamp('not a date', '10:00:01')).toBe('10:00:01')
  })

  it('counts a fold, and copes with no item or time', () => {
    expect(rowDetailsLabel(entry('Tick'), '10:00:01', 3)).toBe('Details for Tick, 10:00:01, 3 entries')
    expect(rowDetailsLabel(entry('Tick'), '')).toBe('Details for Tick')
  })
})
