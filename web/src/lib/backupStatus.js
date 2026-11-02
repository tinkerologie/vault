// The Dashboard's one backup verdict (#527), and the health-tile wording that
// has to agree with it. Pure, so every state is testable without rendering.
//
// The server's health score only weighs items already in a job and recent run
// outcomes, so it can read 100 while discovered items are in no job at all.
// That is why the verdict never comes from the score alone. Below HEALTHY_SCORE
// neither the banner nor the health tile may call backups healthy.

const HEALTHY_SCORE = 80

const plural = (count, one, many = `${one}s`) => `${count} ${count === 1 ? one : many}`

/**
 * Counts the runs that need a look. A failed restore is a failure, but a skipped,
 * cancelled or partial restore says nothing about whether backups are running.
 */
export function countRunOutcomes(runs) {
  const counts = { failed: 0, warnings: 0, skipped: 0, cancelled: 0 }
  for (const run of runs || []) {
    if (run.status === 'failed' || run.status === 'error') counts.failed++
    else if ((run.run_type || 'backup') !== 'backup') continue
    else if (run.status === 'partial') counts.warnings++
    else if (run.status === 'skipped') counts.skipped++
    else if (run.status === 'cancelled') counts.cancelled++
  }
  return counts
}

/**
 * The run counts the verdict uses. The sample only covers ten jobs, so the
 * server's counters, which cover every job, decide; the sample can only add.
 */
export function mergeRunCounts(sample, summary) {
  return {
    failures: Math.max(sample.failed, summary?.recent_failed ?? 0),
    warnings: Math.max(sample.warnings, summary?.recent_partial ?? 0),
    skipped: Math.max(sample.skipped, summary?.recent_skipped ?? 0),
    cancelled: Math.max(sample.cancelled, summary?.recent_cancelled ?? 0),
  }
}

/** Names of destinations whose health check failed or whose breaker is open. */
export function unhealthyStorage(storage) {
  return (storage || []).filter((dest) => dest.last_health_check_status === 'failed' || dest.breaker_state === 'open').map((dest) => dest.name)
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
  return Boolean(ranInWindow(summary) || summary.last_success_at || summary.protected_items)
}

// Whether the score has any runs behind it. With run history purged it reflects
// nothing that happened, however low it reads.
function ranInWindow(summary) {
  return Boolean(summary.recent_failed || summary.recent_success || summary.recent_skipped || summary.recent_cancelled)
}

// A score that says something is wrong. Items not yet backed up score low too,
// and those are reported as waiting, not as a low score.
function isLowScore(summary) {
  return Boolean(summary?.protected_items) && ranInWindow(summary) && (summary.health_score ?? 0) < HEALTHY_SCORE
}

function storageSummary(names) {
  return names.length === 1 ? `Storage "${names[0]}" is unhealthy` : `${names.length} storage destinations are unhealthy`
}

function staleSummary(jobs) {
  if (jobs.length !== 1) return `${jobs.length} jobs have no recent backup`
  const { name, enabled, last_success_at: last } = jobs[0]
  return `${enabled ? 'Job' : 'Disabled job'} "${name}" last backed up ${String(last).slice(0, 10)}`
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

function attentionIssues({ failures, warnings, skipped, cancelled, unhealthy, stale, unprotected, total, summary, replica }) {
  const issues = []
  if (failures > 0) {
    issues.push({ key: 'failures', summary: plural(failures, 'recent failure'), action: 'Review failures', href: '/history' })
  }
  if (skipped > 0) {
    issues.push({ key: 'skipped', summary: `${plural(skipped, 'run')} skipped`, action: 'Review skipped runs', href: '/history' })
  }
  if (cancelled > 0) {
    issues.push({ key: 'cancelled', summary: `${plural(cancelled, 'run')} cancelled`, action: 'Review cancelled runs', href: '/history' })
  }
  if (warnings > 0) {
    issues.push({ key: 'warnings', summary: `${plural(warnings, 'run')} finished with warnings`, action: 'Review warnings', href: '/history' })
  }
  if (unhealthy.length > 0) {
    issues.push({ key: 'storage', summary: storageSummary(unhealthy), action: 'Check storage', href: '/storage' })
  }
  if (stale.length > 0) {
    // A replica has no Jobs page, but its History shows the same dates.
    issues.push({ key: 'stale', summary: staleSummary(stale), action: 'Review jobs', href: replica ? '/history' : '/jobs' })
  }
  // Without the summary nothing is known to be protected, so every item would
  // look unprotected. That is unknown, not a problem to report. A replica doesn't
  // discover items (api.js) and has no Jobs page to add them on.
  if (summary && !replica && unprotected > 0) {
    issues.push({ key: 'unprotected', summary: `${unprotected} of ${plural(total, 'item')} unprotected`, action: 'Add to a backup job', href: '/jobs' })
  }
  if (isLowScore(summary)) {
    issues.push({ key: 'score', summary: `Health score ${summary.health_score ?? 0} of 100`, action: 'View history', href: '/history' })
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
 * @param {number} [input.skipped] Recent runs skipped, such as by an open breaker.
 * @param {number} [input.cancelled] Recent runs cancelled.
 * @param {string[]} [input.unhealthy] Names of unhealthy storage destinations.
 * @param {number} [input.unprotected] Items in no job and no restore point.
 * @param {number} [input.pending] Items in a job, waiting for their first backup.
 * @param {number} [input.protectedCount] Items in a restore point.
 * @param {number} [input.total] Items tracked on this server.
 * @param {object|null} [input.summary] GET /health/summary, or null if it failed.
 * @param {boolean} [input.incomplete] Another source the verdict relies on failed.
 */
export function deriveBackupStatus({
  loading = false, error = false, replica = false, hasStorage = false, hasJobs = false, failures = 0, warnings = 0,
  skipped = 0, cancelled = 0, unhealthy = [], unprotected = 0, pending = 0, protectedCount = 0, total = 0, summary = null, incomplete = false,
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
  const stale = summary?.stale_jobs || []
  const issues = attentionIssues({ failures, warnings, skipped, cancelled, unhealthy, stale, unprotected, total, summary, replica })
  if (issues.length > 0) {
    const parts = issues.map((issue) => issue.summary)
    if (waiting) parts.push(waiting)
    if (unknown) parts.push('some status data is unavailable')
    return verdict('attention', failures > 0 || unhealthy.length > 0 ? 'danger' : 'warning', 'Needs attention', parts.join(' · '), issues)
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

// Problems the tile can't soften to "Backups OK".
function healthProblems({ skipped = 0, cancelled = 0, unhealthy = 0 }, summary) {
  const parts = []
  if (skipped > 0) parts.push(`${skipped} skipped`)
  if (cancelled > 0) parts.push(`${cancelled} cancelled`)
  if (unhealthy > 0) parts.push('storage unhealthy')
  const stale = summary?.stale_jobs?.length ?? 0
  if (stale > 0) parts.push(`${stale} overdue`)
  return parts
}

function healthQualifiers({ unprotected = 0, pending = 0, warnings = 0, incomplete = false }) {
  const parts = []
  if (unprotected > 0) parts.push(`${unprotected} unprotected`)
  if (warnings > 0) parts.push(`${warnings} with warnings`)
  if (pending > 0) parts.push(`${pending} pending`)
  if (incomplete) parts.push('some data unavailable')
  return parts
}

// Restore points exist but no runs are on record, so the score means nothing.
function isScoreUnassessed(summary) {
  return Boolean(summary.protected_items) && !ranInWindow(summary)
}

/**
 * The health tile's sentence. Plain "healthy" only when nothing qualifies it, and
 * it uses the banner's words for anything the banner flags (#527).
 */
export function healthText(summary, counts = {}) {
  if (!summary) return 'Health unavailable'
  const failures = counts.failures ?? summary.recent_failed ?? 0
  if (failures > 0) return plural(failures, 'recent failure')
  const problems = healthProblems(counts, summary)
  const qualifiers = healthQualifiers(counts)
  if (problems.length > 0) return ['Needs attention', ...problems, ...qualifiers].join(' · ')
  // A fresh install scores 0 because nothing has completed yet, not because
  // anything went wrong, so "needs attention" there would be a false alarm.
  if (!hasBackupHistory(summary)) return 'No backups completed yet'
  if (isScoreUnassessed(summary)) return ['Backups OK', 'no recent runs', ...qualifiers].join(' · ')
  if (summary.protected_items === 0 && ranInWindow(summary)) return ['No items backed up yet', ...qualifiers].join(' · ')
  if ((summary.health_score ?? 0) < HEALTHY_SCORE) return ['Needs attention', ...qualifiers].join(' · ')
  return qualifiers.length ? ['Backups OK', ...qualifiers].join(' · ') : 'All backups healthy'
}

/** Colour category for the gauge. A high score with open problems isn't green. */
export function healthTone(summary, counts = {}) {
  if (!summary) return 'unknown'
  if (isScoreUnassessed(summary)) return 'warning'
  const score = summary.health_score ?? 0
  if (score < 50) return 'danger'
  if ((counts.failures ?? summary.recent_failed ?? 0) > 0) return 'warning'
  if (score < HEALTHY_SCORE || healthQualifiers(counts).length > 0 || healthProblems(counts, summary).length > 0) return 'warning'
  return 'success'
}

/** The Needs attention tile's caption. Claims full protection only when it's true. */
export function attentionCaption({ failures = 0, unprotected = 0, other = 0, fullyProtected = false, known = true }) {
  const others = other > 0 ? ` · ${plural(other, 'other issue')}` : ''
  if (failures > 0 || unprotected > 0) return `${plural(failures, 'recent failure')} · ${unprotected} unprotected${others}`
  if (other > 0) return `No failures${others}`
  if (!known) return 'No failures · protection unknown'
  return fullyProtected ? 'No failures · all items protected' : 'No failures'
}
