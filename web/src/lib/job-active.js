/** Decides whether a job row should show the running/cancel control.
 *  Extracted from Jobs.svelte as plain JS (no runes) so it is unit-testable
 *  under node vitest — same reason ws-backoff.js / runner-status-sync.js exist.
 *
 *  `running` is the immediately-updated flag (cleared the moment a run ends);
 *  `activeRun` lingers briefly after completion so the Dashboard overlay can
 *  show the finished state. The flag — not `activeRun` alone — is what makes
 *  the button disappear the instant the job ends.
 */
export function isJobRunningOrQueued({ running, activeRun, queue }, jobId) {
  return (
    (running === true && activeRun?.job_id === jobId) ||
    (Array.isArray(queue) && queue.some((q) => q?.job_id === jobId))
  )
}
