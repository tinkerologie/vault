/** Shared backup/restore progress state that persists across page navigations. */

import { formatBytes } from './utils.js'
import { buildApiRequest } from './runtime-config.js'

// Global progress state – survives component mount/unmount cycles.
let activeRun = $state(null) // { job_id, run_id, job_name, started_at, run_type }
// true only while a run is actually executing. Cleared immediately on
// job_run_completed (unlike activeRun, which lingers for the overlay's grace).
//
// Lifecycle: set true in restoreFromStatus, syncFromStatus, and
// handleProgressMessage (item-message synthesis + job_run_started); cleared
// only in job_run_completed.
let running = $state(false)
let itemProgress = $state({}) // { item_name: { percent, message, item_type, status } }
let overallDone = $state(0)
let overallFailed = $state(0)
let overallTotal = $state(0)
let phaseMessage = $state(null)
let elapsedSec = $state(0)
let jobQueue = $state([])
let cancelling = $state(false)
let _elapsedInterval = null
let _completionTimer = null

function defaultProgressMessage(runType) {
  return runType === 'restore' ? 'Preparing restore...' : 'In progress...'
}

export function getProgress() {
  return {
    get activeRun() { return activeRun },
    get running() { return running },
    get itemProgress() { return itemProgress },
    get overallDone() { return overallDone },
    get overallFailed() { return overallFailed },
    get overallTotal() { return overallTotal },
    get elapsedSec() { return elapsedSec },
    get phaseMessage() { return phaseMessage },
    get queue() { return jobQueue },
    get cancelling() { return cancelling },
  }
}

/** Restore progress state from the runner status API response.
 *  Called on page load / WebSocket reconnect so the progress overlay
 *  re-appears even if the job_run_started message was missed.
 */
export function restoreFromStatus(status) {
  if (!status?.active) return
  // Only overwrite a placeholder that never learned its run_id. A real,
  // already-tracked run (run_id set) is left untouched so a reconnect resync
  // or a late snapshot can never roll a live run backwards.
  if (activeRun && activeRun.run_id != null) return
  activeRun = {
    job_id: status.job_id,
    run_id: status.run_id,
    job_name: status.job_name || `Job #${status.job_id}`,
    started_at: status.started_at ? Date.parse(status.started_at) : Date.now(),
    run_type: status.run_type || 'backup',
  }
  running = true
  overallDone = status.items_done || 0
  overallFailed = status.items_failed || 0
  overallTotal = status.items_total || 0
  jobQueue = status.queue || []
  cancelling = !!status.cancelling
  itemProgress = {}
  if (status.current_item) {
    itemProgress = {
      [status.current_item]: {
        percent: status.current_item_percent || 0,
        message: status.current_item_message || defaultProgressMessage(status.run_type),
        item_type: status.current_item_type,
        status: 'running',
      },
    }
  }
  // Resume elapsed timer from the real start time.
  const startMs = status.started_at ? Date.parse(status.started_at) : Date.now()
  elapsedSec = Math.max(0, Math.round((Date.now() - startMs) / 1000))
  clearInterval(_elapsedInterval)
  _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
  startWatchdog()
}

/** Keep progress state aligned with the latest runner-status snapshot.
 *  Used by proxy polling mode where item-level WebSocket events are unavailable.
 */
export function syncFromStatus(status) {
  if (!status?.active) return

  if (!activeRun || activeRun.run_id !== status.run_id) {
    // restoreFromStatus sets `running` itself when it actually restores. Setting
    // it here would leave running=true while activeRun still points at the old
    // finished run this branch early-returns on.
    restoreFromStatus(status)
    return
  }

  running = true
  activeRun = {
    ...activeRun,
    job_name: status.job_name || activeRun.job_name,
    run_type: status.run_type || activeRun.run_type || 'backup',
  }

  overallDone = status.items_done || 0
  overallFailed = status.items_failed || 0
  overallTotal = status.items_total || 0
  jobQueue = status.queue || []
  cancelling = !!status.cancelling

  if (status.current_item) {
    const existing = itemProgress[status.current_item] || {}
    itemProgress = {
      ...itemProgress,
      [status.current_item]: {
        ...existing,
        percent: status.current_item_percent ?? existing.percent ?? 0,
        item_type: status.current_item_type || existing.item_type,
        status: 'running',
        message: status.current_item_message || existing.message || defaultProgressMessage(status.run_type),
      },
    }
  }

  // Mark non-current items that reached 100% as done (proxy polling doesn't
  // receive item_backup_done / item_restore_done WebSocket events).
  const updated = { ...itemProgress }
  let changed = false
  for (const [name, info] of Object.entries(updated)) {
    if (name !== status.current_item && info.status === 'running' && info.percent >= 100) {
      updated[name] = { ...info, status: 'done', message: info.message || 'backup complete' }
      changed = true
    }
  }
  if (changed) itemProgress = updated

  const startMs = status.started_at ? Date.parse(status.started_at) : Date.now()
  elapsedSec = Math.max(0, Math.round((Date.now() - startMs) / 1000))
}

/** Optimistically mark a job as active the moment its Run Now click
 *  succeeds, so the row flips to Cancel immediately instead of waiting for
 *  the job_run_started WebSocket event. Mirrors the job_run_started state
 *  writes, but never clobbers a run that is already live: if another run is
 *  active the guard short-circuits and leaves state untouched — the row keeps
 *  showing Run Now until the server's queue_update event reflects the click.
 */
export function markJobActiveOptimistically(jobId, jobName) {
  if (running && activeRun?.job_id != null) return
  cancelling = false
  running = true
  clearTimeout(_completionTimer)
  _completionTimer = null
  activeRun = {
    job_id: jobId,
    run_id: null, // unknown until job_run_started arrives
    job_name: jobName || `Job #${jobId}`,
    started_at: Date.now(),
    run_type: 'backup',
  }
  itemProgress = {}
  overallDone = 0
  overallFailed = 0
  overallTotal = 0
  elapsedSec = 0
  clearInterval(_elapsedInterval)
  _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
  startWatchdog()
}

/** Reset all run state to idle. Used by the completion path, the watchdog,
 *  and as the exported reset for tests/consumers. */
export function clearActiveRun() {
  running = false
  activeRun = null
  itemProgress = {}
  overallDone = 0
  overallFailed = 0
  overallTotal = 0
  elapsedSec = 0
  phaseMessage = null
  cancelling = false
  clearInterval(_elapsedInterval)
  clearTimeout(_completionTimer)
  _completionTimer = null
  stopWatchdog()
}

// ---- Run watchdog ----------------------------------------------------------
// While the UI believes a run is active, periodically verify against
// /runner/status and clear the state if the server reports nothing is running.
// The hub is lossy and can drop BOTH job_run_started and job_run_completed for
// a slow client without ever dropping the socket — in which case no reconnect
// resync fires and the optimistic placeholder would otherwise strand the
// Cancel button forever. This backstop is deliberately conservative: it only
// ever clears stale state, never sets running=true (events do that).
let _watchdogTimer = null

function stopWatchdog() {
  if (_watchdogTimer) {
    clearTimeout(_watchdogTimer)
    _watchdogTimer = null
  }
}

function scheduleWatchdog(delayMs) {
  stopWatchdog()
  _watchdogTimer = setTimeout(() => { void verifyRun() }, delayMs)
}

export function startWatchdog() {
  if (_watchdogTimer) return
  scheduleWatchdog(1500)
}

export async function verifyRun() {
  if (!running) {
    stopWatchdog()
    return
  }
  try {
    const { url, options } = buildApiRequest('GET', '/runner/status')
    const res = await fetch(url, options)
    if (!res.ok) {
      scheduleWatchdog(4000)
      return
    }
    const status = await res.json()
    if (!running) return // a live event cleared state while we were fetching
    if (status?.active) {
      // A run is genuinely active. If we still hold a placeholder (run_id
      // unknown because job_run_started was dropped), adopt the real run —
      // but only when it is the same job. A different active job means our
      // placeholder is stale (its run already finished and another started
      // while both events were lost): clear it and let that job's own events
      // (or the next reconnect resync) repopulate the correct state.
      if (activeRun?.run_id == null) {
        if (status.job_id === activeRun?.job_id) {
          restoreFromStatus(status)
          scheduleWatchdog(4000)
          return
        }
        clearActiveRun()
        return
      }
      // run_id is known but the server is active: leave the live state alone.
      // If this is a *stale* run whose completion was dropped while a newer
      // run is already live, we cannot tell it apart from the live run here —
      // clearing would risk killing the correct active state, so we keep
      // polling and rely on the newer run's own completion event to reconcile.
      scheduleWatchdog(4000)
      return
    }
    // Server reports nothing active — the run we were tracking has ended.
    clearActiveRun()
    stopWatchdog()
  } catch {
    scheduleWatchdog(4000) // transient failure: verify again shortly
  }
}

/** Handle an incoming WebSocket message – update progress state.
 *  Returns true if this message was a progress event (handled).
 */
export function handleProgressMessage(msg, jobNameResolver) {
  // If we receive item-level progress but don't have an activeRun yet
  // (e.g. page was reloaded mid-backup), synthesize the run from message data.
  if (!activeRun && msg.job_id && msg.run_id &&
      (msg.type === 'item_backup_start' || msg.type === 'backup_progress' ||
       msg.type === 'item_backup_done' || msg.type === 'item_backup_failed' ||
       msg.type === 'restore_progress' || msg.type === 'item_restore_done' ||
       msg.type === 'item_restore_failed')) {
    const jName = msg.job_name || activeRun?.job_name || jobNameResolver?.(msg.job_id) || `Job #${msg.job_id}`
    activeRun = {
      job_id: msg.job_id,
      run_id: msg.run_id,
      job_name: jName,
      started_at: Date.now(),
      run_type: msg.run_type || (msg.type.startsWith('restore') || msg.type.includes('_restore_') ? 'restore' : 'backup'),
    }
    running = true
    overallTotal = msg.items_total || 0
    clearInterval(_elapsedInterval)
    _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
    startWatchdog()
  }

  switch (msg.type) {
    case 'job_run_started': {
      cancelling = false
      running = true
      // A previous run's delayed cleanup must not wipe this new run's state.
      clearTimeout(_completionTimer)
      _completionTimer = null
      const jName = msg.job_name || activeRun?.job_name || jobNameResolver?.(msg.job_id) || `Job #${msg.job_id}`
      activeRun = {
        job_id: msg.job_id,
        run_id: msg.run_id,
        job_name: jName,
        started_at: Date.now(),
        run_type: msg.run_type || 'backup',
      }
      itemProgress = {}
      overallDone = 0
      overallFailed = 0
      overallTotal = msg.items_total || 0
      elapsedSec = 0
      clearInterval(_elapsedInterval)
      _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
      startWatchdog()
      return true
    }
    case 'containers_stopping_all': {
      phaseMessage = `Stopping ${msg.count} containers...`
      return true
    }
    case 'containers_restarting_all': {
      phaseMessage = `Restarting ${msg.count} containers...`
      return true
    }
    case 'phase_message': {
      phaseMessage = msg.message || null
      return true
    }
    case 'backup_phase': {
      // Deferred-mode phase transitions emitted by the runner (#77).
      if (msg.phase === 'staging') {
        phaseMessage = msg.item_name
          ? `Staging ${msg.item_name} locally...`
          : 'Staging backups locally...'
      } else if (msg.phase === 'uploading') {
        const n = msg.count || 0
        phaseMessage = n > 0
          ? `Uploading ${n} staged item${n === 1 ? '' : 's'} to remote storage...`
          : 'Uploading to remote storage...'
      }
      return true
    }
    case 'item_staged': {
      const prev = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev, percent: 50, message: 'Staged – awaiting upload', status: 'running', item_type: msg.item_type || prev.item_type },
      }
      return true
    }
    case 'item_upload_start': {
      const prev = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev, percent: 60, message: 'Uploading...', status: 'running', item_type: msg.item_type || prev.item_type },
      }
      return true
    }
    case 'item_backup_start': {
      phaseMessage = null
      if (msg.items_total) overallTotal = msg.items_total
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { percent: 0, message: 'Starting...', item_type: msg.item_type, status: 'running' },
      }
      return true
    }
    case 'item_restore_start': {
      phaseMessage = null
      if (msg.items_total) overallTotal = msg.items_total
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { percent: 0, message: 'Starting...', item_type: msg.item_type, status: 'running' },
      }
      return true
    }
    case 'backup_progress':
    case 'restore_progress': {
      const existing = itemProgress[msg.item] || {}
      // Don't revert a terminal status (done/failed) back to running.
      const keepStatus = existing.status === 'done' || existing.status === 'failed'
      itemProgress = {
        ...itemProgress,
        [msg.item]: { ...existing, percent: msg.percent, message: msg.message, item_type: msg.item_type || existing.item_type, status: keepStatus ? existing.status : 'running' },
      }
      return true
    }
    case 'item_backup_done': {
      phaseMessage = null
      const prev = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev, percent: 100, message: `Done – ${formatBytes(msg.size_bytes)}`, status: 'done' },
      }
      if (msg.items_done !== undefined) overallDone = msg.items_done
      if (msg.items_total) overallTotal = msg.items_total
      return true
    }
    case 'item_backup_failed': {
      phaseMessage = null
      const prev2 = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev2, percent: 100, message: msg.error || 'Failed', status: 'failed' },
      }
      overallFailed++
      if (msg.items_done !== undefined) overallDone = msg.items_done
      if (msg.items_total) overallTotal = msg.items_total
      return true
    }
    case 'item_restore_done': {
      phaseMessage = null
      const prev3 = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev3, percent: 100, message: 'Restored', status: 'done' },
      }
      if (msg.items_done !== undefined) overallDone = msg.items_done
      if (msg.items_total) overallTotal = msg.items_total
      return true
    }
    case 'item_restore_failed': {
      phaseMessage = null
      const prev4 = itemProgress[msg.item_name] || {}
      itemProgress = {
        ...itemProgress,
        [msg.item_name]: { ...prev4, percent: 100, message: msg.error || 'Failed', status: 'failed' },
      }
      overallFailed++
      if (msg.items_done !== undefined) overallDone = msg.items_done
      if (msg.items_total) overallTotal = msg.items_total
      return true
    }
    case 'queue_update': {
      jobQueue = msg.queue || []
      return true
    }
    case 'job_cancelling': {
      if (activeRun && msg.job_id === activeRun.job_id) cancelling = true
      return true
    }
    case 'job_run_completed': {
      phaseMessage = null
      cancelling = false
      // Clear immediately: the Jobs page's Cancel button keys off this flag and
      // must disappear the instant the run ends, not after the overlay's 5s grace.
      // Guard against a stale/out-of-order completion for an older run clearing
      // the flag while a newer run is already active. A completion matches the
      // active run when the run ids agree, or — while the active run is still an
      // optimistic placeholder whose run_id is unknown (its job_run_started was
      // dropped by the lossy hub) — when it is for the same job.
      const completionMatches =
        msg.run_id == null ||
        (activeRun?.run_id == null && msg.job_id === activeRun?.job_id) ||
        msg.run_id === activeRun?.run_id
      if (!activeRun || completionMatches) running = false
      if (!running) stopWatchdog()
      clearInterval(_elapsedInterval)
      if (activeRun) {
        const completedRunId = activeRun.run_id
        clearTimeout(_completionTimer)
        _completionTimer = setTimeout(() => {
          // Only clear if a newer run hasn't taken over in the meantime.
          if (activeRun?.run_id !== completedRunId) return
          activeRun = null
          itemProgress = {}
          overallDone = 0
          overallFailed = 0
          overallTotal = 0
          elapsedSec = 0
        }, 5000)
      }
      return true
    }
    default:
      return false
  }
}
