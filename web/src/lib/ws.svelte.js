/** WebSocket client with auto-reconnect */

import { buildApiRequest, getLiveMode } from './runtime-config.js'
import { backoffDelay } from './ws-backoff.js'
import { reconcileRunnerStatus, trackRunState } from './runner-status-sync.js'
import {
  handleAnomalyRaised,
  handleAnomalyUpdated,
  handleBulkResolved,
  handleBulkAcked,
  notifyBaselineUpdated,
  setOpenList,
} from './anomalies.svelte.js'
import {
  handleMountStarted,
  handleMountActive,
  handleMountUnmounted,
  handleMountFailed,
} from './mounts.svelte.js'

let ws = null
let listeners = []
let reconnectTimer = null
let pollTimer = null
let previousStatus = null
let pollEnabled = false
let pollTicks = 0
/** Open anomalies at the last poll, by id, serialized to detect changes. @type {Map<number, string>} */
let knownAnomalies = new Map()
let anomalyPollInFlight = false
// /runner/status polls every 2 s, so this checks anomalies every 30 s.
const ANOMALY_POLL_EVERY = 15
// The API's largest page, and a default window that would hide older open ones.
const ANOMALY_POLL_LIMIT = 500
const ANOMALY_POLL_MAX_PAGES = 20
const ANOMALY_POLL_SINCE = '1970-01-01T00:00:00Z'
const ANOMALY_POLL_TIMEOUT_MS = 15000
let status = $state('disconnected')

// Counts live run-state transitions (start/completion). resyncRunnerStatus
// samples it around its fetch so an older snapshot cannot overwrite newer live
// state. Deliberately NOT bumped for every message: anomaly, config, and
// storage events are constant background chatter, and invalidating on those
// would suppress the very completion resync this exists to deliver.
let runStateSeq = 0

// Counts live queue changes, tracked separately from runStateSeq. Folding them
// together would let ordinary queue churn discard the whole snapshot and
// suppress the completion resync this exists to deliver; keeping them apart
// lets a stale queue be dropped on its own.
let queueSeq = 0

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
    case 'mount.started':
      handleMountStarted(msg.data)
      break
    case 'mount.active':
      handleMountActive(msg.data)
      break
    case 'mount.unmounted':
      handleMountUnmounted(msg.data)
      break
    case 'mount.failed':
      handleMountFailed(msg.data)
      break
  }
}

/**
 * Every open anomaly, whatever its age. Returns null unless the whole set was
 * read, because replacing the shared list with part of it would drop the rest.
 */
async function fetchOpenAnomalies(signal) {
  const list = []
  let cursor = ''
  for (let page = 0; page < ANOMALY_POLL_MAX_PAGES; page++) {
    const params = new URLSearchParams({ state: 'open', since: ANOMALY_POLL_SINCE, limit: String(ANOMALY_POLL_LIMIT) })
    if (cursor) params.set('cursor', cursor)
    const { url, options } = buildApiRequest('GET', `/anomalies?${params}`)
    const res = await fetch(url, { ...options, signal })
    if (!res.ok) return null
    const body = await res.json()
    list.push(...(body?.anomalies ?? []))
    cursor = body?.next_cursor ?? ''
    if (!cursor) return list
  }
  return null
}

/**
 * Stands in for the anomaly.* WebSocket events a polling client never gets.
 * Emits what changed since the last check, so pages keep their usual handling.
 */
async function pollAnomalies() {
  // One at a time, so a slow older response can't overwrite a newer one.
  if (anomalyPollInFlight) return
  anomalyPollInFlight = true
  // Covers the body too: a stalled check must not block every later one.
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), ANOMALY_POLL_TIMEOUT_MS)
  try {
    const list = await fetchOpenAnomalies(controller.signal)
    if (!list) return
    const next = new Map(list.map((a) => [a.id, JSON.stringify(a)]))
    for (const anomaly of list) {
      const before = knownAnomalies.get(anomaly.id)
      if (before === undefined) emitMessage({ type: 'anomaly.raised', data: anomaly })
      else if (before !== next.get(anomaly.id)) emitMessage({ type: 'anomaly.updated', data: anomaly })
    }
    // An anomaly that left the open list may be resolved or acked; the event
    // that tells pages to refetch covers either.
    if ([...knownAnomalies.keys()].some((id) => !next.has(id))) {
      emitMessage({ type: 'anomaly.bulk_resolved', data: null })
    }
    knownAnomalies = next
    setOpenList(list)
  } catch {
    // Best-effort: the next check catches up.
  } finally {
    clearTimeout(timer)
    anomalyPollInFlight = false
  }
}

async function pollRunnerStatus() {
  if (!pollEnabled) return

  pollTicks++
  if (pollTicks % ANOMALY_POLL_EVERY === 0) void pollAnomalies()

  try {
    const { url, options } = buildApiRequest('GET', '/runner/status')
    const res = await fetch(url, options)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)

    const snapshot = await res.json()
    reconcileRunnerStatus(previousStatus, snapshot).forEach(emitMessage)

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

/**
 * Reconciles local state against /runner/status after the socket opens.
 *
 * Events are missed whenever the socket is down, and the hub also drops the
 * in-flight message when it evicts a client that cannot keep up — which may be
 * the terminal job_run_completed. The progress UI only clears on that event, so
 * without this a finished job renders as running until the user reloads (the
 * issue #256 symptom). Runs on every open, not just reconnects, so a page
 * loaded mid-run also gets a baseline to detect completion against.
 */
async function resyncRunnerStatus(socket) {
  const seqAtRequest = runStateSeq
  const queueAtRequest = queueSeq
  try {
    const { url, options } = buildApiRequest('GET', '/runner/status')
    const res = await fetch(url, options)
    if (!res.ok) return
    const snapshot = await res.json()
    // Discard the snapshot if this socket was superseded, or if a live run
    // transition arrived while the request was in flight — the stream is
    // authoritative and an older snapshot would roll the UI backwards.
    if (ws !== socket || runStateSeq !== seqAtRequest) return

    // A queue change during the fetch is newer than the snapshot's queue, but
    // only that part is stale: drop the queue message and keep the live queue
    // in the baseline, rather than throwing away a resync the run state needs.
    const staleQueue = queueSeq !== queueAtRequest
    reconcileRunnerStatus(previousStatus, snapshot)
      .filter((m) => !(staleQueue && m.type === 'queue_update'))
      .forEach(emitMessage)
    previousStatus = staleQueue
      ? { ...snapshot, queue: previousStatus?.queue || [] }
      : snapshot
  } catch {
    // Best-effort: a later reconnect (or the user reloading) resyncs.
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
    void resyncRunnerStatus(socket)
  }

  socket.onmessage = (e) => {
    // Drop late messages from a superseded socket so a reconnect race can't
    // emit stale events — same guard as onopen/onclose.
    if (ws !== socket) return
    try {
      const msg = JSON.parse(e.data)
      if (msg.type === 'job_run_started' || msg.type === 'job_run_completed') {
        runStateSeq++
      }
      if (msg.type === 'queue_update') {
        queueSeq++
      }
      // Keep the run-state baseline current from live events too, so a
      // reconnect has something to detect a completed run against.
      previousStatus = trackRunState(previousStatus, msg)
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
