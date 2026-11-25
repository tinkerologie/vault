import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'

// The pre-paint theme script in index.html (#696) must pick the same classes
// as initTheme, or the page would flash from one theme to another on boot.
// Both run here against the same faked storage, media query and frame.

const HTML = readFileSync(new URL('../../index.html', import.meta.url), 'utf8')
const SCRIPT = HTML.match(/<script data-vault-theme-boot>([\s\S]*?)<\/script>/)?.[1]

function fakeEnv({ stored = {}, prefersDark = true, failStorage = false, failMatchMedia = false, runtime, framed = false } = {}) {
  const classes = new Set()
  const data = { ...stored }
  const localStorage = {
    getItem: (k) => {
      if (failStorage) throw new Error('SecurityError')
      return k in data ? data[k] : null
    },
    setItem: (k, v) => {
      if (failStorage) throw new Error('SecurityError')
      data[k] = String(v)
    },
    removeItem: (k) => {
      if (failStorage) throw new Error('SecurityError')
      delete data[k]
    },
  }
  const location = { origin: 'http://tower' }
  const parent = { location }
  const win = {
    __VAULT_RUNTIME_CONFIG__: runtime,
    matchMedia: () => {
      if (failMatchMedia) throw new Error('blocked')
      return { matches: prefersDark, addEventListener() {}, removeEventListener() {} }
    },
    location,
  }
  win.self = win
  win.top = framed ? parent : win
  win.parent = framed ? parent : win
  win.localStorage = localStorage
  const document = {
    documentElement: {
      classList: {
        toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
        add: (c) => classes.add(c),
        remove: (c) => classes.delete(c),
      },
    },
  }
  return { win, document, localStorage, classes, data }
}

/** Classes the inline script puts on <html>. */
function prepaint(opts) {
  const env = fakeEnv(opts)
  // eslint-disable-next-line no-new-func -- runs the inline script exactly as the browser would
  new Function('window', 'document', 'localStorage', SCRIPT)(env.win, env.document, env.localStorage)
  return [...env.classes].sort()
}

/** Classes initTheme puts on <html> in the same environment. */
async function app(opts) {
  vi.resetModules()
  const env = fakeEnv(opts)
  vi.stubGlobal('document', env.document)
  vi.stubGlobal('localStorage', env.localStorage)
  vi.stubGlobal('window', env.win)
  const theme = await import('./theme.svelte.js')
  const runtime = await import('./runtime-config.js')
  theme.initTheme(runtime.isEmbedded() ? runtime.getHostThemeMode() : null)
  return [...env.classes].sort()
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const embedded = (hostTheme) => ({ runtime: { presentation: 'embedded', hostTheme }, framed: true })

const CASES = [
  ['nothing saved, dark device', {}, ['dark']],
  ['nothing saved, light device', { prefersDark: false }, []],
  ['saved dark mode', { stored: { 'vault-mode': 'dark' }, prefersDark: false }, ['dark']],
  ['saved light mode', { stored: { 'vault-mode': 'light' } }, []],
  ['saved system mode on a light device', { stored: { 'vault-mode': 'system' }, prefersDark: false }, []],
  ['saved Retro light', { stored: { 'vault-style': 'retro', 'vault-mode': 'light' } }, ['theme-retro']],
  ['saved 1-Bit dark', { stored: { 'vault-style': '1bit', 'vault-mode': 'dark' } }, ['dark', 'theme-1bit']],
  ['Dracula with a saved light mode', { stored: { 'vault-style': 'dracula', 'vault-mode': 'light' } }, ['dark', 'theme-dracula']],
  ['Catppuccin light', { stored: { 'vault-style': 'catppuccin', 'vault-mode': 'light' } }, ['theme-catppuccin']],
  ['legacy 8bit style', { stored: { 'vault-style': '8bit', 'vault-mode': 'light' } }, ['theme-retro']],
  ['legacy 16bit style', { stored: { 'vault-style': '16bit', 'vault-mode': 'dark' } }, ['dark', 'theme-retro']],
  ['older vault-theme retro-dark', { stored: { 'vault-theme': 'retro-dark' }, prefersDark: false }, ['dark', 'theme-retro']],
  ['older vault-theme light', { stored: { 'vault-theme': 'light' } }, []],
  ['unknown values', { stored: { 'vault-style': 'constructor', 'vault-mode': 'neon' }, prefersDark: false }, []],
  ['blocked storage', { failStorage: true }, ['dark']],
  ['blocked matchMedia', { failMatchMedia: true }, ['dark']],
  ['embedded in a light host, nothing saved', { ...embedded('light') }, []],
  ['embedded in a dark host, nothing saved', { ...embedded('dark'), prefersDark: false }, ['dark']],
  ['embedded in a light host with saved dark mode', { ...embedded('light'), stored: { 'vault-mode': 'dark' } }, ['dark']],
  ['embedded in a light host with saved system mode', { ...embedded('light'), stored: { 'vault-mode': 'system' } }, ['dark']],
  ['host theme outside a frame is ignored', { runtime: { presentation: 'embedded', hostTheme: 'light' } }, ['dark']],
  ['host theme without embedded presentation is ignored', { runtime: { hostTheme: 'light' }, framed: true }, ['dark']],
]

describe('pre-paint theme script', () => {
  it('is inline in index.html', () => {
    expect(SCRIPT).toBeTruthy()
    // Before the app bundle, so it runs first.
    expect(HTML.indexOf('data-vault-theme-boot')).toBeLessThan(HTML.indexOf('src="/src/main.js"'))
    // In <body>, after the runtime config the servers inject before </head>.
    expect(HTML.indexOf('data-vault-theme-boot')).toBeGreaterThan(HTML.indexOf('</head>'))
  })

  it.each(CASES)('%s', async (_name, opts, expected) => {
    expect(prepaint(opts)).toEqual(expected)
    expect(await app(opts)).toEqual(expected)
  })

  it('never writes to storage, leaving migration to the app', () => {
    const env = fakeEnv({ stored: { 'vault-theme': 'retro', 'vault-style': '8bit' } })
    new Function('window', 'document', 'localStorage', SCRIPT)(env.win, env.document, env.localStorage)
    expect(env.data).toEqual({ 'vault-theme': 'retro', 'vault-style': '8bit' })
  })

  it('survives a document it cannot touch', () => {
    expect(() => new Function('window', 'document', 'localStorage', SCRIPT)({}, {}, undefined)).not.toThrow()
  })
})

describe('native control colours', () => {
  const CSS = readFileSync(new URL('../app.css', import.meta.url), 'utf8')

  it('declares a light colour scheme by default and a dark one under .dark', () => {
    expect(CSS).toMatch(/:root \{\s*color-scheme: light;\s*\}/)
    expect(CSS).toMatch(/:root\.dark \{\s*color-scheme: dark;\s*\}/)
  })
})
