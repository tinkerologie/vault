// Page filters live in the hash query (#/history?status=failed), the same
// convention the Dashboard's attention links already use (#693). Writing uses
// history.replaceState, so changing a filter never adds a Back step, while
// leaving the page and coming Back restores the URL, and with it the filters.
// No hashchange fires for a replaced entry, so the router doesn't re-run.

/**
 * The query for a set of filters, leaving out defaults and empty values. Keys
 * follow the order of `defaults`, then any others, so the URL stays stable.
 * @param {Record<string, unknown>} values
 * @param {Record<string, unknown>} defaults
 */
export function filterQuery(values, defaults = {}) {
  const keys = [...Object.keys(defaults), ...Object.keys(values).filter((key) => !(key in defaults))]
  const params = new URLSearchParams()
  for (const key of keys) {
    if (!(key in values)) continue
    const value = values[key]
    if (value === undefined || value === null || value === false || value === '') continue
    const text = String(value)
    if (key in defaults && text === String(defaults[key])) continue
    params.set(key, text)
  }
  return params.toString()
}

/**
 * The filters in the hash for `route`, or null when another route is showing.
 * @param {string} route Route path, such as '/history'.
 * @param {string} [hash] Defaults to the current location hash.
 */
export function readHashFilters(route, hash = window.location.hash) {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash
  const at = raw.indexOf('?')
  const path = at === -1 ? raw : raw.slice(0, at)
  if (path !== route) return null
  return new URLSearchParams(at === -1 ? '' : raw.slice(at + 1))
}

/**
 * Puts the filters in the URL without adding a history entry. Does nothing
 * while another route is showing, or when the URL already matches.
 * @param {string} route
 * @param {Record<string, unknown>} values
 * @param {Record<string, unknown>} [defaults]
 */
export function writeHashFilters(route, values, defaults = {}) {
  if (readHashFilters(route) === null) return
  const query = filterQuery(values, defaults)
  const next = `#${route}${query ? `?${query}` : ''}`
  if (window.location.hash === next) return
  window.history.replaceState(window.history.state, '', next)
}
