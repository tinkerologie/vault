/** Shared backup/restore progress state that persists across page navigations. */

import { formatBytes } from './utils.js'

// Global progress state — survives component mount/unmount cycles.
let activeRun = $state(null) // { job_id, run_id, job_name, started_at, run_type }
let itemProgress = $state({}) // { item_name: { percent, message, item_type, status } }
let overallDone = $state(0)
let overallFailed = $state(0)
let overallTotal = $state(0)
let phaseMessage = $state(null)
let elapsedSec = $state(0)
let jobQueue = $state([])
let _elapsedInterval = null

function defaultProgressMessage(runType) {
  return runType === 'restore' ? 'Preparing restore...' : 'In progress...'
}

export function getProgress() {
  return {
    get activeRun() { return activeRun },
    get itemProgress() { return itemProgress },
    get overallDone() { return overallDone },
    get overallFailed() { return overallFailed },
    get overallTotal() { return overallTotal },
    get elapsedSec() { return elapsedSec },
    get phaseMessage() { return phaseMessage },
    get queue() { return jobQueue },
  }
}

/** Restore progress state from the runner status API response.
 *  Called on page load / WebSocket reconnect so the progress overlay
 *  re-appears even if the job_run_started message was missed.
 */
export function restoreFromStatus(status) {
  if (!status?.active) return
  // Don't overwrite an already-active run (WebSocket already initialised it).
  if (activeRun) return
  activeRun = {
    job_id: status.job_id,
    run_id: status.run_id,
    job_name: status.job_name || `Job #${status.job_id}`,
    started_at: status.started_at ? new Date(status.started_at).getTime() : Date.now(),
    run_type: status.run_type || 'backup',
  }
  overallDone = status.items_done || 0
  overallFailed = status.items_failed || 0
  overallTotal = status.items_total || 0
  jobQueue = status.queue || []
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
  const startMs = status.started_at ? new Date(status.started_at).getTime() : Date.now()
  elapsedSec = Math.max(0, Math.round((Date.now() - startMs) / 1000))
  clearInterval(_elapsedInterval)
  _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
}

/** Keep progress state aligned with the latest runner-status snapshot.
 *  Used by proxy polling mode where item-level WebSocket events are unavailable.
 */
export function syncFromStatus(status) {
  if (!status?.active) return

  if (!activeRun || activeRun.run_id !== status.run_id) {
    restoreFromStatus(status)
    return
  }

  activeRun = {
    ...activeRun,
    job_name: status.job_name || activeRun.job_name,
    run_type: status.run_type || activeRun.run_type || 'backup',
  }

  overallDone = status.items_done || 0
  overallFailed = status.items_failed || 0
  overallTotal = status.items_total || 0
  jobQueue = status.queue || []

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

  const startMs = status.started_at ? new Date(status.started_at).getTime() : Date.now()
  elapsedSec = Math.max(0, Math.round((Date.now() - startMs) / 1000))
}

/** Handle an incoming WebSocket message — update progress state.
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
    overallTotal = msg.items_total || 0
    clearInterval(_elapsedInterval)
    _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
  }

  switch (msg.type) {
    case 'job_run_started': {
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
        [msg.item_name]: { ...prev, percent: 100, message: `Done — ${formatBytes(msg.size_bytes)}`, status: 'done' },
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
    case 'job_run_completed': {
      phaseMessage = null
      clearInterval(_elapsedInterval)
      if (activeRun) {
        setTimeout(() => {
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
