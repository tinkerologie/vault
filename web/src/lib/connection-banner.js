// The connection-lost banner (#686). A brief drop, such as a daemon restart,
// shows nothing; a lasting one shows the banner once and keeps it through every
// retry, so it neither flickers nor re-announces itself.

export const CONNECTION_BANNER_DELAY_MS = 3000

/**
 * Whether a live-connection status counts as lost. 'connecting' is a first
 * connect, and 'polling' is healthy: neither is trouble.
 * @param {string} status
 */
export function isConnectionLost(status) {
  return status === 'disconnected' || status === 'reconnecting'
}

/**
 * @param {(shown: boolean) => void} onchange called only when visibility changes
 * @param {number} [delayMs]
 */
export function createConnectionBanner(onchange, delayMs = CONNECTION_BANNER_DELAY_MS) {
  let shown = false
  /** @type {ReturnType<typeof setTimeout> | null} */
  let timer = null

  const clear = () => {
    if (timer) clearTimeout(timer)
    timer = null
  }

  return {
    /** @param {boolean} lost */
    update(lost) {
      if (lost) {
        if (shown || timer) return
        timer = setTimeout(() => {
          timer = null
          shown = true
          onchange(true)
        }, delayMs)
        return
      }
      clear()
      if (shown) {
        shown = false
        onchange(false)
      }
    },
    dispose() {
      clear()
    },
  }
}
