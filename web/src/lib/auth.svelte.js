/**
 * Auth stub — no-op placeholder.
 *
 * API key authentication is enforced server-side by the APIKeyAuth
 * middleware (internal/api/middleware.go). The browser UI is always
 * exempt because it connects via loopback; only remote / LAN clients
 * must supply the X-API-Key header.
 *
 * These exports exist so that future client-side auth features can
 * import from a single module without a refactor.
 */

export function getApiKey() {
  return ''
}

export function isAuthenticated() {
  return true
}

export async function checkAuthStatus() {
  // No-op: auth status is driven by the API key settings endpoint.
}
