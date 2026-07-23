/** WebSocket client with auto-reconnect */

import { buildApiRequest, getLiveMode } from './runtime-config.js'
import { backoffDelay } from './ws-backoff.js'
import {
  handleAnomalyRaised,
  handleAnomalyUpdated,
  handleBulkResolved,
  handleBulkAcked,
  notifyBaselineUpdated,
} from './anomalies.svelte.js'

let ws = null
let listeners = []
let reconnectTimer = null
let pollTimer = null
let previousStatus = null
let pollEnabled = false
let status = $state('disconnected')

// Bounded exponential backoff for reconnection. A fixed 3s uncapped loop churned
// silently forever when the daemon was unreachable; jittered, capped backoff
// (see ws-backoff.js) makes persistent failures gentle on the daemon and
// diagnosable via the connection indicator (issue #250).
let reconnectAttempts = 0

function scheduleReconnect() {
  clearTimeout(reconnectTimer)
  const delay = backoffDelay(reconnectAttempts)
  reconnectAttempts++
  reconnectTimer = setTimeout(connectWs, delay)
}

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
  listeners.forEach((fn) => {
    try {
      fn(msg)
    } catch (err) {
      console.error('ws listener error', err, msg)
    }
  })
  // Dispatch anomaly events directly into the shared reactive state so all
  // components see live updates without subscribing individually.
  switch (msg.type) {
    case 'anomaly.raised':
      handleAnomalyRaised(msg.data)
      break
    case 'anomaly.updated':
      handleAnomalyUpdated(msg.data)
      break
    case 'anomaly.bulk_resolved':
      handleBulkResolved(msg.data)
      break
    case 'anomaly.bulk_acked':
      handleBulkAcked(msg.data)
      break
    case 'baseline.updated':
      notifyBaselineUpdated(msg.data)
      break
  }
}

async function pollRunnerStatus() {
  if (!pollEnabled) return

  try {
    const { url, options } = buildApiRequest('GET', '/runner/status')
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
        job_name: snapshot.job_name,
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

  status = 'connecting'
  // Capture this socket locally so a stale handler (from a socket that was
  // replaced) can never mutate shared state for the current connection.
  let socket
  try {
    socket = new WebSocket(url)
  } catch {
    // The WebSocket constructor can throw (blocked API / SecurityError). This
    // runs from the reconnect timer too, so swallow it and keep the bounded
    // retry loop alive instead of letting the exception kill reconnection.
    ws = null
    status = 'reconnecting'
    scheduleReconnect()
    return
  }
  ws = socket

  socket.onopen = () => {
    if (ws !== socket) return
    status = 'connected'
    reconnectAttempts = 0
    clearTimeout(reconnectTimer)
  }

  socket.onmessage = (e) => {
    // Drop late messages from a superseded socket so a reconnect race can't
    // emit stale events — same guard as onopen/onclose.
    if (ws !== socket) return
    try {
      const msg = JSON.parse(e.data)
      emitMessage(msg)
    } catch {
      // ignore non-JSON messages
    }
  }

  socket.onclose = () => {
    if (ws !== socket) return
    // Every onclose schedules an automatic retry (disconnectWs detaches this
    // handler for manual/terminal closes), so 'reconnecting' is the honest
    // status — 'disconnected' is reserved for the idle/torn-down state.
    status = 'reconnecting'
    ws = null
    scheduleReconnect()
  }

  socket.onerror = () => {
    socket.close()
  }
}

export function disconnectWs() {
  clearTimeout(reconnectTimer)
  clearTimeout(pollTimer)
  pollTimer = null
  pollEnabled = false
  previousStatus = null
  reconnectAttempts = 0
  if (ws) {
    ws.onclose = null
    ws.close()
    ws = null
  }
  status = 'disconnected'
}
