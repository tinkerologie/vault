// What a screen reader hears about a running backup or restore (#686). The
// progress card itself changes every second (elapsed time, percentages, speed),
// so it is not a live region. Only a start, a change of phase, and the finish
// are announced, through one visually hidden status element.

// Phase messages name the item being staged ("Staging plex locally..."), so the
// announcement keys on the kind of phase, not its exact text.
const PHASES = [
  [/^staging/i, 'Staging backups locally'],
  [/^uploading/i, 'Uploading to remote storage'],
  [/^stopping/i, 'Stopping containers'],
  [/^restarting/i, 'Restarting containers'],
]

function phaseLabel(message) {
  if (!message) return ''
  const match = PHASES.find(([pattern]) => pattern.test(message))
  return match ? match[1] : String(message).replace(/\.\.\.$|…$/, '')
}

/**
 * @param {object} state
 * @param {any} state.activeRun { run_id, job_name, run_type } or null.
 * @param {boolean} state.running False once the run has finished.
 * @param {string|null} [state.phaseMessage]
 * @param {number} [state.failed] Items that failed so far.
 * @returns {{ key: string, text: string } | null} Null while nothing runs. A new
 *   key means a new announcement; the same key means stay quiet.
 */
export function progressAnnouncement({ activeRun, running, phaseMessage = null, failed = 0 }) {
  if (!activeRun) return null
  const kind = activeRun.run_type === 'restore' ? 'Restore' : 'Backup'
  const subject = `${kind} of ${activeRun.job_name}`
  const id = activeRun.run_id ?? activeRun.job_name
  if (!running) {
    const outcome = failed > 0 ? `finished with ${failed} failed item${failed === 1 ? '' : 's'}` : 'finished'
    return { key: `${id}|done|${failed > 0}`, text: `${subject} ${outcome}` }
  }
  const phase = phaseLabel(phaseMessage)
  if (phase) return { key: `${id}|phase|${phase}`, text: `${subject}: ${phase}` }
  return { key: `${id}|started`, text: `${subject} started` }
}
