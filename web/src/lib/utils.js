/** Shared utility functions */

import { getHour12 } from './runtime-config.js'

export function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B'
  const k = 1024
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + units[i]
}

export function formatDate(str) {
  if (!str) return '—'
  const d = new Date(str)
  if (isNaN(d.getTime())) return '—'
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
  if (!str) return '—'
  const d = new Date(str)
  if (isNaN(d.getTime())) return '—'
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
    return `Yearly on ${monthNames[monNum - 1]} ${ordinal(parseInt(dom, 10))} at ${time}`
  }
  if (dom !== '*' && dow === '*') {
    if (dom === 'L') return `Monthly on last day at ${time}`
    return `Monthly on ${ordinal(parseInt(dom, 10))} at ${time}`
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

/** Extract a failure reason from a job run's log */
export function getFailureReason(run) {
  if (run.status !== 'failed' && run.status !== 'error') return null
  if (!run.log) return 'Unknown error'
  try {
    const items = JSON.parse(run.log)
    if (Array.isArray(items)) {
      const failed = items.filter(i => i.status === 'error' || i.status === 'failed')
      if (failed.length > 0 && failed[0].error) return failed[0].error
      if (failed.length > 0) return `${failed.length} item(s) failed`
    }
  } catch {
    const lines = run.log.split('\n').filter(l => l.toLowerCase().includes('error') || l.toLowerCase().includes('fail'))
    if (lines.length > 0) return lines[0].substring(0, 120)
  }
  return 'Backup failed — expand for details'
}

/** Format seconds into human-readable duration (e.g. "11m 4s", "2h 15m") */
export function formatDuration(seconds) {
  if (seconds == null || seconds < 0) return '—'
  const sec = Math.round(seconds)
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

/** Format a start/end date pair into human-readable duration */
export function formatDurationFromDates(startedAt, completedAt) {
  if (!startedAt || !completedAt) return '—'
  const start = new Date(startedAt)
  const end = new Date(completedAt)
  if (isNaN(start.getTime()) || isNaN(end.getTime())) return '—'
  return formatDuration((end - start) / 1000)
}

/** Format bytes/seconds into human-readable speed (e.g. "31.2 MB/s") */
export function formatSpeed(bytes, seconds) {
  if (!bytes || !seconds || seconds === 0) return null;
  const bps = bytes / seconds;
  const k = 1024;
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  const i = Math.min(Math.floor(Math.log(bps) / Math.log(k)), units.length - 1);
  return parseFloat((bps / Math.pow(k, i)).toFixed(1)) + ' ' + units[i];
}
