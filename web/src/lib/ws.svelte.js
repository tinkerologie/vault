/** WebSocket client with auto-reconnect */

import { getApiKey } from './auth.svelte.js'
import { buildApiRequest, getLiveMode } from './runtime-config.js'

let ws = null
let listeners = []
let reconnectTimer = null
let pollTimer = null
let previousStatus = null
let pollEnabled = false
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

function emitMessage(msg) {
  listeners.forEach((fn) => fn(msg))
}

async function pollRunnerStatus() {
  if (!pollEnabled) return

  const headers = {}
  const key = getApiKey()
  if (key) {
    headers['Authorization'] = `Bearer ${key}`
  }

  try {
    const { url, options } = buildApiRequest('GET', '/runner/status', { headers })
    const res = await fetch(url, options)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)

    const snapshot = await res.json()
    emitMessage({ type: 'runner_status_snapshot', status: snapshot })

    const prevQueue = JSON.stringify(previousStatus?.queue || [])
    const nextQueue = JSON.stringify(snapshot?.queue || [])
    if (prevQueue !== nextQueue) {
      emitMessage({ type: 'queue_update', queue: snapshot?.queue || [] })
    }

    if (!previousStatus?.active && snapshot?.active) {
      emitMessage({
        type: 'job_run_started',
        job_id: snapshot.job_id,
        run_id: snapshot.run_id,
        run_type: snapshot.run_type,
        items_total: snapshot.items_total,
      })
    }

    if (previousStatus?.active && !snapshot?.active) {
      emitMessage({
        type: 'job_run_completed',
        job_id: previousStatus.job_id,
        run_id: previousStatus.run_id,
        run_type: previousStatus.run_type,
      })
    }

    previousStatus = snapshot
    status = 'polling'
  } catch {
    status = 'disconnected'
  } finally {
    if (pollEnabled) {
      pollTimer = setTimeout(pollRunnerStatus, 2000)
    } else {
      pollTimer = null
    }
  }
}

function startPolling() {
  clearTimeout(reconnectTimer)
  pollEnabled = true
  if (pollTimer) return
  previousStatus = null
  status = 'polling'
  void pollRunnerStatus()
}

export function connectWs() {
  if (getLiveMode() === 'poll') {
    startPolling()
    return
  }

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
      emitMessage(msg)
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
  clearTimeout(pollTimer)
  pollTimer = null
  pollEnabled = false
  previousStatus = null
  if (ws) {
    ws.onclose = null
    ws.close()
    ws = null
  }
  status = 'disconnected'
}
