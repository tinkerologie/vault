import { describe, expect, it } from 'vitest'
import { failedRestoreItemNames, gatherRestorableItems, jobItemSignatures, reconcileJobState, unitNeedsDestination, pointKey, pickerMatchesPoint } from './restore-wizard-state.js'

describe('gatherRestorableItems (#669)', () => {
  const jobs = [
    { id: 1, name: 'Docker', items: [{ item_name: 'plex', item_type: 'container', settings: '{}' }],
      historical_items: [{ item_name: 'sonarr', item_type: 'container', last_backup_at: '2026-10-01T00:00:00Z' }] },
    { id: 2, name: 'Old Docker', items: [],
      historical_items: [{ item_name: 'plex', item_type: 'container', last_backup_at: '2026-09-01T00:00:00Z' }] },
  ]

  it('adds items only restore points hold, linked to the jobs that hold them', () => {
    const items = gatherRestorableItems(jobs)
    const sonarr = items.find((i) => i.name === 'sonarr')
    expect(sonarr).toMatchObject({ type: 'container', historical: true, lastBackupAt: '2026-10-01T00:00:00Z' })
    expect(sonarr.jobs.map((j) => j.id)).toEqual([1])
  })

  it('merges a historical entry into the same item still in another job', () => {
    const plex = gatherRestorableItems(jobs).find((i) => i.name === 'plex')
    expect(plex.historical).toBe(false)
    expect(plex.jobs.map((j) => j.id)).toEqual([1, 2])
    expect(plex.historicalJobIds).toEqual([2])
  })

  it('handles jobs without details', () => {
    expect(gatherRestorableItems([null, { id: 3 }])).toEqual([])
  })
})

describe('failedRestoreItemNames (#701)', () => {
  it('names every item that did not restore', () => {
    const log = JSON.stringify([
      { name: 'plex', status: 'ok' },
      { name: 'nextcloud', status: 'failed', error: 'boom' },
      { name: 'sonarr', status: 'cancelled' },
      { name: 'radarr', status: 'not_attempted' },
    ])
    expect([...failedRestoreItemNames({ log })]).toEqual(['nextcloud', 'sonarr', 'radarr'])
  })

  it('returns null when the run has no per-item results', () => {
    expect(failedRestoreItemNames({ log: 'plain text' })).toBeNull()
    expect(failedRestoreItemNames({ log: '' })).toBeNull()
    expect(failedRestoreItemNames(null)).toBeNull()
    expect(failedRestoreItemNames({ log: '{"a":1}' })).toBeNull()
  })
})

const job1 = { id: 1, name: 'Docker & Appdata' }
const job2 = { id: 2, name: 'VM Backups' }
const plex = { name: 'plex', type: 'container', jobs: [job1] }
const nextcloud = { name: 'nextcloud', type: 'container', jobs: [job1] }
const ha = { name: 'home-assistant', type: 'vm', jobs: [job2] }
const shared = { name: 'shared', type: 'folder', jobs: [job1, job2] }
const key = (i) => `${i.type}:${i.name}`

describe('jobItemSignatures', () => {
  it('records the sorted selected item keys of each job', () => {
    const sigs = jobItemSignatures([nextcloud, plex, ha, shared], key)
    expect(sigs.get(1)).toBe('container:nextcloud|container:plex|folder:shared')
    expect(sigs.get(2)).toBe('folder:shared|vm:home-assistant')
  })
})

describe('reconcileJobState', () => {
  it('keeps every job whose selected items did not change', () => {
    const before = jobItemSignatures([plex, ha], key)
    const result = reconcileJobState({ selected: [plex, ha], recorded: before, keyFn: key })
    expect(result.cleared).toEqual([])
    expect(result.dropped).toEqual([])
    expect(result.kept.sort()).toEqual([1, 2])
  })

  it('clears only the job whose item set changed', () => {
    const before = jobItemSignatures([plex, ha], key)
    const result = reconcileJobState({ selected: [plex, nextcloud, ha], recorded: before, keyFn: key })
    expect(result.cleared).toEqual([1])
    expect(result.kept).toEqual([2])
  })

  it('drops jobs that no longer hold any selected item', () => {
    const before = jobItemSignatures([plex, ha], key)
    const result = reconcileJobState({ selected: [plex], recorded: before, keyFn: key })
    expect(result.dropped).toEqual([2])
    expect(result.cleared).toEqual([])
    expect(result.kept).toEqual([1])
  })

  it('treats jobs never recorded as new rather than cleared', () => {
    const result = reconcileJobState({ selected: [plex, ha], recorded: new Map([[1, 'container:plex']]), keyFn: key })
    expect(result.added).toEqual([2])
    expect(result.cleared).toEqual([])
  })
})

describe('unitNeedsDestination (#669)', () => {
  const folder = (historicalJobIds) => ({ type: 'folder', name: 'share', historicalJobIds })
  it('asks for a destination for a removed folder or ZFS item from this job', () => {
    expect(unitNeedsDestination({ jobId: 4, items: [folder([4])] })).toBe(true)
    expect(unitNeedsDestination({ jobId: 4, items: [{ type: 'zfs', name: 'pool/data', historicalJobIds: [4] }] })).toBe(true)
  })
  it('not when the item is still in this job, or for containers and VMs', () => {
    expect(unitNeedsDestination({ jobId: 4, items: [folder([7])] })).toBe(false)
    expect(unitNeedsDestination({ jobId: 4, items: [folder([])] })).toBe(false)
    expect(unitNeedsDestination({ jobId: 4, items: [{ type: 'container', name: 'plex', historicalJobIds: [4] }] })).toBe(false)
    expect(unitNeedsDestination({ jobId: 4, items: [{ type: 'folder', name: 'x' }] })).toBe(false)
    expect(unitNeedsDestination(null)).toBe(false)
  })
})

describe('picker file lists follow their restore point', () => {
  it('keys a point by job and id', () => {
    expect(pointKey({ jobId: 2, id: 9 })).toBe('2:9')
    expect(pointKey(null)).toBe('')
  })
  it('matches only the point the list was loaded from', () => {
    const entry = { model: {}, pointKey: '1:5' }
    expect(pickerMatchesPoint(entry, { jobId: 1, id: 5 })).toBe(true)
    expect(pickerMatchesPoint(entry, { jobId: 2, id: 8 })).toBe(false)
    expect(pickerMatchesPoint(entry, null)).toBe(false)
    expect(pickerMatchesPoint({ model: null }, { jobId: 1, id: 5 })).toBe(false)
  })
})
