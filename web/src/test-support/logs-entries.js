// Plain factories for the Logs component tests (#509). Kept out of the
// .svelte.js store fixture so they are ordinary, non-reactive values.

let seq = 0

/**
 * Build `count` unified log entries, OLDEST FIRST (the store's order), all on
 * one day a few seconds apart, with distinct messages so the duplicate
 * collapsing in buildRows never folds them together.
 */
export function makeEntries(count, { start = 0, base = Date.parse('2026-10-20T12:00:00Z'), level = 'info' } = {}) {
  return Array.from({ length: count }, (_, i) => {
    const n = start + i
    seq++
    return {
      id: `e${n}-${seq}`,
      ts: new Date(base + n * 1000).toISOString(),
      level,
      message: `Log line ${n}`,
      type: 'activity',
      category: 'backup',
      jobName: n % 2 ? 'Nightly' : 'Weekly',
      jobId: n % 2 ? 1 : 2,
      runId: null,
      meta: null,
    }
  })
}

/** A promise with its resolve/reject handles exposed. */
export function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
