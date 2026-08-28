/**
 * Shared anomaly state – rune-backed, survives page navigations.
 *
 * WS handlers call the mutators below; components import getAnomalies()
 * and read reactive state via the returned getters.
 */

/** @type {any[]} */
let openList = $state([])

const TERMINAL_STATES = new Set(['resolved', 'expected'])

export function getAnomalies() {
  return {
    get openList() { return openList },
  }
}

/** Replace the full open list (called after a refetch). */
export function setOpenList(list) {
  openList = list || []
}

/** Handle anomaly.raised – prepend to the open list. */
export function handleAnomalyRaised(anomaly) {
  if (!anomaly) return
  // Avoid duplicates (idempotent on reconnect).
  if (openList.some(a => a.id === anomaly.id)) return
  openList = [anomaly, ...openList]
}

/** Handle anomaly.updated – replace by id; remove if terminal. */
export function handleAnomalyUpdated(anomaly) {
  if (!anomaly) return
  if (TERMINAL_STATES.has(anomaly.state)) {
    openList = openList.filter(a => a.id !== anomaly.id)
  } else {
    const idx = openList.findIndex(a => a.id === anomaly.id)
    if (idx >= 0) {
      openList = openList.map(a => (a.id === anomaly.id ? anomaly : a))
    } else {
      openList = [anomaly, ...openList]
    }
  }
}

/** Handle anomaly.bulk_resolved – no-op on shared state. The bulk_resolved
 *  event is a server-side change whose precise effect on the open list is
 *  not fully described by the event payload, so components that care refetch
 *  via their own bulk_resolved WS listeners. */
export function handleBulkResolved(/* _data */) {
  // intentionally a no-op
}

/** Handle anomaly.bulk_acked – remove acknowledged ids from the open list. */
export function handleBulkAcked(data) {
  if (!data?.ids?.length) return
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  const idSet = new Set(data.ids)
  openList = openList.filter(a => !idSet.has(a.id))
}

/** Handle baseline.updated – no structural change to anomaly list; callers
 *  may subscribe to their own baseline state; here we just expose a hook
 *  so the Jobs page badge can be notified. */
let baselineListeners = []

export function onBaselineUpdated(fn) {
  baselineListeners.push(fn)
  return () => { baselineListeners = baselineListeners.filter(l => l !== fn) }
}

export function notifyBaselineUpdated(data) {
  baselineListeners.forEach(fn => fn(data))
}

/**
 * Compute open anomaly counts per severity level for items matching a filter.
 *
 * @param {((a: any) => boolean) | { scope_kind?: string, scope_id?: number, job_run_id?: number, kind?: string, id?: number, runId?: number } | null} [filter]
 * @param {any[]} [customList] Optional list to evaluate against; defaults to shared openList.
 * @returns {{ critical: number, warning: number, info: number }}
 */
export function getAnomalyCounts(filter, customList) {
  const list = customList ?? openList
  const counts = { critical: 0, warning: 0, info: 0 }
  if (!Array.isArray(list)) return counts

  let predicate = () => true
  if (typeof filter === 'function') {
    predicate = filter
  } else if (filter && typeof filter === 'object') {
    const scopeKind = filter.scope_kind ?? filter.kind
    const scopeId = filter.scope_id ?? filter.id
    const runId = filter.job_run_id ?? filter.runId

    predicate = (a) => {
      if (!a) return false
      if (scopeKind !== undefined && a.scope_kind !== scopeKind) return false
      if (scopeId !== undefined && a.scope_id !== scopeId) return false
      if (runId !== undefined && a.job_run_id !== runId) return false
      return true
    }
  }

  for (const item of list) {
    if (!item) continue
    if (predicate(item)) {
      const sev = item.severity
      if (sev === 'critical' || sev === 'warning' || sev === 'info') {
        counts[sev]++
      }
    }
  }

  return counts
}

/**
 * Format a human-readable tooltip text explaining what the anomaly badges represent
 * and how to act on them.
 *
 * @param {{ critical?: number, warning?: number, info?: number }} counts
 * @returns {string}
 */
export function formatAnomalyTooltip(counts) {
  const parts = []
  if (counts?.critical) parts.push(`${counts.critical} critical`)
  if (counts?.warning) parts.push(`${counts.warning} warning`)
  if (counts?.info) parts.push(`${counts.info} info`)
  const countSummary = parts.join(', ') || '0 anomalies'
  return `Open anomalies (${countSummary}). Review on the Anomalies page to inspect details, mark as expected to update the baseline, or resolve.`
}
