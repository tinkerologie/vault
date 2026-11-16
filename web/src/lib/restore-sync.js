/**
 * Reconciliation helpers for the restore wizard.
 *
 * Extracted as plain JS (not a .svelte.js runes module) so it is importable
 * under the project's node vitest env.
 */

/**
 * Checks if targetJobId matches the expected job filter (single ID, Set, Array, or null/any).
 *
 * @param {number|string|null|undefined} targetJobId
 * @param {number|string|Set<number|string>|Array<number|string>|null|undefined} filter
 * @returns {boolean}
 */
export function matchesJobId(targetJobId, filter) {
  if (filter == null) return true
  if (targetJobId == null) return false

  if (typeof filter === 'number' || typeof filter === 'string') {
    return Number(targetJobId) === Number(filter) || targetJobId === filter
  }
  if (filter instanceof Set || filter instanceof Map) {
    return filter.has(targetJobId) || filter.has(Number(targetJobId)) || filter.has(String(targetJobId))
  }
  if (Array.isArray(filter)) {
    return filter.some(id => Number(id) === Number(targetJobId) || id === targetJobId)
  }
  return false
}

const terminalStatuses = new Set(['completed', 'partial', 'failed', 'cancelled'])

export function hasTerminalOutcome(run) {
  return terminalStatuses.has(run?.status)
}

export function outcomeFromCompletion(msg, fallbackTotal) {
  if (msg?.run_type !== 'restore' || !hasTerminalOutcome(msg)) return null
  const done = msg.items_done ?? 0
  const total = msg.items_total ?? fallbackTotal ?? done
  let failed = msg.items_failed ?? 0
  if (msg.items_failed == null && fallbackTotal != null) {
    if (msg.status === 'failed') failed = total
    if (msg.status === 'partial') failed = Math.max(0, total - done)
  }
  return { status: msg.status, done, failed, total, bytes: msg.size_bytes ?? 0 }
}

export function outcomeFromRun(run, { jobId, runId }) {
  if (jobId == null || runId == null || !matchesJobId(run?.job_id, jobId) || !matchesJobId(run?.id, runId)) return null
  return outcomeFromCompletion(run)
}

// A reconnect can produce just one snapshot, so retry without waiting for another event.
export async function waitForRestoreOutcome(getRun, expected, isCurrent) {
  let delay = 1000
  while (isCurrent()) {
    let run
    try {
      run = await getRun(expected.runId)
    } catch {
      // The API can be briefly unavailable as the connection recovers.
    }
    if (!isCurrent()) return null
    const outcome = outcomeFromRun(run, expected)
    if (outcome) return outcome
    await new Promise(resolve => setTimeout(resolve, delay))
    delay = Math.min(delay * 2, 8000)
  }
  return null
}

/**
 * Checks whether a runner status snapshot represents an active restore for the given job or set of jobs.
 *
 * @param {object|null|undefined} status freshly fetched /runner/status body or snapshot
 * @param {number|string|Set<number|string>|Array<number|string>|null|undefined} restoringJobIds job ID or set/array of in-progress restore job IDs
 * @returns {boolean} true only when status is active, run_type is 'restore', and job_id matches (if set)
 */
export function isRestoreActive(status, restoringJobIds) {
  if (!status || status.active !== true || status.run_type !== 'restore') {
    return false
  }
  if (restoringJobIds != null && !matchesJobId(status.job_id, restoringJobIds)) {
    return false
  }
  return true
}

/**
 * Checks whether an incoming job_run_completed message should clear the restoring flag.
 *
 * @param {object|null|undefined} msg incoming WebSocket message
 * @param {number|string|Set<number|string>|Array<number|string>|null|undefined} restoringJobIds job ID or set/array of in-progress restore job IDs
 * @returns {boolean} true when the completed run matches the restore
 */
export function shouldClearOnCompleted(msg, restoringJobIds) {
  if (msg?.run_type !== 'restore') return false
  if (restoringJobIds != null && !matchesJobId(msg.job_id, restoringJobIds)) return false
  return true
}

/**
 * Checks whether an incoming runner_status_snapshot message should clear the restoring flag.
 *
 * @param {object|null|undefined} status snapshot status object
 * @param {number|string|Set<number|string>|Array<number|string>|null|undefined} restoringJobIds job ID or set/array of in-progress restore job IDs
 * @returns {boolean} true when the restore is no longer active
 */
export function shouldClearOnSnapshot(status, restoringJobIds) {
  return !isRestoreActive(status, restoringJobIds)
}
