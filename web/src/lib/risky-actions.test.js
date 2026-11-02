import { describe, expect, it } from 'vitest'
import { PURGE_HISTORY_SUMMARY, replaceDatabaseSummary, restoreAnywaySummary } from './risky-actions.js'

const fail = { ok: false, checks: [{ label: 'Storage reachable', status: 'ok' }, { label: 'Space', status: 'fail', detail: 'disk full' }, { label: 'Archive', status: 'fail' }] }
const pass = { ok: true, checks: [{ label: 'Storage reachable', status: 'ok' }] }
const unit = (over) => ({ jobName: 'Docker', pointDate: 'Oct 1', destination: 'Original location', cleanDestination: false, preflight: fail, ...over })

describe('replaceDatabaseSummary', () => {
  it('names the storage, the file and the date', () => {
    const { source } = replaceDatabaseSummary({ storageName: 'Array', path: '_vault/vault.db', modified: 'Oct 1' })
    expect(source.map((s) => s.value)).toEqual(['Array', '_vault/vault.db', 'Oct 1'])
  })

  it('omits an unknown date', () => {
    const { source } = replaceDatabaseSummary({ storageName: 'Array', path: '_vault/vault.db' })
    expect(source.map((s) => s.label)).toEqual(['Storage', 'Backup file'])
  })

  it('states the loss without claiming a restart is always needed', () => {
    const text = replaceDatabaseSummary({ storageName: 'A', path: 'p' }).consequences.join(' ')
    expect(text).toContain('Changes made after this backup was taken are lost')
    expect(text).toContain('If reopening fails, restart Vault')
    expect(text).not.toMatch(/will need to be restarted/)
  })
})

describe('restoreAnywaySummary', () => {
  it('lists every failing check with its detail, and only the failing ones', () => {
    const { source } = restoreAnywaySummary([unit()])
    expect(source[0]).toEqual({ label: 'Failed checks: Docker', value: 'Space: disk full; Archive' })
  })

  it('has a fallback when a failed result names no failing check', () => {
    expect(restoreAnywaySummary([unit({ preflight: { ok: false, checks: [] } })]).source[0].value).toBe('Pre-flight did not pass')
    expect(restoreAnywaySummary([unit({ preflight: null })]).source[0].value).toBe('Pre-flight did not pass')
  })

  it('names the restore point and destination of every unit, but only failed jobs as failed', () => {
    const { source } = restoreAnywaySummary([unit(), unit({ jobName: 'VMs', preflight: pass, pointDate: 'Oct 2', destination: '/mnt/x' })])
    expect(source.map((s) => s.label)).toEqual(['Failed checks: Docker', 'Restoring: Docker', 'Restoring: VMs'])
    expect(source[2].value).toBe('Oct 2 to /mnt/x')
  })

  it('counts jobs in the consequence', () => {
    expect(restoreAnywaySummary([unit()]).consequences[0]).toMatch(/^The selected job/)
    expect(restoreAnywaySummary([unit(), unit({ jobName: 'VMs', preflight: pass })]).consequences[0]).toBe('All 2 selected jobs are submitted, including 1 that failed pre-flight checks.')
  })

  it('warns about clearing only when a unit clears its destination', () => {
    expect(restoreAnywaySummary([unit()]).consequences.join(' ')).not.toMatch(/removed/)
    expect(restoreAnywaySummary([unit({ cleanDestination: true }), unit({ jobName: 'VMs' })]).consequences.join(' ')).toContain('not in the backup are removed for: Docker.')
  })
})

describe('PURGE_HISTORY_SUMMARY', () => {
  it('states that the scope is everything and that restore points go too', () => {
    expect(PURGE_HISTORY_SUMMARY.source[0].value).toContain('not only the filtered rows')
    expect(PURGE_HISTORY_SUMMARY.consequences.join(' ')).toContain('All restore points are deleted')
  })
})
