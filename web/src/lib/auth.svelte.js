/** Authentication state management for API key auth */

const STORAGE_KEY = 'vault_api_key'

let apiKey = $state(localStorage.getItem(STORAGE_KEY) || '')
let authRequired = $state(false)
let checked = $state(false)

export function getApiKey() {
  return apiKey
}

export function isAuthRequired() {
  return authRequired
}

export function isAuthChecked() {
  return checked
}

export function isAuthenticated() {
  // Web UI users are always authenticated via origin-based bypass.
  // API key is only needed for 3rd-party integrations (curl, HA, etc.)
  return !authRequired || apiKey.length > 0
}

export function setApiKey(key) {
  apiKey = key
  if (key) {
    localStorage.setItem(STORAGE_KEY, key)
  } else {
    localStorage.removeItem(STORAGE_KEY)
  }
}

export function clearApiKey() {
  apiKey = ''
  localStorage.removeItem(STORAGE_KEY)
}

/**
 * Check if the server requires authentication for the Web UI.
 * Calls GET /api/v1/auth/status (unauthenticated endpoint).
 * The server returns ui_auth_required (always false for browser SPA)
 * and auth_required (true if API key exists, for 3rd-party clients).
 */
export async function checkAuthStatus() {
  try {
    const res = await fetch('/api/v1/auth/status')
    const data = await res.json()
    // Use ui_auth_required for the SPA — the server never requires
    // browser-based users to authenticate (origin-based bypass).
    authRequired = data.ui_auth_required === true
  } catch {
    // If we can't reach the server, assume no auth required
    authRequired = false
  }
  checked = true
}

/**
 * Validate a key by making an authenticated health check.
 * Returns true if the key is valid.
 */
export async function validateApiKey(key) {
  try {
    const res = await fetch('/api/v1/health', {
      headers: { Authorization: `Bearer ${key}` },
    })
    return res.ok
  } catch {
    return false
  }
}
