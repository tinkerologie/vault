import { describe, it, expect, beforeEach } from 'vitest'
import {
  getAnomalies,
  setOpenList,
  handleAnomalyRaised,
  handleAnomalyUpdated,
  handleBulkResolved,
  handleBulkAcked,
  onBaselineUpdated,
  notifyBaselineUpdated,
  getAnomalyCounts,
  formatAnomalyTooltip,
} from './anomalies.svelte.js'

describe('anomalies.svelte.js', () => {
  beforeEach(() => {
    setOpenList([])
  })

  describe('getAnomalyCounts', () => {
    it('returns zeroes when openList is empty', () => {
      const counts = getAnomalyCounts()
      expect(counts).toEqual({ critical: 0, warning: 0, info: 0 })
    })

    it('counts mixed severities present at once', () => {
      setOpenList([
        { id: 1, severity: 'critical', scope_kind: 'job', scope_id: 10 },
        { id: 2, severity: 'critical', scope_kind: 'job', scope_id: 10 },
        { id: 3, severity: 'warning', scope_kind: 'job', scope_id: 10 },
        { id: 4, severity: 'info', scope_kind: 'job', scope_id: 10 },
      ])

      const counts = getAnomalyCounts()
      expect(counts).toEqual({ critical: 2, warning: 1, info: 1 })
    })

    it('counts when only one severity is present', () => {
      setOpenList([
        { id: 1, severity: 'warning', scope_kind: 'job', scope_id: 10 },
        { id: 2, severity: 'warning', scope_kind: 'job', scope_id: 10 },
      ])

      const counts = getAnomalyCounts()
      expect(counts).toEqual({ critical: 0, warning: 2, info: 0 })
    })

    it('returns zeroes when no anomalies match the filter', () => {
      setOpenList([
        { id: 1, severity: 'critical', scope_kind: 'job', scope_id: 10 },
        { id: 2, severity: 'warning', scope_kind: 'job', scope_id: 10 },
      ])

      const counts = getAnomalyCounts({ scope_kind: 'job', scope_id: 99 })
      expect(counts).toEqual({ critical: 0, warning: 0, info: 0 })
    })

    it('filters correctly by job scope descriptor', () => {
      setOpenList([
        { id: 1, severity: 'critical', scope_kind: 'job', scope_id: 1 },
        { id: 2, severity: 'warning', scope_kind: 'job', scope_id: 1 },
        { id: 3, severity: 'critical', scope_kind: 'job', scope_id: 2 },
        { id: 4, severity: 'info', scope_kind: 'job', scope_id: 2 },
        { id: 5, severity: 'warning', scope_kind: 'container', scope_id: 1 },
      ])

      const job1Counts = getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })
      expect(job1Counts).toEqual({ critical: 1, warning: 1, info: 0 })

      const job2Counts = getAnomalyCounts({ kind: 'job', id: 2 })
      expect(job2Counts).toEqual({ critical: 1, warning: 0, info: 1 })

      const containerCounts = getAnomalyCounts({ scope_kind: 'container', scope_id: 1 })
      expect(containerCounts).toEqual({ critical: 0, warning: 1, info: 0 })
    })

    it('filters correctly by run ID descriptor', () => {
      setOpenList([
        { id: 1, severity: 'critical', job_run_id: 100 },
        { id: 2, severity: 'warning', job_run_id: 100 },
        { id: 3, severity: 'info', job_run_id: 100 },
        { id: 4, severity: 'critical', job_run_id: 200 },
      ])

      const run100Counts = getAnomalyCounts({ job_run_id: 100 })
      expect(run100Counts).toEqual({ critical: 1, warning: 1, info: 1 })

      const run200Counts = getAnomalyCounts({ runId: 200 })
      expect(run200Counts).toEqual({ critical: 1, warning: 0, info: 0 })
    })

    it('supports custom predicate functions', () => {
      setOpenList([
        { id: 1, severity: 'critical', scope_kind: 'job', scope_id: 5, job_run_id: 10 },
        { id: 2, severity: 'warning', scope_kind: 'job', scope_id: 5, job_run_id: 11 },
        { id: 3, severity: 'info', scope_kind: 'job', scope_id: 6, job_run_id: 12 },
      ])

      const customCounts = getAnomalyCounts(a => a.scope_kind === 'job' && a.scope_id === 5)
      expect(customCounts).toEqual({ critical: 1, warning: 1, info: 0 })
    })

    it('supports customList parameter without touching openList', () => {
      const custom = [
        { id: 10, severity: 'critical', scope_kind: 'job', scope_id: 1 },
        { id: 11, severity: 'info', scope_kind: 'job', scope_id: 1 },
      ]

      const counts = getAnomalyCounts({ scope_kind: 'job', scope_id: 1 }, custom)
      expect(counts).toEqual({ critical: 1, warning: 0, info: 1 })
      expect(getAnomalies().openList).toEqual([])
    })

    it('ignores items with unknown or missing severity', () => {
      setOpenList([
        { id: 1, severity: 'invalid', scope_kind: 'job', scope_id: 1 },
        { id: 2, severity: undefined, scope_kind: 'job', scope_id: 1 },
        { id: 3, severity: 'critical', scope_kind: 'job', scope_id: 1 },
        null,
      ])

      const counts = getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })
      expect(counts).toEqual({ critical: 1, warning: 0, info: 0 })
    })

    it('handles non-array inputs gracefully', () => {
      const counts = getAnomalyCounts(null, null)
      expect(counts).toEqual({ critical: 0, warning: 0, info: 0 })
    })
  })

  describe('reactive mutators update getAnomalyCounts', () => {
    it('updates counts when handleAnomalyRaised is called', () => {
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 0, info: 0 })

      handleAnomalyRaised({ id: 101, severity: 'warning', scope_kind: 'job', scope_id: 1 })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 1, info: 0 })

      // Duplicate should be ignored
      handleAnomalyRaised({ id: 101, severity: 'warning', scope_kind: 'job', scope_id: 1 })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 1, info: 0 })
    })

    it('updates counts when handleAnomalyUpdated updates severity or resolves', () => {
      handleAnomalyRaised({ id: 101, severity: 'warning', scope_kind: 'job', scope_id: 1, state: 'open' })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 1, info: 0 })

      // Escalated to critical
      handleAnomalyUpdated({ id: 101, severity: 'critical', scope_kind: 'job', scope_id: 1, state: 'open' })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 1, warning: 0, info: 0 })

      // Resolved (terminal state) removes it from open list
      handleAnomalyUpdated({ id: 101, severity: 'critical', scope_kind: 'job', scope_id: 1, state: 'resolved' })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 0, info: 0 })
    })

    it('updates counts when handleBulkAcked removes acknowledged anomalies', () => {
      setOpenList([
        { id: 1, severity: 'critical', scope_kind: 'job', scope_id: 1 },
        { id: 2, severity: 'warning', scope_kind: 'job', scope_id: 1 },
        { id: 3, severity: 'info', scope_kind: 'job', scope_id: 1 },
      ])

      handleBulkAcked({ ids: [1, 3] })
      expect(getAnomalyCounts({ scope_kind: 'job', scope_id: 1 })).toEqual({ critical: 0, warning: 1, info: 0 })
    })
  })

  describe('formatAnomalyTooltip', () => {
    it('formats multiple severities correctly', () => {
      const text = formatAnomalyTooltip({ critical: 2, warning: 1, info: 3 })
      expect(text).toContain('Open anomalies (2 critical, 1 warning, 3 info)')
      expect(text).toContain('Review on the Anomalies page')
    })

    it('formats single severity correctly', () => {
      const text = formatAnomalyTooltip({ critical: 1, warning: 0, info: 0 })
      expect(text).toContain('Open anomalies (1 critical)')
    })

    it('formats empty / zero counts gracefully', () => {
      const text = formatAnomalyTooltip({ critical: 0, warning: 0, info: 0 })
      expect(text).toContain('Open anomalies (0 anomalies)')
    })

    it('handles undefined or null counts gracefully', () => {
      const text = formatAnomalyTooltip(null)
      expect(text).toContain('Open anomalies (0 anomalies)')
    })
  })

  describe('baseline and bulk listeners', () => {
    beforeEach(() => {
      setOpenList([])
    })

    it('handles anomaly.bulk_resolved without throwing', () => {
      expect(() => handleBulkResolved()).not.toThrow()
    })

    it('handles anomaly.updated when item was not previously in list', () => {
      handleAnomalyUpdated({ id: 999, severity: 'info', state: 'open' })
      expect(getAnomalies().openList).toHaveLength(1)
      expect(getAnomalies().openList[0].id).toBe(999)
    })

    it('handles null anomaly gracefully in handlers', () => {
      handleAnomalyRaised(null)
      handleAnomalyUpdated(null)
      handleBulkAcked(null)
      handleBulkAcked({ ids: [] })
      expect(getAnomalies().openList).toHaveLength(0)
    })

    it('registers and triggers onBaselineUpdated listeners', () => {
      let calledWith = null
      const unsubscribe = onBaselineUpdated((data) => {
        calledWith = data
      })

      notifyBaselineUpdated({ sample_count: 5 })
      expect(calledWith).toEqual({ sample_count: 5 })

      // Test unsubscribe
      unsubscribe()
      notifyBaselineUpdated({ sample_count: 6 })
      expect(calledWith).toEqual({ sample_count: 5 })
    })
  })
})
