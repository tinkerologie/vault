import { describe, it, expect } from 'vitest'
import {
  REASON_NOT_IN_JOB,
  REASON_NOT_IN_POINT,
  STATUS_LEGACY_NOT_RECORDED,
  parsePointMetadata,
  getPointMembership,
  getPointCoverage,
  assignItemsToChosenPoints,
  defaultItemKey,
  buildRestoreUnits,
  isPlanComplete,
} from './restore-plan.js'

describe('restore-plan module', () => {
  it('exports expected reason and status constants', () => {
    expect(REASON_NOT_IN_JOB).toBe('item is not in this job')
    expect(REASON_NOT_IN_POINT).toBe('this point did not capture the item')
    expect(STATUS_LEGACY_NOT_RECORDED).toBe('Item list not recorded for this backup')
  })

  describe('parsePointMetadata & getPointMembership', () => {
    it('parses JSON string metadata', () => {
      const meta = parsePointMetadata('{"item_sizes":{"c1":100}}')
      expect(meta).toEqual({ item_sizes: { c1: 100 } })
    })

    it('handles empty or malformed metadata gracefully', () => {
      expect(parsePointMetadata(null)).toEqual({})
      expect(parsePointMetadata(undefined)).toEqual({})
      expect(parsePointMetadata('')).toEqual({})
      expect(parsePointMetadata('{badjson')).toEqual({})
    })

    it('identifies known membership from item_sizes or item_manifests', () => {
      const pt1 = { metadata: JSON.stringify({ item_sizes: { app1: 50, app2: 60 } }) }
      const mem1 = getPointMembership(pt1)
      expect(mem1.known).toBe(true)
      expect(mem1.itemNames.has('app1')).toBe(true)
      expect(mem1.itemNames.has('app2')).toBe(true)

      const pt2 = { metadata: JSON.stringify({ item_manifests: { app3: 'manifest3' } }) }
      const mem2 = getPointMembership(pt2)
      expect(mem2.known).toBe(true)
      expect(mem2.itemNames.has('app3')).toBe(true)
    })

    it('identifies unknown/legacy membership when no item_sizes or manifests are present', () => {
      const ptLegacy = { metadata: JSON.stringify({ notes: 'legacy run' }) }
      const mem = getPointMembership(ptLegacy)
      expect(mem.known).toBe(false)
      expect(mem.itemNames.size).toBe(0)
    })
  })

  describe('getPointCoverage', () => {
    const job1 = { id: 1, name: 'Job 1' }
    const job2 = { id: 2, name: 'Job 2' }

    const itemPlex = { name: 'plex', type: 'container', jobs: [job1] }
    const itemSonarr = { name: 'sonarr', type: 'container', jobs: [job1] }
    const itemVM = { name: 'ha-vm', type: 'vm', jobs: [job2] }

    it('reproduces cross-job coverage: items not in job receive REASON_NOT_IN_JOB', () => {
      const rpJob1 = {
        id: 101,
        jobId: 1,
        metadata: JSON.stringify({ item_sizes: { plex: 500, sonarr: 200 } }),
      }

      const coverage1 = getPointCoverage(rpJob1, [itemPlex, itemSonarr, itemVM])
      expect(coverage1.coversAll).toBe(false)
      expect(coverage1.coverageCount).toBe(2)
      expect(coverage1.coveredNames).toEqual(['plex', 'sonarr'])
      expect(coverage1.missingItems).toHaveLength(1)
      expect(coverage1.missingItems[0]).toEqual({
        item: itemVM,
        name: 'ha-vm',
        reason: REASON_NOT_IN_JOB,
        reasonCode: 'not_in_job',
      })

      const rpJob2 = {
        id: 201,
        jobId: 2,
        metadata: JSON.stringify({ item_sizes: { 'ha-vm': 2048 } }),
      }

      const coverage2 = getPointCoverage(rpJob2, [itemPlex, itemSonarr, itemVM])
      expect(coverage2.coversAll).toBe(false)
      expect(coverage2.coverageCount).toBe(1)
      expect(coverage2.coveredNames).toEqual(['ha-vm'])
      expect(coverage2.missingItems).toHaveLength(2)
      expect(coverage2.missingItems[0].reason).toBe(REASON_NOT_IN_JOB)
      expect(coverage2.missingItems[1].reason).toBe(REASON_NOT_IN_JOB)
    })

    it('flags partial coverage when item belongs to job but point did not capture it', () => {
      const itemRadarr = { name: 'radarr', type: 'container', jobs: [job1] }
      // rp only captured plex and sonarr; radarr was added later
      const rpPartial = {
        id: 102,
        jobId: 1,
        metadata: JSON.stringify({ item_sizes: { plex: 500, sonarr: 200 } }),
      }

      const coverage = getPointCoverage(rpPartial, [itemPlex, itemRadarr])
      expect(coverage.coveredNames).toEqual(['plex'])
      expect(coverage.missingItems).toHaveLength(1)
      expect(coverage.missingItems[0]).toEqual({
        item: itemRadarr,
        name: 'radarr',
        reason: REASON_NOT_IN_POINT,
        reasonCode: 'not_in_point',
      })
    })

    it('treats legacy points (no recorded membership) as covering items in its job', () => {
      const rpLegacy = {
        id: 103,
        jobId: 1,
        metadata: JSON.stringify({ note: 'no item sizes' }),
      }

      const coverage = getPointCoverage(rpLegacy, [itemPlex, itemSonarr, itemVM])
      expect(coverage.isLegacy).toBe(true)
      // plex and sonarr belong to job 1, so they are covered
      expect(coverage.coveredNames).toEqual(['plex', 'sonarr'])
      // ha-vm is not in job 1, so it is missing
      expect(coverage.missingItems).toHaveLength(1)
      expect(coverage.missingItems[0].name).toBe('ha-vm')
      expect(coverage.missingItems[0].reason).toBe(REASON_NOT_IN_JOB)
    })
  })

  describe('assignItemsToChosenPoints', () => {
    const job1 = { id: 1, name: 'Job 1' }
    const job2 = { id: 2, name: 'Job 2' }

    const itemPlex = { name: 'plex', type: 'container', jobs: [job1] }
    const itemSonarr = { name: 'sonarr', type: 'container', jobs: [job1] }
    const itemVM = { name: 'ha-vm', type: 'vm', jobs: [job2] }

    const rpJob1 = {
      id: 101,
      jobId: 1,
      jobName: 'Job 1',
      created_at: '2026-09-01T10:00:00Z',
      metadata: JSON.stringify({ item_sizes: { plex: 500, sonarr: 200 } }),
    }

    const rpJob2 = {
      id: 201,
      jobId: 2,
      jobName: 'Job 2',
      created_at: '2026-09-01T11:00:00Z',
      metadata: JSON.stringify({ item_sizes: { 'ha-vm': 2048 } }),
    }

    it('assigns items across jobs to their respective chosen points and marks plan complete', () => {
      const chosen = new Map([
        [1, rpJob1],
        [2, rpJob2],
      ])

      const result = assignItemsToChosenPoints([itemPlex, itemSonarr, itemVM], chosen)
      expect(result.isComplete).toBe(true)
      expect(result.uncovered).toHaveLength(0)
      expect(result.units).toHaveLength(2)

      const unit1 = result.units.find(u => u.jobId === 1)
      expect(unit1.items.map(i => i.name)).toEqual(['plex', 'sonarr'])
      expect(unit1.point.id).toBe(101)

      const unit2 = result.units.find(u => u.jobId === 2)
      expect(unit2.items.map(i => i.name)).toEqual(['ha-vm'])
      expect(unit2.point.id).toBe(201)
    })

    it('reports uncovered items if chosen points do not cover all selected items', () => {
      // Only Job 1 point chosen
      const chosen = new Map([[1, rpJob1]])
      const result = assignItemsToChosenPoints([itemPlex, itemSonarr, itemVM], chosen)

      expect(result.isComplete).toBe(false)
      expect(result.uncovered).toHaveLength(1)
      expect(result.uncovered[0].name).toBe('ha-vm')
      expect(result.units).toHaveLength(1)
    })

    it('resolves shared items by assigning to the newest chosen point', () => {
      const itemShared = { name: 'shared-data', type: 'folder', jobs: [job1, job2] }

      const rp1 = {
        id: 105,
        jobId: 1,
        jobName: 'Job 1',
        created_at: '2026-09-01T08:00:00Z',
        metadata: JSON.stringify({ item_sizes: { 'shared-data': 100 } }),
      }
      const rp2 = {
        id: 205,
        jobId: 2,
        jobName: 'Job 2',
        created_at: '2026-09-01T12:00:00Z', // newer!
        metadata: JSON.stringify({ item_sizes: { 'shared-data': 100 } }),
      }

      const chosen = new Map([
        [1, rp1],
        [2, rp2],
      ])

      const result = assignItemsToChosenPoints([itemShared], chosen)
      expect(result.isComplete).toBe(true)
      expect(result.units).toHaveLength(1)
      expect(result.units[0].jobId).toBe(2)
      expect(result.units[0].point.id).toBe(205)
    })

    it('works cleanly for single-job selections as a one-unit plan', () => {
      const chosen = new Map([[1, rpJob1]])
      const result = assignItemsToChosenPoints([itemPlex, itemSonarr], chosen)

      expect(result.isComplete).toBe(true)
      expect(result.uncovered).toHaveLength(0)
      expect(result.units).toHaveLength(1)
      expect(result.units[0].jobId).toBe(1)
      expect(result.units[0].items.map(i => i.name)).toEqual(['plex', 'sonarr'])
    })

    it('supports custom keyFn to match consumer key conventions', () => {
      const chosen = new Map([[1, rpJob1]])
      const customKeyFn = item => `custom:${item.name}`
      const result = assignItemsToChosenPoints([itemPlex], chosen, customKeyFn)
      expect(result.assignments.has('custom:plex')).toBe(true)
      expect(result.assignments.get('custom:plex').item).toBe(itemPlex)
    })

    it('supports chosenPoints as Array or plain object and normalizes job_id', () => {
      const arrayChosen = [{ job_id: 1, ...rpJob1 }]
      const resArray = assignItemsToChosenPoints([itemPlex], arrayChosen)
      expect(resArray.isComplete).toBe(true)
      expect(resArray.units[0].jobId).toBe(1)

      const objectChosen = { 1: { job_id: 1, ...rpJob1 } }
      const resObj = assignItemsToChosenPoints([itemPlex], objectChosen)
      expect(resObj.isComplete).toBe(true)
      expect(resObj.units[0].jobId).toBe(1)
    })

    it('defaultItemKey formats type:name or falls back safely', () => {
      expect(defaultItemKey({ type: 'container', name: 'app' })).toBe('container:app')
      expect(defaultItemKey({ item_type: 'folder', item_name: 'media' })).toBe('folder:media')
      expect(defaultItemKey(null)).toBe(':')
    })

    it('parses metadata when passed directly as an object or manifests-only', () => {
      const rawObj = { item_manifests: { itemX: 'hash' } }
      expect(parsePointMetadata(rawObj)).toBe(rawObj)
      const mem = getPointMembership({ metadata: rawObj })
      expect(mem.known).toBe(true)
      expect(mem.itemNames.has('itemX')).toBe(true)
      expect(getPointMembership(null).known).toBe(false)
    })

    it('supports helper functions buildRestoreUnits and isPlanComplete', () => {
      const chosen = new Map([[1, rpJob1], [2, rpJob2]])
      const units = buildRestoreUnits([itemPlex, itemVM], chosen)
      expect(units).toHaveLength(2)
      expect(isPlanComplete([itemPlex, itemVM], chosen)).toBe(true)
      expect(isPlanComplete([itemPlex, { name: 'missing', jobs: [] }], chosen)).toBe(false)
    })
  })
})
