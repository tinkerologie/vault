import { getApiKey } from './auth.svelte.js'

const defaultConfig = Object.freeze({
  mode: 'direct',
  apiBase: '/api/v1',
  apiDisplayBase: '/api/v1',
  daemonBindAddress: '127.0.0.1',
  daemonPort: '24085',
  proxyPath: '',
  restoreRequestActionPath: '',
  csrfToken: '',
  liveMode: 'websocket',
  timeFormat: 'auto',
  presentation: 'standalone',
  hostTheme: null,
})

export function getRuntimeConfig() {
  if (typeof window === 'undefined') return defaultConfig
  return { ...defaultConfig, ...(window.__VAULT_RUNTIME_CONFIG__ || {}) }
}

export function isProxyMode() {
  return getRuntimeConfig().mode === 'unraid-proxy'
}

/**
 * Whether this window is a same-origin frame, which is the only place a host
 * script can size it. Reading a cross-origin parent throws, and counts as no.
 * @param {Window} win
 */
function isSameOriginFrame(win) {
  try {
    return win.self !== win.top && win.parent.location.origin === win.location.origin
  } catch {
    return false
  }
}

/**
 * True only when the page asked for embedded presentation and really is in a
 * host's frame. Elsewhere the embedded layout would be clipped and unscrollable.
 */
export function isEmbedded() {
  if (typeof window === 'undefined') return false
  return getRuntimeConfig().presentation === 'embedded' && isSameOriginFrame(window)
}

/** Unraid's light or dark theme, or null when it's unknown. */
export function getHostThemeMode() {
  const { hostTheme } = getRuntimeConfig()
  return hostTheme === 'light' || hostTheme === 'dark' ? hostTheme : null
}

export function getLiveMode() {
  // A browser can't send the key on a WebSocket upgrade, so a client that needs
  // one follows progress by polling.
  if (getApiKey()) return 'poll'
  return getRuntimeConfig().liveMode || defaultConfig.liveMode
}

export function getApiDisplayBase() {
  return getRuntimeConfig().apiDisplayBase || defaultConfig.apiDisplayBase
}

export function getDaemonBindAddress() {
  return getRuntimeConfig().daemonBindAddress || defaultConfig.daemonBindAddress
}

export function getDaemonPort() {
  return getRuntimeConfig().daemonPort || defaultConfig.daemonPort
}

export function getTimeFormat() {
  return getRuntimeConfig().timeFormat || defaultConfig.timeFormat
}

/**
 * Returns the hour12 option for Intl/toLocale* calls:
 * - true  → force 12-hour (AM/PM)
 * - false → force 24-hour
 * - undefined → let the browser locale decide
 */
export function getHour12() {
  const fmt = getTimeFormat()
  if (fmt === '12h') return true
  if (fmt === '24h') return false
  return undefined
}

export function buildApiRequest(method, path, { body = null, headers = {} } = {}) {
  const cfg = getRuntimeConfig()
  const normalizedPath = path.startsWith('/') ? path : `/${path}`

  if (cfg.mode === 'unraid-proxy' && cfg.proxyPath) {
    if (method === 'GET' || method === 'HEAD') {
      return {
        url: `${cfg.proxyPath}?path=${encodeURIComponent(`/api/v1${normalizedPath}`)}`,
        options: { method, headers },
      }
    }

    const form = new URLSearchParams()
    form.set('csrf_token', cfg.csrfToken || '')
    form.set('method', method)
    form.set('path', `/api/v1${normalizedPath}`)
    if (body !== null) {
      form.set('payload', JSON.stringify(body))
    }

    return {
      url: cfg.proxyPath,
      options: {
        method: 'POST',
        headers: {
          ...headers,
          'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8',
        },
        body: form.toString(),
      },
    }
  }

  const apiKey = getApiKey()
  const options = { method, headers: apiKey ? { ...headers, 'X-API-Key': apiKey } : headers }
  if (body !== null) {
    options.headers = { ...options.headers, 'Content-Type': 'application/json' }
    options.body = JSON.stringify(body)
  }

  return {
    url: `${cfg.apiBase || defaultConfig.apiBase}${normalizedPath}`,
    options,
  }
}

export function canApproveRestoreRequest() {
  const cfg = getRuntimeConfig()
  return isProxyMode() && Boolean(cfg.restoreRequestActionPath && cfg.csrfToken)
}

export function buildRestoreApprovalRequest(method, id, { body }) {
  if (!canApproveRestoreRequest()) throw new Error('Open the signed-in Unraid Vault page to approve.')
  const cfg = getRuntimeConfig()
  const form = new URLSearchParams({
    action: 'approve',
    id,
    review_digest: body.reviewDigest,
    passphrase: body.passphrase,
    csrf_token: cfg.csrfToken,
  })
  return {
    url: cfg.restoreRequestActionPath,
    options: {
      method,
      credentials: 'same-origin',
      redirect: 'error',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8' },
      body: form.toString(),
    },
  }
}
