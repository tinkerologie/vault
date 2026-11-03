// Each dispatched route's page heading, the one source for per-page tab titles.
// A route App.svelte doesn't recognise renders Dashboard, so it gets that title.
const PAGE_HEADINGS = {
  '/': 'Dashboard',
  '/jobs': 'Backup Jobs',
  '/storage': 'Storage Destinations',
  '/history': 'Backup & Restore History',
  '/restore': 'Restore',
  '/logs': 'Logs',
  '/replication': 'Replication',
  '/recovery': 'Disaster Recovery',
  '/recover': 'Recover Vault',
  '/anomalies': 'Anomalies',
  '/settings': 'Settings',
}

export const ROUTES = Object.keys(PAGE_HEADINGS)

/** @param {string} route */
export function pageTitle(route) {
  const heading = Object.hasOwn(PAGE_HEADINGS, route) ? PAGE_HEADINGS[route] : PAGE_HEADINGS['/']
  return `${heading} – Vault`
}
