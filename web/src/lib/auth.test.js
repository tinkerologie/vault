import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { buildApiRequest, getLiveMode } from './runtime-config.js'
import { api } from './api.js'
import { getApiKey, isAuthRequired, isRemoteKeyClient, setApiKey } from './auth.svelte.js'

function fakeStorage() {
  const data = new Map()
  return {
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => data.set(k, v),
  }
}

describe('direct-mode API key', () => {
  beforeEach(() => vi.stubGlobal('sessionStorage', fakeStorage()))
  afterEach(() => vi.unstubAllGlobals())

  it('sends nothing without a stored key', () => {
    const { options } = buildApiRequest('GET', '/jobs')
    expect(options.headers['X-API-Key']).toBeUndefined()
    expect(getLiveMode()).toBe('websocket')
  })

  it('sends a stored key and switches to polling', () => {
    setApiKey('abc')
    const { options } = buildApiRequest('POST', '/jobs', { body: { a: 1 } })
    expect(options.headers['X-API-Key']).toBe('abc')
    expect(options.headers['Content-Type']).toBe('application/json')
    expect(getLiveMode()).toBe('poll')
  })

  it('never puts the key on a proxied request', () => {
    setApiKey('abc')
    vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: { mode: 'unraid-proxy', proxyPath: '/proxy.php' } })
    expect(JSON.stringify(buildApiRequest('GET', '/jobs'))).not.toContain('abc')
    expect(JSON.stringify(buildApiRequest('POST', '/jobs', { body: {} }))).not.toContain('abc')
  })

  it('treats only a browser holding a key as a remote client', () => {
    expect(isRemoteKeyClient()).toBe(false)
    setApiKey('abc')
    expect(isRemoteKeyClient()).toBe(true)
  })

  it('sends every key-management call through the plugin proxy with its CSRF token', () => {
    vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: { mode: 'unraid-proxy', proxyPath: '/proxy.php', csrfToken: 'tok' } })
    for (const [method, path] of [['POST', '/settings/api-key/generate'], ['POST', '/settings/api-key/rotate'], ['DELETE', '/settings/api-key']]) {
      const { url, options } = buildApiRequest(method, path)
      const form = new URLSearchParams(options.body)
      expect(url).toBe('/proxy.php')
      expect(options.method).toBe('POST')
      expect(form.get('csrf_token')).toBe('tok')
      expect(form.get('method')).toBe(method)
      expect(form.get('path')).toBe(`/api/v1${path}`)
    }
    expect(buildApiRequest('GET', '/settings/api-key/key').url).toBe(`/proxy.php?path=${encodeURIComponent('/api/v1/settings/api-key/key')}`)
  })

  it('never shows the key prompt in proxy mode', async () => {
    vi.resetModules()
    vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: { mode: 'unraid-proxy', proxyPath: '/proxy.php' } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 401 })))
    const fresh = await import('./api.js')
    const auth = await import('./auth.svelte.js')
    await expect(fresh.api.listStorage()).rejects.toMatchObject({ status: 401 })
    expect(auth.isAuthRequired()).toBe(false)
  })

  it('tolerates blocked storage', () => {
    vi.stubGlobal('sessionStorage', {
      getItem: () => { throw new Error('blocked') },
      setItem: () => { throw new Error('blocked') },
    })
    expect(getApiKey()).toBe('')
    expect(setApiKey('abc')).toBe(false)
  })

  it('asks for a key after a 401 in direct mode', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 401 })))
    await expect(api.listStorage()).rejects.toMatchObject({ status: 401 })
    expect(isAuthRequired()).toBe(true)
  })
})
