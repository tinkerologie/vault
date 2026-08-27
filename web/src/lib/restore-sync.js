/**
 * Reconciliation helpers for the restore wizard.
 *
 * Extracted as plain JS (not a .svelte.js runes module) so it is importable
 * under the project's node vitest env.
 */

/**
 * Checks whether a runner status snapshot represents an active restore for the given job.
 *
 * @param {object|null|undefined} status freshly fetched /runner/status body or snapshot
 * @param {number|string|null|undefined} restoringJobId job ID of the in-progress restore, if known
 * @returns {boolean} true only when status is active, run_type is 'restore', and job_id matches (if set)
 */
export function isRestoreActive(status, restoringJobId) {
  if (!status || status.active !== true || status.run_type !== 'restore') {
    return false
  }
  if (restoringJobId != null && status.job_id !== restoringJobId) {
    return false
  }
  return true
}

/**
 * Checks whether an incoming job_run_completed message should clear the restoring flag.
 *
 * @param {object|null|undefined} msg incoming WebSocket message
 * @param {number|string|null|undefined} restoringJobId job ID of the in-progress restore, if known
 * @returns {boolean} true when the completed run matches the restore
 */
export function shouldClearOnCompleted(msg, restoringJobId) {
  if (msg?.run_type !== 'restore') return false
  if (restoringJobId != null && msg.job_id !== restoringJobId) return false
  return true
}

/**
 * Checks whether an incoming runner_status_snapshot message should clear the restoring flag.
 *
 * @param {object|null|undefined} status snapshot status object
 * @param {number|string|null|undefined} restoringJobId job ID of the in-progress restore, if known
 * @returns {boolean} true when the restore is no longer active
 */
export function shouldClearOnSnapshot(status, restoringJobId) {
  return !isRestoreActive(status, restoringJobId)
}
