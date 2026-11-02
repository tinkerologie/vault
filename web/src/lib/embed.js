// SPA side of the embedded Backups view (#526). The peer is
// plugin/pages/include/embed-host.js: this side reports its content height and
// receives the part of the frame the user can actually see.

const CHILD_SOURCE = 'vault-embed'
const HOST_SOURCE = 'vault-embed-host'
const MAX_LENGTH = 1000000

let visibleTop = 0

/** @param {unknown} value */
function isLength(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= MAX_LENGTH
}

/**
 * The viewport carried by a message, or null unless it's well formed and came
 * from the parent window on this origin.
 * @param {MessageEvent} event
 * @param {Window} win
 */
export function parseViewportMessage(event, win) {
  if (event.origin !== win.location.origin || event.source !== win.parent) return null
  const data = event.data
  if (!data || typeof data !== 'object') return null
  if (data.source !== HOST_SOURCE || data.type !== 'viewport') return null
  if (!isLength(data.top) || !isLength(data.height)) return null
  return { top: data.top, height: data.height }
}

/** @param {Window} win @param {Record<string, unknown>} message */
function postToHost(win, message) {
  win.parent.postMessage({ source: CHILD_SOURCE, ...message }, win.location.origin)
}

/**
 * Starts embedded presentation: flags the root element before first paint and
 * keeps the host told about the content height, whatever changed it.
 * @param {Window & typeof globalThis} [win]
 * @returns {() => void} stops the channel
 */
export function startEmbeddedPresentation(win = window) {
  const doc = win.document
  const root = doc.documentElement
  const app = doc.getElementById('app')
  root.classList.add('vault-embedded')
  visibleTop = 0

  let lastHeight = -1
  let heard = false

  function report(force = false) {
    if (!app) return
    // scrollHeight also counts positioned content that spills out of #app's box.
    const height = Math.ceil(Math.max(app.offsetHeight, app.scrollHeight))
    if (!force && height === lastHeight) return
    lastHeight = height
    postToHost(win, { type: 'size', height })
  }

  /** @param {MessageEvent} event */
  function onMessage(event) {
    const viewport = parseViewportMessage(event, win)
    if (!viewport) return
    visibleTop = viewport.top
    root.style.setProperty('--vault-visible-top', `${viewport.top}px`)
    root.style.setProperty('--vault-visible-height', `${viewport.height}px`)
    // The host may have attached after the first report, which it then missed.
    if (!heard) {
      heard = true
      report(true)
    }
  }

  win.addEventListener('message', onMessage)
  const observer = new win.ResizeObserver(() => report())
  if (app) observer.observe(app)
  report()

  return () => {
    win.removeEventListener('message', onMessage)
    observer.disconnect()
  }
}

/**
 * Asks the host to scroll a position in the frame to the top of what's visible.
 * The frame itself never scrolls.
 * @param {number} top
 * @param {Window} [win]
 */
export function scrollHostTo(top, win = window) {
  postToHost(win, { type: 'scroll', top: Math.max(0, Math.round(top)) })
}

/**
 * Brings the frame's top back on screen if the page has scrolled past it, so a
 * new route doesn't open partway down.
 * @param {Window} [win]
 */
export function revealFrameTop(win = window) {
  if (visibleTop > 0) scrollHostTo(0, win)
}
