/** Minimal hash-based SPA router for Svelte 5 */

import { createNavigationGuard } from './unsaved.js'

let rawHash = $state(readRawHash())
let current = $derived(routePath(rawHash))

function withoutQuery(raw) {
  const qIdx = raw.indexOf('?')
  return qIdx === -1 ? raw : raw.slice(0, qIdx)
}

// The page is the first path segment: '#/settings/security' renders Settings,
// and the rest ('security') is the page's own sub-route, read with
// getSubpath(). Keeping the route at the page level means a sub-route change
// neither remounts the page nor counts as a route change for focus (#712).
function routePath(raw) {
  const path = withoutQuery(raw)
  const slash = path.indexOf('/', 1)
  return slash === -1 ? path : path.slice(0, slash)
}

function subPath(raw) {
  const path = withoutQuery(raw)
  const slash = path.indexOf('/', 1)
  return slash === -1 ? '' : path.slice(slash + 1)
}

// Every route change goes through one guard (#719): navigate(), in-app links,
// Back/Forward and typed URLs. A page with unsaved work can ask before the
// route leaves it with onBeforeLeave().
const nav = typeof window === 'undefined'
  ? null
  : createNavigationGuard({
      current: () => rawHash,
      commit: (raw) => { rawHash = raw },
      pageOf: routePath,
    })

if (nav) {
  // Under HMR the module is re-evaluated with a fresh `rawHash` $state.
  // Stop the previous evaluation's listeners (if any) before starting this
  // one, so exactly one guard – bound to the *current* module – listens.
  const w = /** @type {any} */ (window)
  if (typeof w.__vaultRouterStop === 'function') w.__vaultRouterStop()
  nav.start()
  w.__vaultRouterStop = nav.stop

  // Bind `import.meta.hot` to a local before use. A statement that *starts*
  // with `import` is parsed as an import declaration by CodeQL's JavaScript
  // extractor, which then fails on the `.` of `import.meta` and reports the
  // whole file as a syntax error. Reading it in expression position avoids
  // that, so the router stays in scope for scanning. Vite still replaces
  // `import.meta.hot` with undefined in production, so the block is dead code
  // there and drops out of the bundle as before.
  // @ts-ignore – Vite injects `import.meta.hot` only in dev builds.
  const hot = import.meta.hot
  if (hot) {
    hot.dispose(() => {
      nav.stop()
      if (w.__vaultRouterStop === nav.stop) w.__vaultRouterStop = null
    })
  }
}

/**
 * Go to a route. Waits for the page's leave guard (if any) when the route
 * would leave the page. `replace` swaps the current history entry instead of
 * adding one; it is meant for sub-routes of the current page and never asks.
 * @param {string} path e.g. '/jobs' or '/settings/security'
 * @param {{ replace?: boolean }} [opts]
 * @returns {Promise<void>}
 */
export function navigate(path, opts) {
  return nav ? nav.navigate(path, opts) : Promise.resolve()
}

/**
 * Ask fn(to, from) before the route leaves the current page by any means
 * (links, the command palette, Back/Forward). fn returns, or resolves to,
 * true to leave; on false the page stays and the URL returns to it without
 * a new history entry. Moving between the page's own sub-routes never asks.
 * Returns a function that removes the guard.
 * @param {(to: string, from: string) => boolean | Promise<boolean>} fn
 */
export function onBeforeLeave(fn) {
  return nav ? nav.setGuard(fn) : () => {}
}

export function getRoute() {
  return current
}

/** The part of the path after the page, e.g. 'security' for '#/settings/security'. */
export function getSubpath() {
  return subPath(rawHash)
}

/** Get the raw hash including query params */
export function getRawHash() {
  return rawHash
}

function readRawHash() {
  return typeof window === 'undefined' ? '/' : window.location.hash.slice(1) || '/'
}
