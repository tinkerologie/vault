/** Minimal hash-based SPA router for Svelte 5 */

let current = $state(getHash())

function getHash() {
  const raw = window.location.hash.slice(1) || '/'
  // Strip query params for route matching
  const qIdx = raw.indexOf('?')
  return qIdx === -1 ? raw : raw.slice(0, qIdx)
}

function handleHashChange() {
  current = getHash()
}

if (typeof window !== 'undefined') {
  // Guard against duplicate registrations under HMR.
  const w = /** @type {any} */ (window)
  if (!w.__vaultRouterInit) {
    window.addEventListener('hashchange', handleHashChange)
    w.__vaultRouterInit = true
  }
}

export function navigate(path) {
  window.location.hash = '#' + path
}

export function getRoute() {
  return current
}

/** Get the raw hash including query params */
export function getRawHash() {
  return window.location.hash.slice(1) || '/'
}
