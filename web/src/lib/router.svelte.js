/** Minimal hash-based SPA router for Svelte 5 */

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

function handleHashChange() {
  rawHash = readRawHash()
}

if (typeof window !== 'undefined') {
  // Under HMR the module is re-evaluated and `handleHashChange` closes over a
  // fresh `current` $state. Remove the previously registered handler (if any)
  // before adding the new one so exactly one active listener – bound to the
  // *current* module evaluation – is registered at all times.
  const w = /** @type {any} */ (window)
  if (typeof w.__vaultRouterHandler === 'function') {
    window.removeEventListener('hashchange', w.__vaultRouterHandler)
  }
  window.addEventListener('hashchange', handleHashChange)
  w.__vaultRouterHandler = handleHashChange

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
      window.removeEventListener('hashchange', handleHashChange)
      if (w.__vaultRouterHandler === handleHashChange) {
        w.__vaultRouterHandler = null
      }
    })
  }
}

export function navigate(path) {
  window.location.hash = '#' + path
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
