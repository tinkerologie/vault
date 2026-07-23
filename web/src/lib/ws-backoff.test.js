import { describe, expect, it } from 'vitest'

import { backoffDelay, RECONNECT_BASE_MS, RECONNECT_MAX_MS } from './ws-backoff.js'

describe('backoffDelay', () => {
  it('never exceeds the cap, even at absurdly high attempt counts', () => {
    // rand() => 1 yields the maximum possible delay for a given attempt count.
    for (const attempts of [0, 1, 5, 10, 50, 1000]) {
      expect(backoffDelay(attempts, () => 1)).toBeLessThanOrEqual(RECONNECT_MAX_MS)
    }
  })

  it('grows exponentially before the cap and clamps after', () => {
    expect(backoffDelay(0, () => 1)).toBe(RECONNECT_BASE_MS)      // 1000
    expect(backoffDelay(1, () => 1)).toBe(RECONNECT_BASE_MS * 2)  // 2000
    expect(backoffDelay(2, () => 1)).toBe(RECONNECT_BASE_MS * 4)  // 4000
    // 1000 * 2**5 = 32000 → clamped to the 30000 ceiling.
    expect(backoffDelay(5, () => 1)).toBe(RECONNECT_MAX_MS)
    expect(backoffDelay(100, () => 1)).toBe(RECONNECT_MAX_MS)
  })

  it('applies full jitter within [0, cap]', () => {
    expect(backoffDelay(10, () => 0)).toBe(0)
    expect(backoffDelay(10, () => 0.5)).toBe(RECONNECT_MAX_MS / 2)
  })
})
