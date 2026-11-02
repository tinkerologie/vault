import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  buildApiRequest,
  getApiDisplayBase,
  getDaemonBindAddress,
  getDaemonPort,
  getHostThemeMode,
  getHour12,
  getLiveMode,
  getRuntimeConfig,
  getTimeFormat,
  isEmbedded,
  isProxyMode,
} from './runtime-config.js'

// Runtime config tests (issue #458): window.__VAULT_RUNTIME_CONFIG__ is the
// only input, so it is stubbed per case.

function withConfig(cfg) {
  vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: cfg })
}

afterEach(() => vi.unstubAllGlobals())

describe('getRuntimeConfig', () => {
  it('returns the defaults without a window (SSR / node)', () => {
    expect(getRuntimeConfig().mode).toBe('direct')
    expect(getLiveMode()).toBe('websocket')
  })

  it('returns the defaults when no config was injected', () => {
    vi.stubGlobal('window', {})
    expect(getRuntimeConfig().apiBase).toBe('/api/v1')
  })

  it('merges injected values over the defaults', () => {
    withConfig({ mode: 'unraid-proxy', daemonPort: '9999' })
    expect(isProxyMode()).toBe(true)
    expect(getDaemonPort()).toBe('9999')
    expect(getDaemonBindAddress()).toBe('127.0.0.1')
  })

  it('falls back to defaults for empty injected values', () => {
    withConfig({ liveMode: '', apiDisplayBase: '', daemonBindAddress: '', daemonPort: '', timeFormat: '' })
    expect(getLiveMode()).toBe('websocket')
    expect(getApiDisplayBase()).toBe('/api/v1')
    expect(getDaemonBindAddress()).toBe('127.0.0.1')
    expect(getDaemonPort()).toBe('24085')
    expect(getTimeFormat()).toBe('auto')
    expect(isProxyMode()).toBe(false)
  })

  it('exposes injected display values', () => {
    withConfig({ liveMode: 'poll', apiDisplayBase: '/plugins/vault/api', daemonBindAddress: '0.0.0.0', timeFormat: '24h' })
    expect(getLiveMode()).toBe('poll')
    expect(getApiDisplayBase()).toBe('/plugins/vault/api')
    expect(getDaemonBindAddress()).toBe('0.0.0.0')
    expect(getTimeFormat()).toBe('24h')
  })
})

describe('getHour12', () => {
  it.each([
    ['12h', true],
    ['24h', false],
    ['auto', undefined],
  ])('maps %s to %s', (fmt, expected) => {
    withConfig({ timeFormat: fmt })
    expect(getHour12()).toBe(expected)
  })
})

describe('buildApiRequest', () => {
  it('builds a direct request and normalizes a missing leading slash', () => {
    expect(buildApiRequest('GET', 'jobs')).toEqual({ url: '/api/v1/jobs', options: { method: 'GET', headers: {} } })
  })

  it('serializes a JSON body in direct mode', () => {
    const { options } = buildApiRequest('POST', '/jobs', { body: { a: 1 }, headers: { X: '1' } })
    expect(options.headers).toEqual({ X: '1', 'Content-Type': 'application/json' })
    expect(options.body).toBe('{"a":1}')
  })

  it('uses a custom apiBase, or the default when it is empty', () => {
    withConfig({ apiBase: '/custom' })
    expect(buildApiRequest('GET', '/x').url).toBe('/custom/x')
    withConfig({ apiBase: '' })
    expect(buildApiRequest('GET', '/x').url).toBe('/api/v1/x')
  })

  it('routes GET and HEAD through the proxy query string', () => {
    withConfig({ mode: 'unraid-proxy', proxyPath: '/plugins/vault/api.php' })
    expect(buildApiRequest('GET', '/jobs?x=1').url).toBe('/plugins/vault/api.php?path=%2Fapi%2Fv1%2Fjobs%3Fx%3D1')
    const head = buildApiRequest('HEAD', '/health')
    expect(head.options).toEqual({ method: 'HEAD', headers: {} })
  })

  it('tunnels mutating requests as a CSRF-protected form POST', () => {
    withConfig({ mode: 'unraid-proxy', proxyPath: '/p.php', csrfToken: 'tok' })
    const { url, options } = buildApiRequest('PUT', '/jobs/1', { body: { n: 1 } })
    expect(url).toBe('/p.php')
    expect(options.method).toBe('POST')
    expect(options.headers['Content-Type']).toMatch(/x-www-form-urlencoded/)
    const form = new URLSearchParams(options.body)
    expect(form.get('csrf_token')).toBe('tok')
    expect(form.get('method')).toBe('PUT')
    expect(form.get('path')).toBe('/api/v1/jobs/1')
    expect(form.get('payload')).toBe('{"n":1}')
  })

  it('omits the payload and sends an empty CSRF token when absent', () => {
    withConfig({ mode: 'unraid-proxy', proxyPath: '/p.php', csrfToken: '' })
    const form = new URLSearchParams(buildApiRequest('DELETE', '/jobs/1').options.body)
    expect(form.get('csrf_token')).toBe('')
    expect(form.has('payload')).toBe(false)
  })

  it('falls back to direct mode when proxy mode has no proxyPath', () => {
    withConfig({ mode: 'unraid-proxy', proxyPath: '' })
    expect(buildApiRequest('GET', '/jobs').url).toBe('/api/v1/jobs')
  })
})

describe('embedded presentation (#526)', () => {
  const ORIGIN = 'http://tower.test'
  const framed = (cfg, parent = { location: { origin: ORIGIN } }) => {
    const win = { __VAULT_RUNTIME_CONFIG__: cfg, location: { origin: ORIGIN }, parent, top: parent }
    win.self = win
    vi.stubGlobal('window', win)
  }

  it('defaults to standalone with no host theme', () => {
    expect(getRuntimeConfig().presentation).toBe('standalone')
    expect(getRuntimeConfig().hostTheme).toBeNull()
    expect(isEmbedded()).toBe(false)
    expect(getHostThemeMode()).toBeNull()
  })

  it('is embedded when asked for inside a same-origin frame', () => {
    framed({ presentation: 'embedded' })
    expect(isEmbedded()).toBe(true)
  })

  it('needs the explicit request, even when framed', () => {
    framed({})
    expect(isEmbedded()).toBe(false)
    framed({ presentation: 'bogus' })
    expect(isEmbedded()).toBe(false)
  })

  it('is standalone at the top level, even when asked for', () => {
    const win = { __VAULT_RUNTIME_CONFIG__: { presentation: 'embedded' }, location: { origin: ORIGIN } }
    win.self = win
    win.top = win
    win.parent = win
    vi.stubGlobal('window', win)
    expect(isEmbedded()).toBe(false)
  })

  it('is standalone in a frame of another origin', () => {
    framed({ presentation: 'embedded' }, { location: { origin: 'http://other.test' } })
    expect(isEmbedded()).toBe(false)
  })

  it('does not throw when reading a cross-origin parent throws', () => {
    const parent = {
      get location() {
        throw new DOMException('Blocked a frame', 'SecurityError')
      },
    }
    framed({ presentation: 'embedded' }, parent)
    expect(isEmbedded()).toBe(false)
  })

  it.each(['light', 'dark'])('passes through a %s host theme', (hostTheme) => {
    withConfig({ hostTheme })
    expect(getHostThemeMode()).toBe(hostTheme)
  })

  it.each(['system', 'black', '', 1, null, {}])('rejects a host theme of %j', (hostTheme) => {
    withConfig({ hostTheme })
    expect(getHostThemeMode()).toBeNull()
  })
})
