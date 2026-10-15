import { describe, it, expect } from 'vitest'
import { detailFrom, phaseLabel, describeItemProgress, STALL_HINT_SECONDS } from './item-progress.js'

const GB = 1024 ** 3

describe('detailFrom', () => {
  it('returns null without a phase', () => {
    expect(detailFrom(null)).toBeNull()
    expect(detailFrom({ percent: 40 })).toBeNull()
  })
  it('maps status snapshot fields', () => {
    const d = detailFrom({
      phase: 'transferring', bytes_done: 5, bytes_total: 10, files_done: 1, files_total: 2,
      rate_bytes_per_sec: 3, eta_seconds: 9, progress_determinate: true, current_item_percent: 50,
      phase_started_at: '2026-10-15T00:00:00Z', last_progress_at: '2026-10-15T00:01:00Z',
    })
    expect(d).toMatchObject({ phase: 'transferring', bytesDone: 5, bytesTotal: 10, filesDone: 1, filesTotal: 2, rateBytes: 3, eta: 9, determinate: true, percent: 50 })
    expect(d.lastProgressAt - d.phaseStartedAt).toBe(60000)
  })
  it('prefers the event percent and leaves indeterminate percent null', () => {
    expect(detailFrom({ phase: 'verifying', progress_determinate: true, percent: 12, current_item_percent: 99 }).percent).toBe(12)
    expect(detailFrom({ phase: 'scanning', percent: 40 }).percent).toBeNull()
  })
})

describe('phaseLabel', () => {
  it('labels by run type', () => {
    expect(phaseLabel('transferring', 'backup')).toBe('Uploading')
    expect(phaseLabel('transferring', 'restore')).toBe('Downloading')
    expect(phaseLabel('processing', 'restore')).toBe('Restoring')
    expect(phaseLabel('processing', 'backup')).toBe('Processing')
    expect(phaseLabel('scanning')).toBe('Scanning…')
    expect(phaseLabel('preparing')).toBe('Preparing…')
    expect(phaseLabel('verifying')).toBe('Verifying')
    expect(phaseLabel('finalizing')).toBe('Finalizing…')
    expect(phaseLabel('compacting')).toBe('Compacting')
    expect(phaseLabel('')).toBe('')
  })
})

describe('describeItemProgress', () => {
  const now = Date.parse('2026-10-15T01:00:00Z')
  it('renders the ticket example for a determinate upload (#456)', () => {
    const d = detailFrom({ phase: 'transferring', bytes_done: 84.2 * GB, bytes_total: 225 * GB, progress_determinate: true, current_item_percent: 37, rate_bytes_per_sec: 5 * 1024 ** 2, eta_seconds: 7200, last_progress_at: '2026-10-15T00:59:59Z' })
    const view = describeItemProgress(d, 'backup', now)
    expect(view).toEqual({ label: 'Uploading', text: '84.2 GB / 225 GB — 37%', percent: 37, rate: '5 MB/s', eta: '2h 0m left', stall: null })
  })
  it('renders an indeterminate scan with a file count and file rate', () => {
    const view = describeItemProgress(detailFrom({ phase: 'scanning', files_done: 12340, rate_files_per_sec: 410.6 }), 'backup', now)
    expect(view).toMatchObject({ label: 'Scanning…', text: '12,340 files', percent: null, rate: '411 files/s', eta: null })
  })
  it('shows files done/total and bare byte counts', () => {
    const view = describeItemProgress(detailFrom({ phase: 'verifying', bytes_done: 1024, files_done: 2, files_total: 4 }), 'backup', now)
    expect(view.text).toBe('1 KB · 2 / 4 files')
  })
  it('shows only the percent when nothing is counted yet', () => {
    expect(describeItemProgress(detailFrom({ phase: 'finalizing', progress_determinate: true, percent: 99 }), 'backup', now).text).toBe('99%')
    expect(describeItemProgress(detailFrom({ phase: 'preparing' }), 'backup', now).text).toBe('')
  })
  it('hides the ETA unless determinate', () => {
    expect(describeItemProgress(detailFrom({ phase: 'scanning', eta_seconds: 60 }), 'backup', now).eta).toBeNull()
  })
  it('flags a stall after the threshold', () => {
    const quiet = new Date(now - (STALL_HINT_SECONDS + 60) * 1000).toISOString()
    expect(describeItemProgress(detailFrom({ phase: 'transferring', last_progress_at: quiet }), 'restore', now).stall).toBe('No progress for 3m 0s')
    const recent = new Date(now - 10 * 1000).toISOString()
    expect(describeItemProgress(detailFrom({ phase: 'transferring', last_progress_at: recent }), 'restore', now).stall).toBeNull()
  })
  it('returns null without detail', () => {
    expect(describeItemProgress(null, 'backup', now)).toBeNull()
  })
})
