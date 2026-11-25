import { afterEach, describe, expect, it, vi } from 'vitest'
import { shortcutModifier } from './shortcuts.js'

// The modifier shown in shortcut hints (#691): ⌘ on Apple devices, Ctrl elsewhere.

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('shortcutModifier', () => {
  it.each([
    [{ userAgentData: { platform: 'macOS' } }, '⌘'],
    [{ platform: 'MacIntel' }, '⌘'],
    [{ platform: 'iPad' }, '⌘'],
    [{ userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)' }, '⌘'],
    [{ userAgentData: { platform: 'Windows' } }, 'Ctrl+'],
    [{ platform: 'Linux x86_64' }, 'Ctrl+'],
    [{}, 'Ctrl+'],
  ])('%o -> %s', (nav, expected) => {
    vi.stubGlobal('navigator', nav)
    expect(shortcutModifier()).toBe(expected)
  })

  it('falls back to Ctrl without a navigator', () => {
    vi.stubGlobal('navigator', undefined)
    expect(shortcutModifier()).toBe('Ctrl+')
  })
})
