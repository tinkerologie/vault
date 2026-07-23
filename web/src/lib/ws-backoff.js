// Bounded exponential backoff for WebSocket reconnection. Extracted as a pure,
// runes-free module so the delay math can be unit-tested under the plain-node
// vitest environment (ws.svelte.js itself uses $state and can't be imported
// there). This is the core of the #250 fix that replaced the old fixed 3s
// uncapped retry loop.

export const RECONNECT_BASE_MS = 1000
export const RECONNECT_MAX_MS = 30000

/**
 * Full-jitter exponential backoff: returns a delay in [0, cap], where the cap
 * grows as base * 2**attempts and is clamped to RECONNECT_MAX_MS. Full jitter
 * spreads reconnecting clients so they don't stampede after a daemon restart.
 * @param {number} attempts  reconnect attempts so far (0-based)
 * @param {() => number} rand random source in [0,1) — injectable for tests
 * @returns {number} delay in milliseconds
 */
export function backoffDelay(attempts, rand = Math.random) {
  const cap = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** attempts)
  return Math.round(rand() * cap)
}
