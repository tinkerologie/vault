/** Shared utility functions */

import { getHour12 } from './runtime-config.js'

export function formatBytes(bytes) {
  // Mirrors Bytes() in internal/format exactly, including these guards: a
  // rate computed from a zero duration can arrive as NaN or Infinity, and
  // rendering that as a number would be worse than saying nothing.
  // Missing data (null/undefined) keeps reading as "0 B" — callers pass it
  // for a size that has not been measured yet, and an em dash there would be
  // a behaviour change. NaN/Infinity are different: they mean a computed
  // value went wrong, which is what Go reports as an em dash too.
  if (bytes == null) return '0 B'
  if (!Number.isFinite(bytes)) return '—'
  if (bytes === 0) return '0 B'
  if (bytes < 0) return '-' + formatBytes(-bytes)
  const k = 1024
  // Keep these units and this rounding in step with Bytes() in
  // internal/format — the same number is shown by the daemon (notifications,
  // backup progress, anomaly summaries) and by this interface, and they used
  // to disagree. PB is included and the index is clamped to it: without the
  // clamp a petabyte-scale value indexed past the end of the array and
  // rendered as "1 undefined".
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  // Divide rather than index by Math.log: the logarithm is inexact near a
  // boundary and picked a different unit than the daemon for values just
  // under one (1 PB minus a byte came out as '1 PB' here and '1024 TB'
  // there). A value that rounds up to 1024 promotes, so neither side ever
  // prints '1024 TB'.
  let i = 0
  let v = bytes
  while (v >= k && i < units.length - 1) {
    v /= k
    i++
  }
  if (i < units.length - 1 && Math.round(v * 10) / 10 >= k) {
    v /= k
    i++
  }
  if (i === 0) return `${Math.round(v)} ${units[i]}`
  // Round half away from zero, matching Go — see the note there.
  return parseFloat((Math.round(v * 10) / 10).toFixed(1)) + ' ' + units[i]
}

export function formatDate(str) {
  if (!str) return '–'
  const d = new Date(str)
  if (isNaN(d.getTime())) return '–'
  const hour12 = getHour12()
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    ...(hour12 !== undefined && { hour12 }),
  })
}

/**
 * Compact variant of formatDate – drops the year and uses a numeric hour
 * (no leading zero). Roughly 30-40% narrower than formatDate. Useful in
 * tight horizontal rows like card footers where the full date would push
 * adjacent elements to wrap. Pair with `title={formatDate(str)}` so the
 * full date stays available on hover.
 */
export function formatDateCompact(str) {
  if (!str) return '–'
  const d = new Date(str)
  if (isNaN(d.getTime())) return '–'
  const hour12 = getHour12()
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    ...(hour12 !== undefined && { hour12 }),
  })
}

/** Format hour + minute into a clock time string respecting the configured time format */
export function formatClockTime(h, m) {
  const hour12 = getHour12()
  if (hour12 === false) {
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
  }
  if (hour12 === true) {
    const h12 = h === 0 ? 12 : h > 12 ? h - 12 : h
    const ampm = h < 12 ? 'AM' : 'PM'
    return `${h12}:${String(m).padStart(2, '0')} ${ampm}`
  }
  // auto: use browser locale
  const d = new Date()
  d.setHours(h, m, 0, 0)
  return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
}

export function relTime(str) {
  if (!str) return '–'
  const d = new Date(str)
  if (isNaN(d.getTime())) return '–'
  const diff = Date.now() - d.getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  if (days < 30) return `${days}d ago`
  return formatDate(str)
}

export function statusColor(status) {
  const map = {
    success: 'text-success',
    completed: 'text-success',
    running: 'text-info',
    partial: 'text-warning',
    pending: 'text-warning',
    failed: 'text-danger',
    error: 'text-danger',
  }
  return map[status?.toLowerCase()] || 'text-text-muted'
}

export function statusBadge(status) {
  const map = {
    success: 'badge-success',
    completed: 'badge-success',
    running: 'badge-info',
    partial: 'badge-warning',
    pending: 'badge-warning',
    failed: 'badge-danger',
    error: 'badge-danger',
  }
  return 'badge ' + (map[status?.toLowerCase()] || 'badge-neutral')
}

/** Parse a storage config JSON string into an object */
export function parseConfig(cfg) {
  if (!cfg) return {}
  if (typeof cfg === 'object') return cfg
  try {
    return JSON.parse(cfg)
  } catch {
    return {}
  }
}

/** Convert a cron expression to human-readable text */
export function describeSchedule(cron) {
  if (!cron) return 'Manual only'
  const parts = cron.trim().split(/\s+/)
  if (parts.length !== 5) return cron
  const [min, hr, dom, mon, dow] = parts
  const hrNum = parseInt(hr, 10)
  const minNum = parseInt(min, 10)
  if (isNaN(hrNum) || isNaN(minNum)) return cron
  const time = formatClockTime(hrNum, minNum)
  const monthNames = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
  if (mon !== '*' && dom !== '*') {
    const monNum = parseInt(mon, 10)
    if (!Number.isInteger(monNum) || monNum < 1 || monNum > 12) {
      return `Yearly at ${time}`
    }
    if (dom === 'L') return `Yearly on last day of ${monthNames[monNum - 1]} at ${time}`
    const domNum = parseInt(dom, 10)
    if (!Number.isInteger(domNum) || domNum < 1 || domNum > 31) return `Yearly at ${time}`
    return `Yearly on ${monthNames[monNum - 1]} ${ordinal(domNum)} at ${time}`
  }
  if (dom !== '*' && dow === '*') {
    if (dom === 'L') return `Monthly on last day at ${time}`
    const domNum = parseInt(dom, 10)
    if (!Number.isInteger(domNum) || domNum < 1 || domNum > 31) return `Monthly at ${time}`
    return `Monthly on ${ordinal(domNum)} at ${time}`
  }
  if (dow !== '*' && dom === '*') {
    const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
    const dowParts = dow.split(',')
    if (dowParts.length === 1) return `Weekly on ${days[parseInt(dowParts[0])]} at ${time}`
    return `${dowParts.map(d => days[parseInt(d)]).join(', ')} at ${time}`
  }
  return `Daily at ${time}`
}

function ordinal(n) {
  const s = ['th', 'st', 'nd', 'rd']
  const v = n % 100
  return n + (s[(v - 20) % 10] || s[v] || s[0])
}

/** Format a next-run time string as relative time ("in 2h 15m") */
export function relTimeUntil(dateStr) {
  if (!dateStr) return null
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return null
  const diff = d.getTime() - Date.now()
  if (diff < 0) return 'overdue'
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'in < 1m'
  if (mins < 60) return `in ${mins}m`
  const hrs = Math.floor(mins / 60)
  const remMins = mins % 60
  if (hrs < 24) return remMins > 0 ? `in ${hrs}h ${remMins}m` : `in ${hrs}h`
  const days = Math.floor(hrs / 24)
  return `in ${days}d`
}

/**
 * Extract a failure reason from a job run's log.
 *
 * Surfaces the full per-item error (e.g. the libvirt/QEMU message behind a
 * failed VM backup) rather than a truncated snippet – operators need the whole
 * message to diagnose why a backup failed. Also covers 'partial' runs, where
 * some items succeeded but at least one failed.
 */
export function getFailureReason(run) {
  if (run.status !== 'failed' && run.status !== 'error' && run.status !== 'partial') return null
  if (!run.log) return run.status === 'partial' ? null : 'Unknown error'
  try {
    const items = JSON.parse(run.log)
    if (Array.isArray(items)) {
      const failed = items.filter(i => i.status === 'error' || i.status === 'failed')
      if (failed.length > 0 && failed[0].error) {
        const reason = failed[0].error
        return failed.length > 1 ? `${reason} (+${failed.length - 1} more failed)` : reason
      }
      if (failed.length > 0) return `${failed.length} item(s) failed`
    }
  } catch {
    // Plain-text log (not the structured per-item JSON array). Prefer an
    // explicit error/failure line, but fall back to the first non-empty line
    // so messages that contain neither word – e.g. "All configured backup
    // targets are missing from this server…" – still reach the dashboard
    // instead of the generic placeholder. Capped generously to guard against
    // pathological multi-kilobyte log lines.
    const lines = run.log.split('\n').map(l => l.trim()).filter(Boolean)
    const errLine = lines.find(l => l.toLowerCase().includes('error') || l.toLowerCase().includes('fail'))
    const line = errLine || lines[0]
    if (line) return line.slice(0, 500)
  }
  return run.status === 'partial' ? null : 'Backup failed – see Logs for details'
}

/** Format seconds into human-readable duration (e.g. "11m 4s", "2h 15m") */
export function formatDuration(seconds) {
  if (seconds == null || seconds < 0) return '–'
  const sec = Math.round(seconds)
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

/**
 * Humanize the numbers embedded in an anomaly summary string for display.
 * Detectors store summaries like "backup size anomaly: 4258918530 bytes
 * (0.8x median)" and "backup duration anomaly: 326s (0.8x median)"; this
 * rewrites the raw byte counts to KB/MB/GB and bare second durations to
 * m/s/h so the headline reads "4 GB" / "5m 26s". Applied at render time so
 * it fixes anomalies stored before the backend started humanizing summaries.
 * Idempotent: already-friendly summaries ("4 GB", "5m 26s") contain no
 * "<n> bytes" / bare "<n>s" tokens to rewrite.
 */
export function prettyAnomalySummary(summary) {
  if (!summary) return summary
  return summary
    .replace(/This backup grew(?=\s+to\s+.*?,\s*about <1× its usual)/g, 'This backup shrank')
    .replace(/(\d+)\s*bytes/g, (_, n) => formatBytes(Number(n)))
    .replace(/\b(\d+)s\b(?=\s|$|[),.])/g, (_, n) => formatDuration(Number(n)))
}

/** Format a start/end date pair into human-readable duration */
export function formatDurationFromDates(startedAt, completedAt) {
  if (!startedAt || !completedAt) return '–'
  const start = new Date(startedAt)
  const end = new Date(completedAt)
  if (isNaN(start.getTime()) || isNaN(end.getTime())) return '–'
  return formatDuration((end - start) / 1000)
}

/** Format bytes/seconds into human-readable speed (e.g. "31.2 MB/s") */
export function formatSpeed(bytes, seconds) {
  if (!bytes || !seconds || seconds === 0) return null;
  // Derived from the shared byte contract so speed units and rounding match
  // formatBytes (and the daemon) instead of diverging near unit boundaries.
  return formatBytes(bytes / seconds) + '/s';
}

/**
 * Reduce backup runs to the single largest backup per job, sorted largest
 * first. Only "backup" runs with a real size count. `nameByJob` maps a job
 * id to its display name.
 *
 * The Largest-backups dashboard widget must reflect the true largest backup
 * (typically the full), not whatever happens to be in the last handful of
 * runs — an incremental run records only its delta, so once a full ages out
 * of a short recent window the widget would otherwise shrink to a small
 * incremental. Feeding this a broad history window keeps the full in view.
 */
export function largestBackupsByJob(runs, nameByJob, limit = 5) {
  const maxByJob = new Map()
  for (const r of runs || []) {
    if ((r.run_type || 'backup') !== 'backup' || !r.size_bytes) continue
    if (r.size_bytes > (maxByJob.get(r.job_id) || 0)) maxByJob.set(r.job_id, r.size_bytes)
  }
  return Array.from(maxByJob, ([jobId, size]) => ({
    name: (nameByJob && nameByJob.get(jobId)) || 'Unknown',
    size,
  }))
    .sort((a, b) => b.size - a.size)
    .slice(0, Math.max(0, limit))
}

// Describes the outcome of a database location change for the Settings toast,
// so the user is told whether the database actually moved rather than only
// that the setting was saved. A `warning` means the move did not complete —
// the database is still readable at its previous location.
export function snapshotMigrationMessage(info, fallback) {
  const m = info?.migration
  if (!m) return { text: fallback, tone: 'success' }
  if (m.warning) return { text: `Database not migrated — ${m.warning}`, tone: 'error' }
  const count = m.files_retired ?? 0
  const detail =
    count > 0 ? ` — ${count === 1 ? '1 file' : `${count} files`} removed from ${m.from}` : ''
  return { text: `Database migrated to ${m.to}${detail}`, tone: 'success' }
}
