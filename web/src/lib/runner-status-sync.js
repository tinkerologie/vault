/**
 * Reconciliation between a locally-known run state and a /runner/status
 * snapshot.
 *
 * Extracted as plain JS (not a .svelte.js runes module) so it is importable
 * under the project's node vitest env — same reason ws-backoff.js exists.
 *
 * Two callers need it:
 *   - the polling fallback, which has no event stream at all
 *   - the WebSocket path on (re)connect, where events may have been missed
 *     while the socket was down. A client the hub evicts for being slow loses
 *     the in-flight message, which can be the terminal job_run_completed; the
 *     progress UI only clears on that event, so without this reconciliation a
 *     finished job renders as still running until the user reloads.
 */

/**
 * Returns the messages needed to bring listeners from `previous` to
 * `snapshot`. Pure — the caller emits them and stores the new baseline.
 *
 * @param {object|null} previous last known status ({active, job_id, ...})
 * @param {object|null} snapshot freshly fetched /runner/status body
 * @returns {Array<object>} messages to emit, in order
 */
export function reconcileRunnerStatus(previous, snapshot) {
  const messages = [{ type: 'runner_status_snapshot', status: snapshot }]

  const prevQueue = JSON.stringify(previous?.queue || [])
  const nextQueue = JSON.stringify(snapshot?.queue || [])
  if (prevQueue !== nextQueue) {
    messages.push({ type: 'queue_update', queue: snapshot?.queue || [] })
  }

  if (!previous?.active && snapshot?.active) {
    messages.push({
      type: 'job_run_started',
      job_id: snapshot.job_id,
      run_id: snapshot.run_id,
      job_name: snapshot.job_name,
      run_type: snapshot.run_type,
      items_total: snapshot.items_total,
    })
  }

  if (previous?.active && !snapshot?.active) {
    messages.push({
      type: 'job_run_completed',
      job_id: previous.job_id,
      run_id: previous.run_id,
      run_type: previous.run_type,
    })
  }

  return messages
}

/**
 * Folds a live WebSocket message into the run-state baseline that
 * reconcileRunnerStatus compares against.
 *
 * Without this the baseline would only ever be maintained by the polling path,
 * so a reconnect during normal WebSocket operation would have nothing to
 * detect a completed run against.
 *
 * @param {object|null} previous current baseline
 * @param {object} msg incoming message
 * @returns {object|null} updated baseline (may be the same reference)
 */
export function trackRunState(previous, msg) {
  switch (msg?.type) {
    case 'job_run_started':
      return {
        active: true,
        job_id: msg.job_id,
        run_id: msg.run_id,
        job_name: msg.job_name,
        run_type: msg.run_type,
        items_total: msg.items_total,
        queue: previous?.queue || [],
      }
    case 'job_run_completed':
      return { active: false, queue: previous?.queue || [] }
    case 'queue_update':
      return { ...(previous || { active: false }), queue: msg.queue || [] }
    default:
      return previous
  }
}
