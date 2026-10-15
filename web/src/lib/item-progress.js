// Structured item progress (#456): turns the daemon's phase and counters
// into what the progress card shows. Kept free of Svelte so it is unit-tested.
import { formatBytes, formatDuration, formatInt } from './utils.js'

/** No progress for this long reads as a possible stall. */
export const STALL_HINT_SECONDS = 120

/**
 * Maps a runner-status snapshot or a backup/restore_progress event (both use
 * the same snake_case fields) to a detail object, or null when the daemon
 * sent no structured progress.
 */
export function detailFrom(src) {
  if (!src?.phase) return null
  return {
    phase: src.phase,
    bytesDone: src.bytes_done || 0,
    bytesTotal: src.bytes_total || 0,
    filesDone: src.files_done || 0,
    filesTotal: src.files_total || 0,
    rateBytes: src.rate_bytes_per_sec || 0,
    rateFiles: src.rate_files_per_sec || 0,
    eta: src.eta_seconds || 0,
    determinate: !!src.progress_determinate,
    percent: src.progress_determinate ? (src.percent ?? src.current_item_percent ?? 0) : null,
    phaseStartedAt: src.phase_started_at ? Date.parse(src.phase_started_at) : null,
    lastProgressAt: src.last_progress_at ? Date.parse(src.last_progress_at) : null,
  }
}

const PHASE_LABELS = {
  scanning: () => 'Scanning…',
  preparing: () => 'Preparing…',
  processing: (restore) => (restore ? 'Restoring' : 'Processing'),
  transferring: (restore) => (restore ? 'Downloading' : 'Uploading'),
  verifying: () => 'Verifying',
  finalizing: () => 'Finalizing…',
}

/** Human label for a phase. Unknown phases are shown capitalised. */
export function phaseLabel(phase, runType) {
  const fn = PHASE_LABELS[phase]
  if (fn) return fn(runType === 'restore')
  return phase ? phase[0].toUpperCase() + phase.slice(1) : ''
}

/**
 * What to render for one item's detail at time `now` (ms):
 * - label: the phase ("Uploading")
 * - text: counters, e.g. "84.2 GB / 225 GB — 37%" (determinate) or
 *   "12,340 files" (indeterminate); empty when nothing is counted yet
 * - percent: number when determinate, otherwise null
 * - rate / eta: strings, or null when unknown
 * - stall: "No progress for 3m" once nothing moved for STALL_HINT_SECONDS
 */
export function describeItemProgress(detail, runType, now = Date.now()) {
  if (!detail) return null
  const label = phaseLabel(detail.phase, runType)
  const parts = []
  if (detail.bytesTotal > 0) {
    parts.push(`${formatBytes(detail.bytesDone)} / ${formatBytes(detail.bytesTotal)}`)
  } else if (detail.bytesDone > 0) {
    parts.push(formatBytes(detail.bytesDone))
  }
  if (detail.filesTotal > 0) {
    parts.push(`${formatInt(detail.filesDone)} / ${formatInt(detail.filesTotal)} files`)
  } else if (detail.filesDone > 0) {
    parts.push(`${formatInt(detail.filesDone)} files`)
  }
  let text = parts.join(' · ')
  if (detail.determinate && detail.percent != null) text += `${text ? ' — ' : ''}${detail.percent}%`

  let rate = null
  if (detail.rateBytes > 0) rate = `${formatBytes(detail.rateBytes)}/s`
  else if (detail.rateFiles > 0) rate = `${formatInt(Math.round(detail.rateFiles))} files/s`

  const eta = detail.determinate && detail.eta > 0 ? `${formatDuration(detail.eta)} left` : null

  let stall = null
  if (detail.lastProgressAt) {
    const idle = (now - detail.lastProgressAt) / 1000
    if (idle >= STALL_HINT_SECONDS) stall = `No progress for ${formatDuration(idle)}`
  }
  return { label, text, percent: detail.determinate ? detail.percent : null, rate, eta, stall }
}
