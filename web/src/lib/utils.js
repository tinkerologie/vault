/** Shared utility functions */

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
  return d.toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
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
  if (!cron) return '—'
  const parts = cron.trim().split(/\s+/)
  if (parts.length !== 5) return cron
  const [min, hr, dom, , dow] = parts
  const time = `${hr.padStart(2, '0')}:${min.padStart(2, '0')}`
  if (dom !== '*' && dow === '*') return `Monthly on ${ordinal(parseInt(dom))} at ${time}`
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
