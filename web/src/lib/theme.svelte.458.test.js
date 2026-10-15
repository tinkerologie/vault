import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Theme store tests (issue #458). The node test env has no DOM, so <html>'s
// classList, localStorage and matchMedia are faked. The module keeps state at
// module scope, so each test imports a fresh copy.

function fakeStorage(initial = {}, { failSet = false, failAll = false } = {}) {
  const data = { ...initial }
  return {
    data,
    getItem: vi.fn((k) => {
      if (failAll) throw new Error('SecurityError')
      return k in data ? data[k] : null
    }),
    setItem: vi.fn((k, v) => {
      if (failAll || failSet) throw new Error('QuotaExceededError')
      data[k] = String(v)
    }),
    removeItem: vi.fn((k) => {
      if (failAll) throw new Error('SecurityError')
      delete data[k]
    }),
  }
}

function fakeMediaQuery(matches) {
  const listeners = new Set()
  return {
    matches,
    addEventListener: vi.fn((_, fn) => listeners.add(fn)),
    removeEventListener: vi.fn((_, fn) => listeners.delete(fn)),
    fire(next) {
      this.matches = next
      listeners.forEach((fn) => fn())
    },
    listeners,
  }
}

let classes
let storage
let mq
let theme

async function setup({ stored = {}, storageOpts = {}, prefersDark = true, matchMedia } = {}) {
  vi.resetModules()
  classes = new Set()
  vi.stubGlobal('document', {
    documentElement: {
      classList: {
        toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
        add: (c) => classes.add(c),
        remove: (c) => classes.delete(c),
      },
    },
  })
  storage = fakeStorage(stored, storageOpts)
  vi.stubGlobal('localStorage', storage)
  mq = fakeMediaQuery(prefersDark)
  vi.stubGlobal('window', { matchMedia: matchMedia || vi.fn(() => mq) })
  theme = await import('./theme.svelte.js')
  return theme
}

beforeEach(() => {
  classes = new Set()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('initTheme', () => {
  it('defaults to the default style in system mode, following a dark OS preference', async () => {
    await setup()
    theme.initTheme()
    expect(theme.getStyle()).toBe('default')
    expect(theme.getMode()).toBe('system')
    expect(theme.getIsDark()).toBe(true)
    expect(theme.getIsThemed()).toBe(false)
    expect(theme.getIsRetro()).toBe(false)
    expect(classes.has('dark')).toBe(true)
  })

  it('follows a light OS preference and reacts to OS changes in system mode', async () => {
    await setup({ prefersDark: false })
    theme.initTheme()
    expect(theme.getIsDark()).toBe(false)
    expect(classes.has('dark')).toBe(false)
    mq.fire(true)
    expect(theme.getIsDark()).toBe(true)
    expect(classes.has('dark')).toBe(true)
  })

  it('restores a persisted style and mode', async () => {
    await setup({ stored: { 'vault-style': '8bit', 'vault-mode': 'light' } })
    theme.initTheme()
    expect(theme.getStyle()).toBe('8bit')
    expect(theme.getMode()).toBe('light')
    expect(theme.getIsDark()).toBe(false)
    expect(theme.getIsThemed()).toBe(true)
    expect(classes.has('theme-8bit')).toBe(true)
  })

  it('ignores invalid persisted values', async () => {
    await setup({ stored: { 'vault-style': 'neon', 'vault-mode': 'dim' } })
    theme.initTheme()
    expect(theme.getStyle()).toBe('default')
    expect(theme.getMode()).toBe('system')
  })

  it('degrades to the dark default when matchMedia throws', async () => {
    await setup({ prefersDark: false, matchMedia: vi.fn(() => { throw new Error('blocked') }) })
    theme.initTheme()
    expect(theme.getIsDark()).toBe(true)
  })

  it('detaches the previous media-query listener when re-initialised', async () => {
    await setup()
    theme.initTheme()
    const first = mq
    theme.initTheme()
    expect(first.removeEventListener).toHaveBeenCalledWith('change', expect.any(Function))
  })

  it('swallows removeEventListener failures on re-init', async () => {
    await setup()
    theme.initTheme()
    mq.removeEventListener.mockImplementation(() => { throw new Error('nope') })
    expect(() => theme.initTheme()).not.toThrow()
  })

  it('does not throw when localStorage is entirely blocked', async () => {
    await setup({ stored: { 'vault-theme': 'dark' }, storageOpts: { failAll: true } })
    expect(() => theme.initTheme()).not.toThrow()
    expect(theme.getStyle()).toBe('default')
  })
})

describe('legacy vault-theme migration', () => {
  const cases = [
    ['light', 'default', 'light'],
    ['dark', 'default', 'dark'],
    ['system', 'default', 'system'],
    ['retro', '16bit', 'light'],
    ['retro-dark', '16bit', 'dark'],
  ]
  it.each(cases)('maps %s to style=%s mode=%s and retires the old key', async (old, style, mode) => {
    await setup({ stored: { 'vault-theme': old } })
    theme.initTheme()
    expect(theme.getStyle()).toBe(style)
    expect(theme.getMode()).toBe(mode)
    expect(storage.data['vault-style']).toBe(style)
    expect(storage.data['vault-mode']).toBe(mode)
    expect(storage.data['vault-theme']).toBeUndefined()
  })

  it('applies the migrated value in memory but keeps the legacy key when persistence fails', async () => {
    await setup({ stored: { 'vault-theme': 'retro-dark' }, storageOpts: { failSet: true } })
    theme.initTheme()
    expect(theme.getStyle()).toBe('16bit')
    expect(theme.getMode()).toBe('dark')
    expect(storage.data['vault-theme']).toBe('retro-dark')
  })

  it('drops an unrecognised legacy value', async () => {
    await setup({ stored: { 'vault-theme': 'purple' } })
    theme.initTheme()
    expect(storage.data['vault-theme']).toBeUndefined()
    expect(theme.getStyle()).toBe('default')
  })
})

describe('setStyle / setMode', () => {
  it('switches style classes and persists the choice', async () => {
    await setup()
    theme.initTheme()
    theme.setStyle('1bit')
    expect(classes.has('theme-1bit')).toBe(true)
    expect(storage.data['vault-style']).toBe('1bit')
    theme.setStyle('16bit')
    expect(classes.has('theme-1bit')).toBe(false)
    expect(classes.has('theme-16bit')).toBe(true)
    theme.setStyle('default')
    expect(classes.has('theme-16bit')).toBe(false)
    expect(theme.getIsThemed()).toBe(false)
  })

  it('forces light or dark regardless of the OS preference', async () => {
    await setup({ prefersDark: true })
    theme.initTheme()
    theme.setMode('light')
    expect(theme.getIsDark()).toBe(false)
    expect(storage.data['vault-mode']).toBe('light')
    theme.setMode('dark')
    expect(theme.getIsDark()).toBe(true)
  })

  it('keeps the in-memory value when persistence fails', async () => {
    await setup({ storageOpts: { failSet: true } })
    theme.initTheme()
    theme.setMode('light')
    expect(theme.getMode()).toBe('light')
  })
})
