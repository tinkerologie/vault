// Setup for the jsdom `component` Vitest project (#509). Testing Library's
// auto-cleanup (unmounting rendered components) comes from svelteTesting()
// in vitest.config.js; this file only resets test doubles between tests.
import { afterEach, vi } from 'vitest'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})
