import { describe, it, expect, vi, afterEach } from 'vitest'
import { getProgress, markJobActiveOptimistically, handleProgressMessage, restoreFromStatus, verifyRun, clearActiveRun } from './progress.svelte.js'

afterEach(() => { clearActiveRun(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

// The store keeps module-level $state; tests share one instance, so each case
// drives the store into the precondition it needs before asserting.
describe('markJobActiveOptimistically', () => {
  const cases = [
    {
      name: 'marks the clicked job active with running=true and no run_id',
      pre: () => {},
      act: () => markJobActiveOptimistically(5, 'plex'),
      assert: (p) => p.running === true && p.activeRun?.job_id === 5 && p.activeRun?.run_id === null && p.activeRun?.job_name === 'plex',
    },
    {
      name: 'defaults the job name to "Job #<id>" when not provided',
      pre: () => { handleProgressMessage({ type: 'job_run_completed', job_id: 5, run_id: null }) },
      act: () => markJobActiveOptimistically(7),
      assert: (p) => p.activeRun?.job_name === 'Job #7',
    },
    {
      name: 'does not clobber an already-live run',
      pre: () => {
        handleProgressMessage({ type: 'job_run_completed', job_id: 5, run_id: null })
        markJobActiveOptimistically(5, 'plex')
      },
      act: () => markJobActiveOptimistically(9, 'other'),
      assert: (p) => p.activeRun?.job_id === 5,
    },
    {
      name: 'a real job_run_started event replaces the optimistic placeholder',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      act: () => handleProgressMessage({ type: 'job_run_started', job_id: 5, run_id: 42, job_name: 'plex', items_total: 3 }),
      assert: (p) => p.activeRun?.run_id === 42 && p.activeRun?.job_id === 5 && p.overallTotal === 3,
    },
    {
      name: 'job_run_completed clears running even after an optimistic start',
      pre: () => { markJobActiveOptimistically(5, 'plex'); handleProgressMessage({ type: 'job_run_started', job_id: 5, run_id: 42, job_name: 'plex' }) },
      act: () => handleProgressMessage({ type: 'job_run_completed', job_id: 5, run_id: 42 }),
      assert: (p) => p.running === false,
    },
    {
      // The v003 regression: a completion whose run_id was never learned
      // (job_run_started dropped by the lossy hub) must still clear the flag —
      // matching on job_id when the placeholder has no run_id.
      name: 'completion with unknown run_id still clears a placeholder for the same job',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      act: () => handleProgressMessage({ type: 'job_run_completed', job_id: 5, run_id: 42 }),
      assert: (p) => p.running === false,
    },
    {
      name: 'completion for a different job does not clear a real run',
      pre: () => { handleProgressMessage({ type: 'job_run_started', job_id: 5, run_id: 42, job_name: 'plex' }) },
      act: () => handleProgressMessage({ type: 'job_run_completed', job_id: 9, run_id: 77 }),
      assert: (p) => p.running === true,
    },
    {
      name: 'completion with a stale run_id does not clear a newer real run',
      pre: () => { handleProgressMessage({ type: 'job_run_started', job_id: 5, run_id: 100, job_name: 'plex' }) },
      act: () => handleProgressMessage({ type: 'job_run_completed', job_id: 5, run_id: 42 }),
      assert: (p) => p.running === true,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      c.pre()
      c.act(getProgress())
      expect(c.assert(getProgress())).toBe(true)
    })
  }
})

describe('restoreFromStatus adopts an optimistic placeholder', () => {
  const cases = [
    {
      name: 'overwrites a placeholder (run_id null) with the real run',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      act: () => restoreFromStatus({ active: true, job_id: 5, run_id: 42, job_name: 'plex', items_total: 3 }),
      assert: (p) => p.activeRun?.run_id === 42 && p.activeRun?.job_id === 5 && p.overallTotal === 3 && p.running === true,
    },
    {
      name: 'does not overwrite a real run (run_id set) with a stale snapshot',
      pre: () => { handleProgressMessage({ type: 'job_run_started', job_id: 5, run_id: 100, job_name: 'plex' }) },
      act: () => restoreFromStatus({ active: true, job_id: 5, run_id: 42, job_name: 'plex' }),
      assert: (p) => p.activeRun?.run_id === 100,
    },
    {
      name: 'ignores an inactive status',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      act: () => restoreFromStatus({ active: false }),
      assert: (p) => p.activeRun?.run_id === null && p.running === true,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      c.pre()
      c.act()
      expect(c.assert(getProgress())).toBe(true)
    })
  }
})

describe('verifyRun (watchdog)', () => {
  const cases = [
    {
      name: 'clears state when the server reports nothing active',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      fetchResult: { ok: true, json: async () => ({ active: false }) },
      assert: (p) => p.running === false && p.activeRun === null,
    },
    {
      name: 'adopts the real run when still active but placeholder is unresolved',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      fetchResult: { ok: true, json: async () => ({ active: true, job_id: 5, run_id: 42, job_name: 'plex' }) },
      assert: (p) => p.activeRun?.run_id === 42 && p.running === true,
    },
    {
      // R1 regression: a different job active on the server must NOT be
      // misattributed to the unresolved placeholder — clear it instead.
      name: 'clears the placeholder when the active run belongs to a different job',
      pre: () => { markJobActiveOptimistically(5, 'plex') },
      fetchResult: { ok: true, json: async () => ({ active: true, job_id: 9, run_id: 77, job_name: 'other' }) },
      assert: (p) => p.running === false && p.activeRun === null,
    },
    {
      name: 'does nothing when already idle',
      pre: () => {},
      fetchResult: { ok: true, json: async () => ({ active: false }) },
      assert: (p) => p.running === false && p.activeRun === null,
    },
  ]

  for (const c of cases) {
    it(c.name, async () => {
      c.pre()
      vi.stubGlobal('fetch', async () => c.fetchResult)
      await verifyRun()
      expect(c.assert(getProgress())).toBe(true)
    })
  }
})
