/** WebSocket client with auto-reconnect */

import { getApiKey } from './auth.svelte.js'

let ws = null
let listeners = []
let reconnectTimer = null
let status = $state('disconnected')

export function getWsStatus() {
  return status
}

export function onWsMessage(fn) {
  listeners.push(fn)
  return () => {
    listeners = listeners.filter((l) => l !== fn)
  }
}

export function connectWs() {
  if (ws && ws.readyState <= 1) return

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  let url = `${proto}//${location.host}/api/v1/ws`
  const key = getApiKey()
  if (key) {
    url += `?token=${encodeURIComponent(key)}`
  }

  status = 'connecting'
  ws = new WebSocket(url)

  ws.onopen = () => {
    status = 'connected'
    clearTimeout(reconnectTimer)
  }

  ws.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data)
      listeners.forEach((fn) => fn(msg))
    } catch {
      // ignore non-JSON messages
    }
  }

  ws.onclose = () => {
    status = 'disconnected'
    ws = null
    reconnectTimer = setTimeout(connectWs, 3000)
  }

  ws.onerror = () => {
    ws?.close()
  }
}

export function disconnectWs() {
  clearTimeout(reconnectTimer)
  if (ws) {
    ws.onclose = null
    ws.close()
    ws = null
  }
  status = 'disconnected'
}
