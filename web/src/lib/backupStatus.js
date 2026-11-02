// The Dashboard's one backup verdict (#527), and the health-tile wording that
// has to agree with it. Pure, so every state is testable without rendering.
//
// The server's health score only weighs items already in a job and recent run
// outcomes, so it can read 100 while discovered items are in no job at all.
// That is why the verdict never comes from the score alone.

const plural = (count, one, many = `${one}s`) => `${count} ${count === 1 ? one : many}`

/** Counts failed runs and runs that finished with warnings. */
export function countRunOutcomes(runs) {
  let failed = 0
  let warnings = 0
  for (const run of runs || []) {
    if (run.status === 'failed' || run.status === 'error') failed++
    else if (run.status === 'partial') warnings++
  }
  return { failed, warnings }
}

/**
 * True when the item list can't be trusted: a discovery request failed, or
 * returned "unavailable" for a type that jobs back up (Docker is down, say). An
 * unavailable type no job uses is normal, such as a server without VMs.
 */
export function isDiscoveryIncomplete(sources, summary) {
  const keys = [...(summary?.protected_keys || []), ...(summary?.pending_keys || [])]
  return Object.entries(sources).some(([type, source]) =>
    source?.failed || (source?.available === false && keys.some((key) => key.startsWith(`${type}:`))))
}

// Whether the server has any record of a backup having run.
function hasBackupHistory(summary) {
  return Boolean(summary.recent_failed || summary.recent_success || summary.last_success_at || summary.protected_items)
}

function verdict(state, tone, title, detail, issues = []) {
  return { state, tone, title, detail, issues, announcement: detail ? `${title}. ${detail}` : title }
}

function setupVerdict({ hasStorage, replica }) {
  if (replica) {
    return verdict('setup', 'info', 'No backup data on this replica yet', 'Backups appear here after replication syncs them.', [
      { key: 'replication', action: 'Open Replication', href: '/replication' },
    ])
  }
  if (!hasStorage) {
    return verdict('setup', 'info', 'Set up your first backup', 'Add a storage destination, then create a backup job.', [
      { key: 'storage', action: 'Add storage', href: '/storage' },
    ])
  }
  return verdict('setup', 'info', 'Create your first backup job', 'Your storage is ready. Choose what to back up and when.', [
    { key: 'jobs', action: 'Create a job', href: '/jobs' },
  ])
}

function attentionIssues({ failures, warnings, unprotected, total, summary, replica }) {
  const issues = []
  if (failures > 0) {
    issues.push({ key: 'failures', summary: plural(failures, 'recent failure'), action: 'Review failures', href: '/history' })
  }
  if (warnings > 0) {
    issues.push({ key: 'warnings', summary: `${plural(warnings, 'run')} finished with warnings`, action: 'Review warnings', href: '/history' })
  }
  // Without the summary nothing is known to be protected, so every item would
  // look unprotected. That is unknown, not a problem to report. A replica doesn't
  // discover items (api.js) and has no Jobs page to add them on.
  if (summary && !replica && unprotected > 0) {
    issues.push({ key: 'unprotected', summary: `${unprotected} of ${plural(total, 'item')} unprotected`, action: 'Add to a backup job', href: '/jobs' })
  }
  return issues
}

/**
 * @param {object} input
 * @param {boolean} [input.loading] First load hasn't finished.
 * @param {boolean} [input.error] The dashboard's data failed to load.
 * @param {boolean} [input.replica] Read-only replica: Jobs, Restore and Recovery are hidden.
 * @param {boolean} [input.hasStorage]
 * @param {boolean} [input.hasJobs]
 * @param {number} [input.failures] Recent failed runs.
 * @param {number} [input.warnings] Recent runs that finished with warnings.
 * @param {number} [input.unprotected] Items in no job and no restore point.
 * @param {number} [input.pending] Items in a job, waiting for their first backup.
 * @param {number} [input.protectedCount] Items in a restore point.
 * @param {number} [input.total] Items tracked on this server.
 * @param {object|null} [input.summary] GET /health/summary, or null if it failed.
 * @param {boolean} [input.incomplete] Another source the verdict relies on failed.
 */
export function deriveBackupStatus({
  loading = false, error = false, replica = false, hasStorage = false, hasJobs = false, failures = 0, warnings = 0,
  unprotected = 0, pending = 0, protectedCount = 0, total = 0, summary = null, incomplete = false,
}) {
  if (loading) return verdict('loading', 'neutral', 'Checking backup status…', '')
  if (error) {
    return verdict('error', 'danger', "Couldn't load backup status", "Vault can't confirm your backups right now.", [
      { key: 'logs', action: 'Open Logs', href: '/logs' },
    ])
  }
  if (!hasStorage || !hasJobs) return setupVerdict({ hasStorage, replica })

  const waiting = pending > 0 ? plural(pending, 'item') + ' waiting for first backup' : ''
  const unknown = !summary || incomplete
  const issues = attentionIssues({ failures, warnings, unprotected, total, summary, replica })
  if (issues.length > 0) {
    const parts = issues.map((issue) => issue.summary)
    if (waiting) parts.push(waiting)
    if (unknown) parts.push('some status data is unavailable')
    return verdict('attention', failures > 0 ? 'danger' : 'warning', 'Needs attention', parts.join(' · '), issues)
  }

  if (unknown) {
    return verdict('unknown', 'neutral', 'Backup health unavailable', "Some status data couldn't be loaded, so Vault can't confirm your backups.", [
      { key: 'history', action: 'View history', href: '/history' },
    ])
  }

  const anyProtected = Math.max(protectedCount, summary.protected_items || 0) > 0
  if (!anyProtected && (total > 0 || !hasBackupHistory(summary))) {
    const title = hasBackupHistory(summary) ? 'No items backed up yet' : 'No backups completed yet'
    const detail = waiting || 'Your first backup will run on schedule, or you can run it now.'
    const actions = replica ? [] : [{ key: 'waiting', action: 'View jobs', href: '/jobs' }]
    return verdict('waiting', 'info', title, detail, actions)
  }

  let detail = 'Recent backups completed.'
  if (waiting) detail = waiting
  else if (total > 0 && protectedCount === total) detail = total === 1 ? 'The 1 tracked item is protected.' : `All ${total} items protected.`
  return verdict('healthy', 'success', 'Backups healthy', detail)
}

/**
 * What identifies a change worth announcing: the state and which kinds of problem
 * exist. Count changes during a run stay silent. Null while nothing is known.
 */
export function announcementKey(status) {
  if (status.state === 'loading') return null
  return [status.state, ...status.issues.map((issue) => issue.key)].join('|')
}

function healthQualifiers({ unprotected = 0, pending = 0, warnings = 0 }) {
  const parts = []
  if (unprotected > 0) parts.push(`${unprotected} unprotected`)
  if (warnings > 0) parts.push(`${warnings} with warnings`)
  if (pending > 0) parts.push(`${pending} pending`)
  return parts
}

/** The health tile's sentence. Plain "healthy" only when nothing qualifies it. */
export function healthText(summary, counts = {}) {
  if (!summary) return 'Health unavailable'
  const failures = counts.failures ?? summary.recent_failed ?? 0
  if (failures > 0) return plural(failures, 'recent failure')
  // A fresh install scores 0 because nothing has completed yet, not because
  // anything went wrong, so "needs attention" there would be a false alarm.
  if (!hasBackupHistory(summary)) return 'No backups completed yet'
  const score = summary.health_score ?? 0
  const qualifiers = healthQualifiers(counts)
  if (score >= 80) return qualifiers.length ? ['Backups OK', ...qualifiers].join(' · ') : 'All backups healthy'
  if (score >= 50) return ['Backups mostly healthy', ...qualifiers].join(' · ')
  return 'Needs attention'
}

/** Colour category for the gauge. A high score with open problems isn't green. */
export function healthTone(summary, counts = {}) {
  if (!summary) return 'unknown'
  const score = summary.health_score ?? 0
  if (score < 50) return 'danger'
  if ((counts.failures ?? summary.recent_failed ?? 0) > 0) return 'warning'
  if (score < 80 || healthQualifiers(counts).length > 0) return 'warning'
  return 'success'
}

/** The Needs attention tile's caption. Claims full protection only when it's true. */
export function attentionCaption({ failures = 0, unprotected = 0, fullyProtected = false, known = true }) {
  if (failures > 0 || unprotected > 0) return `${plural(failures, 'recent failure')} · ${unprotected} unprotected`
  if (!known) return 'No failures · protection unknown'
  return fullyProtected ? 'No failures · all items protected' : 'No failures'
}
