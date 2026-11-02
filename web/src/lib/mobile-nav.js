// Mobile navigation: the bottom bar plus the More sheet (#530). The sheet is
// derived from the sidebar's filtered inventory, so a page can't be missing from
// both places, and feature flags and replica mode apply to it the same way.

/** Routes that sit under a navigation entry but aren't entries themselves. */
const CHILD_ROUTES = { '/recover': '/recovery' }

/**
 * Whether a mobile navigation entry owns the route.
 * @param {string} path
 * @param {string} route
 */
export function isMobileNavActive(path, route) {
  const owner = CHILD_ROUTES[route] ?? route
  return path === '/' ? owner === '/' : owner.startsWith(path)
}

/**
 * Entries the bottom bar doesn't show, in sidebar order.
 * @template {{ path: string }} T
 * @param {T[]} nav The already-filtered inventory.
 * @param {string[]} barPaths
 * @returns {T[]}
 */
export function moreNavItems(nav, barPaths) {
  return nav.filter((item) => !barPaths.includes(item.path))
}

/**
 * Whether the route belongs to a page in the More sheet.
 * @param {{ path: string, external?: boolean }[]} items
 * @param {string} route
 */
export function isMoreActive(items, route) {
  return items.some((item) => !item.external && isMobileNavActive(item.path, route))
}

/**
 * Groups entries into contiguous sections. A section appears only with at least
 * one visible entry, so hidden pages never leave an empty header behind.
 * @template {{ section?: string }} T
 * @param {T[]} items
 * @returns {{ section: string | null, items: T[] }[]}
 */
export function groupBySection(items) {
  /** @type {{ section: string | null, items: T[] }[]} */
  const groups = []
  /** @type {Record<string, { section: string | null, items: T[] }>} */
  const bySection = {}
  for (const item of items) {
    const key = item.section ?? '__none__'
    if (!bySection[key]) {
      bySection[key] = { section: item.section ?? null, items: [] }
      groups.push(bySection[key])
    }
    bySection[key].items.push(item)
  }
  return groups
}
