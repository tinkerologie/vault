import { describe, it, expect, vi, afterEach } from 'vitest'
import { getProgress, markJobActiveOptimistically, handleProgressMessage, restoreFromStatus, syncFromStatus, verifyRun, clearActiveRun } from './progress.svelte.js'

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

describe('currentItem tracking', () => {
  it('sets current item on item_backup_start and updates on backup_progress', () => {
    handleProgressMessage({ type: 'job_run_started', job_id: 1, run_id: 10, job_name: 'test' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 1, run_id: 10, item_name: 'nextcloud', item_type: 'container' })
    const p = getProgress()
    expect(p.currentItem).toEqual({
      name: 'nextcloud',
      item_type: 'container',
      percent: 0,
      message: 'Starting...',
    })

    handleProgressMessage({ type: 'backup_progress', item: 'nextcloud', item_type: 'container', percent: 45, message: 'Backing up appdata...' })
    expect(p.currentItem).toEqual({
      name: 'nextcloud',
      item_type: 'container',
      percent: 45,
      message: 'Backing up appdata...',
    })
  })

  it('advances to next item on new start event and retains currentItem across item_backup_done', () => {
    handleProgressMessage({ type: 'job_run_started', job_id: 1, run_id: 10, job_name: 'test' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 1, run_id: 10, item_name: 'plex', item_type: 'container' })
    expect(getProgress().currentItem?.name).toBe('plex')

    handleProgressMessage({ type: 'item_backup_done', job_id: 1, run_id: 10, item_name: 'plex', size_bytes: 1024 })
    // Retained across done
    expect(getProgress().currentItem?.name).toBe('plex')

    handleProgressMessage({ type: 'item_backup_start', job_id: 1, run_id: 10, item_name: 'radarr', item_type: 'container' })
    expect(getProgress().currentItem?.name).toBe('radarr')
  })

  it('clears currentItem on job_run_completed and clearActiveRun', () => {
    handleProgressMessage({ type: 'job_run_started', job_id: 1, run_id: 10, job_name: 'test' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 1, run_id: 10, item_name: 'plex', item_type: 'container' })
    expect(getProgress().currentItem).not.toBeNull()

    handleProgressMessage({ type: 'job_run_completed', job_id: 1, run_id: 10 })
    expect(getProgress().currentItem).toBeNull()

    handleProgressMessage({ type: 'item_backup_start', job_id: 1, run_id: 11, item_name: 'plex', item_type: 'container' })
    expect(getProgress().currentItem).not.toBeNull()
    clearActiveRun()
    expect(getProgress().currentItem).toBeNull()
  })

  it('populates currentItem from restoreFromStatus and syncFromStatus', () => {
    restoreFromStatus({
      active: true,
      job_id: 2,
      run_id: 20,
      job_name: 'daily',
      current_item: 'homeassistant',
      current_item_type: 'vm',
      current_item_percent: 75,
      current_item_message: 'Snapshotting...',
    })
    const p = getProgress()
    expect(p.currentItem).toEqual({
      name: 'homeassistant',
      item_type: 'vm',
      percent: 75,
      message: 'Snapshotting...',
    })

    syncFromStatus({
      active: true,
      job_id: 2,
      run_id: 20,
      job_name: 'daily',
      current_item: 'homeassistant',
      current_item_type: 'vm',
      current_item_percent: 90,
      current_item_message: 'Transferring...',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'homeassistant',
      item_type: 'vm',
      percent: 90,
      message: 'Transferring...',
    })
  })

  it('does not leak previous item details when transitioning via syncFromStatus, item_staged, or item_upload_start', () => {
    // Start with item A having explicit type and custom progress
    handleProgressMessage({ type: 'job_run_started', job_id: 3, run_id: 30, job_name: 'multi' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 3, run_id: 30, item_name: 'plex', item_type: 'container' })
    handleProgressMessage({ type: 'backup_progress', item: 'plex', item_type: 'container', percent: 80, message: 'Compressing...' })
    expect(getProgress().currentItem).toEqual({
      name: 'plex',
      item_type: 'container',
      percent: 80,
      message: 'Compressing...',
    })

    // syncFromStatus transitions to item B without explicit type or message
    syncFromStatus({
      active: true,
      job_id: 3,
      run_id: 30,
      job_name: 'multi',
      current_item: 'appdata-folder',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'appdata-folder',
      item_type: '',
      percent: 0,
      message: 'In progress...',
    })

    // item_staged transitions to item C without explicit item_type
    handleProgressMessage({
      type: 'item_staged',
      job_id: 3,
      run_id: 30,
      item_name: 'system-vm',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'system-vm',
      item_type: '',
      percent: 50,
      message: 'Staged – awaiting upload',
    })

    // item_upload_start transitions to item D without explicit item_type
    handleProgressMessage({
      type: 'item_upload_start',
      job_id: 3,
      run_id: 30,
      item_name: 'flash-backup',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'flash-backup',
      item_type: '',
      percent: 60,
      message: 'Uploading...',
    })
  })

  it('clears currentItem when syncFromStatus receives an active status with no current_item', () => {
    restoreFromStatus({
      active: true,
      job_id: 4,
      run_id: 40,
      job_name: 'test-job',
      current_item: 'influxdb',
      current_item_type: 'container',
      current_item_percent: 50,
      current_item_message: 'Working...',
    })
    expect(getProgress().currentItem?.name).toBe('influxdb')

    syncFromStatus({
      active: true,
      job_id: 4,
      run_id: 40,
      job_name: 'test-job',
      current_item: '',
    })
    expect(getProgress().currentItem).toBeNull()
  })

  it('synthesizes active run and sets currentItem on reload for item_staged and item_upload_start', () => {
    // Reload state: activeRun is null
    clearActiveRun()
    expect(getProgress().activeRun).toBeNull()

    handleProgressMessage({
      type: 'item_staged',
      job_id: 5,
      run_id: 50,
      job_name: 'staged-job',
      item_name: 'mariadb',
      item_type: 'container',
    })
    expect(getProgress().activeRun?.run_id).toBe(50)
    expect(getProgress().running).toBe(true)
    expect(getProgress().currentItem).toEqual({
      name: 'mariadb',
      item_type: 'container',
      percent: 50,
      message: 'Staged – awaiting upload',
    })

    clearActiveRun()
    handleProgressMessage({
      type: 'item_upload_start',
      job_id: 6,
      run_id: 60,
      job_name: 'upload-job',
      item_name: 'vaultwarden',
      item_type: 'container',
    })
    expect(getProgress().activeRun?.run_id).toBe(60)
    expect(getProgress().running).toBe(true)
    expect(getProgress().currentItem).toEqual({
      name: 'vaultwarden',
      item_type: 'container',
      percent: 60,
      message: 'Uploading...',
    })
  })

  it('clears currentItem on run completion', () => {
    handleProgressMessage({ type: 'job_run_started', job_id: 7, run_id: 100, job_name: 'new-run' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 7, run_id: 100, item_name: 'postgres', item_type: 'container' })
    expect(getProgress().currentItem?.name).toBe('postgres')

    handleProgressMessage({ type: 'job_run_completed', job_id: 7, run_id: 100 })
    expect(getProgress().running).toBe(false)
    expect(getProgress().currentItem).toBeNull()
  })

  it('handles restore progress and default restore messages', () => {
    // restoreFromStatus with restore run_type and no explicit message
    restoreFromStatus({
      active: true,
      job_id: 8,
      run_id: 108,
      job_name: 'restore-job',
      run_type: 'restore',
      current_item: 'nextcloud',
      current_item_type: 'container',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'nextcloud',
      item_type: 'container',
      percent: 0,
      message: 'Preparing restore...',
    })

    // item_restore_start and restore_progress
    handleProgressMessage({
      type: 'item_restore_start',
      job_id: 8,
      run_id: 108,
      item_name: 'mariadb',
      item_type: 'container',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'mariadb',
      item_type: 'container',
      percent: 0,
      message: 'Starting...',
    })

    handleProgressMessage({
      type: 'restore_progress',
      item: 'mariadb',
      item_type: 'container',
      percent: 55,
      message: 'Unpacking files...',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'mariadb',
      item_type: 'container',
      percent: 55,
      message: 'Unpacking files...',
    })

    // backup_progress when currentItem was null
    clearActiveRun()
    handleProgressMessage({
      type: 'job_run_started',
      job_id: 9,
      run_id: 109,
      job_name: 'orphan-progress',
    })
    handleProgressMessage({
      type: 'backup_progress',
      item: 'standalone-container',
      item_type: 'container',
      percent: 30,
      message: 'Hashing...',
    })
    expect(getProgress().currentItem).toEqual({
      name: 'standalone-container',
      item_type: 'container',
      percent: 30,
      message: 'Hashing...',
    })
  })

  it('clears activeRun and currentItem after completion grace timeout', () => {
    vi.useFakeTimers()
    handleProgressMessage({ type: 'job_run_started', job_id: 10, run_id: 110, job_name: 'grace-test' })
    handleProgressMessage({ type: 'item_backup_start', job_id: 10, run_id: 110, item_name: 'plex', item_type: 'container' })
    handleProgressMessage({ type: 'job_run_completed', job_id: 10, run_id: 110 })
    expect(getProgress().running).toBe(false)
    expect(getProgress().activeRun).not.toBeNull()

    vi.advanceTimersByTime(5000)
    expect(getProgress().activeRun).toBeNull()
    expect(getProgress().currentItem).toBeNull()
    vi.useRealTimers()
  })

  it('does not clear newer active run when older completion timer fires', () => {
    vi.useFakeTimers()
    handleProgressMessage({ type: 'job_run_started', job_id: 11, run_id: 111, job_name: 'first-run' })
    handleProgressMessage({ type: 'job_run_completed', job_id: 11, run_id: 111 })

    // Start a new run before the 5s timer expires
    handleProgressMessage({ type: 'job_run_started', job_id: 12, run_id: 112, job_name: 'second-run' })
    vi.advanceTimersByTime(5000)
    expect(getProgress().activeRun?.run_id).toBe(112)
    vi.useRealTimers()
  })
})
