/**
 * The tab a keydown should select in a horizontal tablist (WAI-ARIA tabs pattern),
 * or null when the key isn't one the tablist handles.
 * @param {{ key: string, altKey?: boolean, ctrlKey?: boolean, metaKey?: boolean, shiftKey?: boolean }} e
 * @param {string[]} ids
 * @param {string} current
 * @returns {string | null}
 */
export function tabFromKey(e, ids, current) {
  if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return null
  const i = ids.indexOf(current)
  switch (e.key) {
    case 'ArrowRight': return ids[(i + 1) % ids.length]
    case 'ArrowLeft': return ids[(i - 1 + ids.length) % ids.length]
    case 'Home': return ids[0]
    case 'End': return ids[ids.length - 1]
    default: return null
  }
}
