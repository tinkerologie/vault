const replicaHiddenRoutes = new Set(['/jobs', '/restore', '/recovery', '/recover'])

export function shouldRedirectRoute({ route, replicaMode, anomalyEnabled, replicationEnabled }) {
  if (replicaMode && [...replicaHiddenRoutes].some(base => route === base || route.startsWith(`${base}/`))) return true
  if (route === '/anomalies' && !anomalyEnabled) return true
  return route === '/replication' && !replicaMode && !replicationEnabled
}
