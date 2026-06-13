import { api } from './api.js'

// Shared reactive feature flags. App nav and Dashboard read these; the Settings
// page writes them on save so the UI updates without a reload.
let anomalyEnabled = $state(true)
let replicationEnabled = $state(true)

export function getAnomalyEnabled() {
  return anomalyEnabled
}

export function getReplicationEnabled() {
  return replicationEnabled
}

export function setAnomalyEnabled(v) {
  anomalyEnabled = !!v
}

export function setReplicationEnabled(v) {
  replicationEnabled = !!v
}

// loadFeatureFlags fetches settings once (on app mount) and resolves the two
// flags. Fail open: a settings-fetch error keeps features visible. Replication
// derives from whether sources exist only when the setting is unset.
export async function loadFeatureFlags() {
  let settings
  try {
    settings = await api.getSettings()
  } catch {
    anomalyEnabled = true
    replicationEnabled = true
    return
  }

  anomalyEnabled = settings?.anomaly_detection_enabled !== 'false'

  const repl = settings?.replication_enabled
  if (repl === 'true') {
    replicationEnabled = true
  } else if (repl === 'false') {
    replicationEnabled = false
  } else {
    // Unset → derive from whether any replication sources exist.
    try {
      const sources = await api.listReplicationSources()
      replicationEnabled = Array.isArray(sources) && sources.length > 0
    } catch {
      replicationEnabled = false
    }
  }
}
