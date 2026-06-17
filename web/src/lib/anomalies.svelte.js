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
