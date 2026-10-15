import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  clearActiveRun,
  getProgress,
  handleProgressMessage,
  markJobActiveOptimistically,
  restoreFromStatus,
  syncFromStatus,
  verifyRun,
} from './progress.svelte.js'

// Additional progress-store coverage (issue #458): phase messages, staged /
// upload events, restore events, failures, queue and cancel handling, the
// polling sync path and the watchdog's error branches.

const p = getProgress()

beforeEach(() => {
  vi.useFakeTimers()
  clearActiveRun()
})

afterEach(() => {
  clearActiveRun()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function start(extra = {}) {
  handleProgressMessage({ type: 'job_run_started', job_id: 1, run_id: 10, job_name: 'nightly', ...extra })
}

describe('phase messages', () => {
  it('renders container stop/restart and explicit phase messages', () => {
    start()
    expect(handleProgressMessage({ type: 'containers_stopping_all', count: 3 })).toBe(true)
    expect(p.phaseMessage).toBe('Stopping 3 containers...')
    handleProgressMessage({ type: 'containers_restarting_all', count: 2 })
    expect(p.phaseMessage).toBe('Restarting 2 containers...')
    handleProgressMessage({ type: 'phase_message', message: 'Verifying' })
    expect(p.phaseMessage).toBe('Verifying')
    handleProgressMessage({ type: 'phase_message' })
    expect(p.phaseMessage).toBeNull()
  })

  it('describes deferred staging and upload phases', () => {
    start()
    const cases = [
      [{ phase: 'staging', item_name: 'plex' }, 'Staging plex locally...'],
      [{ phase: 'staging' }, 'Staging backups locally...'],
      [{ phase: 'uploading', count: 1 }, 'Uploading 1 staged item to remote storage...'],
      [{ phase: 'uploading', count: 4 }, 'Uploading 4 staged items to remote storage...'],
      [{ phase: 'uploading' }, 'Uploading to remote storage...'],
    ]
    for (const [msg, text] of cases) {
      handleProgressMessage({ type: 'backup_phase', ...msg })
      expect(p.phaseMessage).toBe(text)
    }
    // Unknown phases leave the message unchanged.
    handleProgressMessage({ type: 'backup_phase', phase: 'other' })
    expect(p.phaseMessage).toBe('Uploading to remote storage...')
  })

  it('returns false for unrelated messages', () => {
    expect(handleProgressMessage({ type: 'anomaly.raised' })).toBe(false)
  })
})

describe('staged and uploading items', () => {
  it('tracks item_staged then item_upload_start, keeping the known item type', () => {
    start()
    handleProgressMessage({ type: 'item_backup_start', item_name: 'plex', item_type: 'container' })
    handleProgressMessage({ type: 'item_staged', item_name: 'plex' })
    expect(p.currentItem).toMatchObject({ name: 'plex', item_type: 'container', percent: 50 })
    expect(p.itemProgress.plex).toMatchObject({ percent: 50, status: 'running' })
    handleProgressMessage({ type: 'item_upload_start', item_name: 'plex' })
    expect(p.currentItem).toMatchObject({ percent: 60, message: 'Uploading...', item_type: 'container' })
  })

  it('handles staged/upload events for a never-seen item', () => {
    start()
    handleProgressMessage({ type: 'item_staged', item_name: 'vm1', item_type: 'vm' })
    expect(p.currentItem.item_type).toBe('vm')
    handleProgressMessage({ type: 'item_upload_start', item_name: 'vm2' })
    expect(p.currentItem).toMatchObject({ name: 'vm2', item_type: '' })
  })

  it('synthesizes a run from an item message after a page reload', () => {
    handleProgressMessage({ type: 'item_staged', job_id: 4, run_id: 44, item_name: 'a', items_total: 3 })
    expect(p.activeRun).toMatchObject({ job_id: 4, run_id: 44, job_name: 'Job #4', run_type: 'backup' })
    expect(p.running).toBe(true)
    expect(p.overallTotal).toBe(3)
  })

  it('infers a restore run type and uses the name resolver', () => {
    handleProgressMessage({ type: 'item_restore_done', job_id: 4, run_id: 45, item_name: 'a' }, (id) => `resolved-${id}`)
    expect(p.activeRun).toMatchObject({ run_type: 'restore', job_name: 'resolved-4' })
  })
})

describe('restore items', () => {
  it('tracks restore start, done and failure counts', () => {
    start({ run_type: 'restore' })
    handleProgressMessage({ type: 'item_restore_start', item_name: 'a', item_type: 'folder', items_total: 2 })
    expect(p.overallTotal).toBe(2)
    expect(p.currentItem).toMatchObject({ name: 'a', percent: 0 })
    handleProgressMessage({ type: 'item_restore_done', item_name: 'a', items_done: 1, items_total: 2 })
    expect(p.itemProgress.a).toMatchObject({ status: 'done', message: 'Restored', percent: 100 })
    expect(p.overallDone).toBe(1)
    handleProgressMessage({ type: 'item_restore_start', item_name: 'b' })
    expect(p.currentItem.item_type).toBe('')
    handleProgressMessage({ type: 'item_restore_failed', item_name: 'b', error: 'disk full', items_done: 2, items_total: 2 })
    expect(p.itemProgress.b).toMatchObject({ status: 'failed', message: 'disk full' })
    expect(p.overallFailed).toBe(1)
    expect(p.overallDone).toBe(2)
    handleProgressMessage({ type: 'item_restore_failed', item_name: 'c' })
    expect(p.itemProgress.c.message).toBe('Failed')
    handleProgressMessage({ type: 'item_restore_done', item_name: 'd' })
    expect(p.overallDone).toBe(2)
  })
})

describe('backup failures', () => {
  it('counts failed items and records the error', () => {
    start()
    handleProgressMessage({ type: 'item_backup_failed', item_name: 'x', error: 'boom', items_done: 1, items_total: 5 })
    expect(p.itemProgress.x).toMatchObject({ status: 'failed', message: 'boom' })
    expect(p.overallFailed).toBe(1)
    expect(p.overallDone).toBe(1)
    expect(p.overallTotal).toBe(5)
    handleProgressMessage({ type: 'item_backup_failed', item_name: 'y' })
    expect(p.itemProgress.y.message).toBe('Failed')
    expect(p.overallFailed).toBe(2)
  })

  it('progress does not revert a terminal item to running and seeds the current item', () => {
    start()
    handleProgressMessage({ type: 'item_backup_failed', item_name: 'x' })
    handleProgressMessage({ type: 'backup_progress', item: 'x', percent: 40, message: 'late' })
    expect(p.itemProgress.x.status).toBe('failed')
    expect(p.currentItem).toMatchObject({ name: 'x', percent: 40 })
  })
})

describe('queue and cancel', () => {
  it('replaces the queue on queue_update', () => {
    handleProgressMessage({ type: 'queue_update', queue: [{ id: 1 }] })
    expect(p.queue).toEqual([{ id: 1 }])
    handleProgressMessage({ type: 'queue_update' })
    expect(p.queue).toEqual([])
  })

  it('marks cancelling only for the active job', () => {
    start()
    handleProgressMessage({ type: 'job_cancelling', job_id: 99 })
    expect(p.cancelling).toBe(false)
    handleProgressMessage({ type: 'job_cancelling', job_id: 1 })
    expect(p.cancelling).toBe(true)
  })

  it('ignores job_cancelling with no active run', () => {
    expect(handleProgressMessage({ type: 'job_cancelling', job_id: 1 })).toBe(true)
    expect(p.cancelling).toBe(false)
  })
})

describe('completion', () => {
  it('clears running at once and the run details after the grace period', () => {
    start()
    handleProgressMessage({ type: 'job_run_completed', job_id: 1, run_id: 10 })
    expect(p.running).toBe(false)
    expect(p.activeRun).not.toBeNull()
    vi.advanceTimersByTime(5000)
    expect(p.activeRun).toBeNull()
  })

  it('a newer run is not wiped by the previous run\'s delayed cleanup', () => {
    start()
    handleProgressMessage({ type: 'job_run_completed', job_id: 1, run_id: 10 })
    handleProgressMessage({ type: 'job_run_started', job_id: 2, run_id: 11 })
    vi.advanceTimersByTime(5000)
    expect(p.activeRun?.run_id).toBe(11)
  })

  it('a stale completion for an older run leaves the active run running', () => {
    start()
    handleProgressMessage({ type: 'job_run_completed', job_id: 1, run_id: 3 })
    expect(p.running).toBe(true)
  })

  it('the elapsed timer ticks while running', () => {
    start()
    vi.advanceTimersByTime(3000)
    expect(p.elapsedSec).toBe(3)
  })
})

describe('restoreFromStatus / syncFromStatus', () => {
  it('restoreFromStatus only updates the queue when nothing is active', () => {
    restoreFromStatus({ active: false, queue: [{ id: 2 }] })
    expect(p.queue).toEqual([{ id: 2 }])
    expect(p.activeRun).toBeNull()
    restoreFromStatus(null)
    expect(p.queue).toEqual([])
  })

  it('restoreFromStatus fills defaults for sparse snapshots', () => {
    restoreFromStatus({ active: true, job_id: 3, run_id: 7, cancelling: true })
    expect(p.activeRun).toMatchObject({ job_name: 'Job #3', run_type: 'backup' })
    expect(p.cancelling).toBe(true)
    expect(p.currentItem).toBeNull()
    expect(p.overallDone).toBe(0)
  })

  it('restoreFromStatus seeds the current item with a restore default message', () => {
    restoreFromStatus({ active: true, job_id: 3, run_id: 7, run_type: 'restore', current_item: 'a' })
    expect(p.currentItem).toMatchObject({ name: 'a', percent: 0, message: 'Preparing restore...' })
    expect(p.itemProgress.a.status).toBe('running')
  })

  it('syncFromStatus ignores inactive snapshots', () => {
    syncFromStatus({ active: false })
    syncFromStatus(null)
    expect(p.activeRun).toBeNull()
  })

  it('syncFromStatus adopts a different run via restoreFromStatus', () => {
    start()
    syncFromStatus({ active: true, job_id: 2, run_id: 20, job_name: 'other' })
    // A tracked run (run_id set) is never rolled back by a snapshot.
    expect(p.activeRun.run_id).toBe(10)
    clearActiveRun()
    syncFromStatus({ active: true, job_id: 2, run_id: 20, job_name: 'other' })
    expect(p.activeRun).toMatchObject({ run_id: 20, job_name: 'other' })
  })

  it('syncFromStatus marks items that reached 100% as done when the current item moves on', () => {
    start()
    handleProgressMessage({ type: 'backup_progress', item: 'a', percent: 100, message: '' })
    syncFromStatus({ active: true, job_id: 1, run_id: 10, current_item: 'b', items_done: 1, items_total: 2 })
    expect(p.itemProgress.a).toMatchObject({ status: 'done', message: 'backup complete' })
    expect(p.currentItem.name).toBe('b')
    expect(p.overallDone).toBe(1)
    syncFromStatus({ active: true, job_id: 1, run_id: 10, started_at: new Date(Date.now() - 5000).toISOString() })
    expect(p.currentItem).toBeNull()
    expect(p.elapsedSec).toBe(5)
  })
})

describe('verifyRun watchdog', () => {
  it('stops immediately when nothing is running', async () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    await verifyRun()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('retries after a non-ok response and after a network error', async () => {
    markJobActiveOptimistically(1, 'a')
    const fetch = vi.fn()
      .mockResolvedValueOnce({ ok: false, status: 500 })
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue({ ok: true, json: async () => ({ active: false }) })
    vi.stubGlobal('fetch', fetch)
    await verifyRun()
    expect(p.running).toBe(true)
    await vi.advanceTimersByTimeAsync(4000)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(p.running).toBe(true)
    await vi.advanceTimersByTimeAsync(4000)
    expect(p.running).toBe(false)
  })

  it('keeps live state when the server reports the tracked run active', async () => {
    start()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ active: true, job_id: 1, run_id: 10 }) })))
    await verifyRun()
    expect(p.running).toBe(true)
    expect(p.activeRun.run_id).toBe(10)
  })

  it('does nothing if a live event cleared state during the fetch', async () => {
    start()
    let resolve
    vi.stubGlobal('fetch', vi.fn(() => new Promise((r) => { resolve = r })))
    const pending = verifyRun()
    handleProgressMessage({ type: 'job_run_completed', job_id: 1, run_id: 10 })
    resolve({ ok: true, json: async () => ({ active: true, job_id: 9, run_id: 99 }) })
    await pending
    expect(p.running).toBe(false)
  })
})
