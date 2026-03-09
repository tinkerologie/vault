const defaultConfig = Object.freeze({
  mode: 'direct',
  apiBase: '/api/v1',
  apiDisplayBase: '/api/v1',
  daemonBindAddress: '127.0.0.1',
  daemonPort: '24085',
  proxyPath: '',
  csrfToken: '',
  liveMode: 'websocket',
})

export function getRuntimeConfig() {
  if (typeof window === 'undefined') return defaultConfig
  return { ...defaultConfig, ...(window.__VAULT_RUNTIME_CONFIG__ || {}) }
}

export function isProxyMode() {
  return getRuntimeConfig().mode === 'unraid-proxy'
}

export function getLiveMode() {
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

  const options = { method, headers }
  if (body !== null) {
    options.headers = { ...headers, 'Content-Type': 'application/json' }
    options.body = JSON.stringify(body)
  }

  return {
    url: `${cfg.apiBase || defaultConfig.apiBase}${normalizedPath}`,
    options,
  }
}
