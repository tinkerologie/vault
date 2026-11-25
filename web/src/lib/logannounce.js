// Live log arrivals, announced at most once per interval (#686). The console
// itself is not a live region: every WebSocket line, and every older row the
// window renders while the reader scrolls, would otherwise be read out. One
// visually hidden status says "N new log entries" instead, batched so its text
// changes no more often than every ten seconds.

/** The announcement for `count` new entries. */
export function arrivalText(count) {
  return `${count} new log ${count === 1 ? 'entry' : 'entries'}`
}

/**
 * @param {object} opts
 * @param {(text: string) => void} opts.announce Sets the live region's text.
 * @param {number} [opts.interval] Minimum time between announcements, in ms.
 * @param {() => number} [opts.now]
 * @param {(fn: () => void, ms: number) => any} [opts.schedule]
 * @param {(handle: any) => void} [opts.cancel]
 */
export function createArrivalAnnouncer({
  announce,
  interval = 10000,
  now = () => Date.now(),
  schedule = (fn, ms) => setTimeout(fn, ms),
  cancel = (handle) => clearTimeout(handle),
}) {
  let pending = 0
  let last = -Infinity
  let timer = null

  function flush() {
    timer = null
    if (pending > 0) announce(arrivalText(pending))
    pending = 0
    last = now()
  }

  return {
    /** Records `count` live arrivals that match the console's filters. */
    add(count) {
      if (!(count > 0)) return
      pending += count
      if (timer !== null) return
      const wait = last + interval - now()
      if (wait <= 0) flush()
      else timer = schedule(flush, wait)
    },
    dispose() {
      if (timer !== null) cancel(timer)
      timer = null
      pending = 0
    },
  }
}
