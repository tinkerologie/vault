/** Shared backup/restore progress state that persists across page navigations. */

import { formatBytes } from './utils.js'

// Global progress state — survives component mount/unmount cycles.
let activeRun = $state(null) // { job_id, run_id, job_name, started_at }
let itemProgress = $state({}) // { item_name: { percent, message, item_type, status } }
let overallDone = $state(0)
let overallFailed = $state(0)
let overallTotal = $state(0)
let elapsedSec = $state(0)
let _elapsedInterval = null

export function getProgress() {
  return {
    get activeRun() { return activeRun },
    get itemProgress() { return itemProgress },
    get overallDone() { return overallDone },
    get overallFailed() { return overallFailed },
    get overallTotal() { return overallTotal },
    get elapsedSec() { return elapsedSec },
  }
}

/** Handle an incoming WebSocket message — update progress state.
 *  Returns true if this message was a progress event (handled).
 */
export function handleProgressMessage(msg, jobNameResolver) {
  switch (msg.type) {
    case 'job_run_started': {
      const jName = jobNameResolver?.(msg.job_id) || `Job #${msg.job_id}`
      activeRun = { job_id: msg.job_id, run_id: msg.run_id, job_name: jName, started_at: Date.now() }
      itemProgress = {}
      overallDone = 0
      overallFailed = 0
      overallTotal = msg.items_total || 0
      elapsedSec = 0
      clearInterval(_elapsedInterval)
      _elapsedInterval = setInterval(() => { elapsedSec++ }, 1000)
      return true
    }
    case 'item_backup_start': {
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
      itemProgress = {
        ...itemProgress,
        [msg.item]: { ...existing, percent: msg.percent, message: msg.message, item_type: msg.item_type || existing.item_type, status: 'running' },
      }
      return true
    }
    case 'item_backup_done': {
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
    case 'job_run_completed': {
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
