/**
 * The API key for a browser reaching the daemon directly from another machine,
 * such as a Docker replica's own UI. The Unraid plugin proxy and a browser on
 * the daemon's host don't need one.
 *
 * Kept in sessionStorage, so it lasts for this tab only.
 */
const STORAGE_KEY = 'vault-api-key'

let authRequired = $state(false)

/** @returns {string} the key for this tab, or '' (storage can be blocked) */
export function getApiKey() {
  try {
    return sessionStorage.getItem(STORAGE_KEY) || ''
  } catch {
    return ''
  }
}

/** @param {string} key */
export function setApiKey(key) {
  try {
    sessionStorage.setItem(STORAGE_KEY, key)
    return true
  } catch {
    return false
  }
}

/**
 * Whether this browser reaches the daemon with a key instead of from the Vault
 * host. The daemon refuses such a client when it reveals or changes the key.
 */
export function isRemoteKeyClient() {
  return getApiKey() !== ''
}

/** Whether the daemon has refused this browser and the UI should ask for a key. */
export function isAuthRequired() {
  return authRequired
}

export function markAuthRequired() {
  authRequired = true
}
