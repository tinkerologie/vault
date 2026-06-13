import { api } from './api.js'

// Shared reactive feature flags. App nav and Dashboard read these; the Settings
// page writes them on save so the UI updates without a reload.
let anomalyEnabled = $state(true)
let replicationEnabled = $state(true)
// featureFlagsLoaded gates consumers (e.g. App's direct-URL guard) so they
// don't act on the optimistic defaults before the real settings have loaded.
let featureFlagsLoaded = $state(false)

export function getAnomalyEnabled() {
  return anomalyEnabled
}

export function getReplicationEnabled() {
  return replicationEnabled
}

export function getFeatureFlagsLoaded() {
  return featureFlagsLoaded
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
  } catch (e) {
    console.warn('loadFeatureFlags: settings fetch failed, defaulting features to visible:', e)
    anomalyEnabled = true
    replicationEnabled = true
    featureFlagsLoaded = true
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
    } catch (e) {
      console.warn('loadFeatureFlags: replication sources fetch failed, defaulting to hidden:', e)
      replicationEnabled = false
    }
  }
  featureFlagsLoaded = true
}
