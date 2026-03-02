/** Minimal hash-based SPA router for Svelte 5 */

let current = $state(getHash())

function getHash() {
  return window.location.hash.slice(1) || '/'
}

function handleHashChange() {
  current = getHash()
}

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', handleHashChange)
}

export function navigate(path) {
  window.location.hash = '#' + path
}

export function getRoute() {
  return current
}
