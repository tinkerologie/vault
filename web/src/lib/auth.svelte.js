/**
 * Auth module stub — API key authentication has been removed.
 * Exports are kept as no-ops so existing imports don't break during cleanup.
 */

export function getApiKey() {
  return ''
}

export function isAuthenticated() {
  return true
}

export async function checkAuthStatus() {
  // No-op: auth is no longer required.
}
