const replicaHiddenRoutes = new Set(['/jobs', '/restore', '/recovery', '/recover'])

// Anomalies is not redirected when detection is off: the page explains why and
// links to the setting, so deep links from notifications or docs still work (#695).
export function shouldRedirectRoute({ route, replicaMode, replicationEnabled }) {
  if (replicaMode && [...replicaHiddenRoutes].some(base => route === base || route.startsWith(`${base}/`))) return true
  return route === '/replication' && !replicaMode && !replicationEnabled
}
