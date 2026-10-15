import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// WebSocket client tests (issue #458). The module keeps connection state at
// module scope, so every test re-imports a fresh copy via vi.resetModules().
// A fake WebSocket stands in for the browser API and fetch is stubbed — no
// real network is touched.

const anomalies = vi.hoisted(() => ({
  handleAnomalyRaised: vi.fn(),
  handleAnomalyUpdated: vi.fn(),
  handleBulkResolved: vi.fn(),
  handleBulkAcked: vi.fn(),
  notifyBaselineUpdated: vi.fn(),
}))
const mounts = vi.hoisted(() => ({
  handleMountStarted: vi.fn(),
  handleMountActive: vi.fn(),
  handleMountUnmounted: vi.fn(),
  handleMountFailed: vi.fn(),
}))
vi.mock('./anomalies.svelte.js', () => anomalies)
vi.mock('./mounts.svelte.js', () => mounts)

class FakeWebSocket {
  static instances = []
  constructor(url) {
    this.url = url
    this.readyState = 0
    this.closed = false
    FakeWebSocket.instances.push(this)
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  receive(data) {
    this.onmessage?.({ data: typeof data === 'string' ? data : JSON.stringify(data) })
  }
  close() {
    this.closed = true
    this.readyState = 3
    this.onclose?.()
  }
}

function jsonResponse(body, ok = true, status = 200) {
  return { ok, status, json: async () => body }
}

let ws
let fetchMock

async function loadWs({ liveMode = 'websocket', protocol = 'http:' } = {}) {
  vi.resetModules()
  vi.stubGlobal('window', { __VAULT_RUNTIME_CONFIG__: { liveMode } })
  vi.stubGlobal('location', { protocol, host: 'vault.local:24085' })
  ws = await import('./ws.svelte.js')
  return ws
}

const lastSocket = () => FakeWebSocket.instances[FakeWebSocket.instances.length - 1]

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
  fetchMock = vi.fn(async () => jsonResponse({ active: false, queue: [] }))
  vi.stubGlobal('fetch', fetchMock)
  Object.values(anomalies).forEach((f) => f.mockClear())
  Object.values(mounts).forEach((f) => f.mockClear())
})

afterEach(() => {
  ws?.disconnectWs()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('websocket mode', () => {
  it('starts disconnected and connects to the ws:// endpoint on http', async () => {
    await loadWs()
    expect(ws.getWsStatus()).toBe('disconnected')
    ws.connectWs()
    expect(lastSocket().url).toBe('ws://vault.local:24085/api/v1/ws')
    expect(ws.getWsStatus()).toBe('connecting')
  })

  it('uses wss:// when the page is served over https', async () => {
    await loadWs({ protocol: 'https:' })
    ws.connectWs()
    expect(lastSocket().url).toBe('wss://vault.local:24085/api/v1/ws')
  })

  it('does not open a second socket while one is connecting or open', async () => {
    await loadWs()
    ws.connectWs()
    ws.connectWs()
    expect(FakeWebSocket.instances).toHaveLength(1)
    lastSocket().open()
    ws.connectWs()
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('reports connected on open and resyncs runner status', async () => {
    await loadWs()
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    fetchMock.mockResolvedValueOnce(jsonResponse({ active: true, job_id: 3, run_id: 9, queue: [] }))
    ws.connectWs()
    lastSocket().open()
    expect(ws.getWsStatus()).toBe('connected')
    await vi.waitFor(() => expect(seen.some((m) => m.type === 'runner_status_snapshot')).toBe(true))
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/runner/status', expect.objectContaining({ method: 'GET' }))
  })

  it('a resync after a missed completion emits job_run_completed', async () => {
    await loadWs()
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    ws.connectWs()
    const s1 = lastSocket()
    // Baseline from the live stream: a run is active.
    s1.receive({ type: 'job_run_started', job_id: 1, run_id: 5, job_name: 'a' })
    // The socket drops and the completion is lost while it is down.
    s1.close()
    expect(ws.getWsStatus()).toBe('reconnecting')
    fetchMock.mockResolvedValueOnce(jsonResponse({ active: false, queue: [] }))
    await vi.advanceTimersByTimeAsync(30000)
    const s2 = lastSocket()
    expect(s2).not.toBe(s1)
    s2.open()
    await vi.waitFor(() =>
      expect(seen.some((m) => m.type === 'job_run_completed' && m.run_id === 5)).toBe(true),
    )
  })

  it('discards the resync snapshot when a live run transition arrives mid-fetch', async () => {
    await loadWs()
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    let resolveFetch
    fetchMock.mockImplementationOnce(() => new Promise((r) => { resolveFetch = r }))
    ws.connectWs()
    const s = lastSocket()
    s.open()
    s.receive({ type: 'job_run_started', job_id: 1, run_id: 5 })
    resolveFetch(jsonResponse({ active: false, queue: [] }))
    await vi.advanceTimersByTimeAsync(0)
    expect(seen.map((m) => m.type)).toEqual(['job_run_started'])
  })

  it('drops only the stale queue message when the queue changes mid-fetch', async () => {
    await loadWs()
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    let resolveFetch
    fetchMock.mockImplementationOnce(() => new Promise((r) => { resolveFetch = r }))
    ws.connectWs()
    const s = lastSocket()
    s.open()
    s.receive({ type: 'queue_update', queue: [{ id: 1 }] })
    resolveFetch(jsonResponse({ active: false, queue: [{ id: 2 }] }))
    await vi.advanceTimersByTimeAsync(0)
    const types = seen.map((m) => m.type)
    expect(types.filter((t) => t === 'queue_update')).toHaveLength(1)
    expect(types).toContain('runner_status_snapshot')
  })

  it('ignores a failed or non-ok resync', async () => {
    await loadWs()
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    fetchMock.mockResolvedValueOnce(jsonResponse({}, false, 500))
    ws.connectWs()
    lastSocket().open()
    await vi.advanceTimersByTimeAsync(0)
    expect(seen).toEqual([])

    lastSocket().close()
    fetchMock.mockRejectedValueOnce(new Error('offline'))
    await vi.advanceTimersByTimeAsync(30000)
    lastSocket().open()
    await vi.advanceTimersByTimeAsync(0)
    expect(seen).toEqual([])
    expect(ws.getWsStatus()).toBe('connected')
  })

  it('delivers messages to listeners, isolates listener errors, and supports unsubscribe', async () => {
    await loadWs()
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const good = vi.fn()
    ws.onWsMessage(() => { throw new Error('boom') })
    const unsub = ws.onWsMessage(good)
    ws.connectWs()
    const s = lastSocket()
    s.receive({ type: 'hello' })
    expect(good).toHaveBeenCalledWith({ type: 'hello' })
    expect(errSpy).toHaveBeenCalled()
    unsub()
    s.receive({ type: 'hello' })
    expect(good).toHaveBeenCalledTimes(1)
  })

  it('ignores non-JSON frames', async () => {
    await loadWs()
    const fn = vi.fn()
    ws.onWsMessage(fn)
    ws.connectWs()
    lastSocket().receive('not json{')
    expect(fn).not.toHaveBeenCalled()
  })

  it('routes anomaly and mount events to their stores', async () => {
    await loadWs()
    ws.connectWs()
    const s = lastSocket()
    const cases = [
      ['anomaly.raised', anomalies.handleAnomalyRaised],
      ['anomaly.updated', anomalies.handleAnomalyUpdated],
      ['anomaly.bulk_resolved', anomalies.handleBulkResolved],
      ['anomaly.bulk_acked', anomalies.handleBulkAcked],
      ['baseline.updated', anomalies.notifyBaselineUpdated],
      ['mount.started', mounts.handleMountStarted],
      ['mount.active', mounts.handleMountActive],
      ['mount.unmounted', mounts.handleMountUnmounted],
      ['mount.failed', mounts.handleMountFailed],
    ]
    for (const [type, handler] of cases) {
      s.receive({ type, data: { type } })
      expect(handler).toHaveBeenCalledWith({ type })
    }
  })

  it('ignores events from a superseded socket', async () => {
    await loadWs()
    const fn = vi.fn()
    ws.onWsMessage(fn)
    ws.connectWs()
    const old = lastSocket()
    // Capture the old handlers, then tear down and reconnect.
    const { onopen, onmessage, onclose } = old
    ws.disconnectWs()
    ws.connectWs()
    onopen()
    onmessage({ data: JSON.stringify({ type: 'x' }) })
    onclose()
    expect(fn).not.toHaveBeenCalled()
    expect(ws.getWsStatus()).toBe('connecting')
  })

  it('closes the socket on error, which schedules a reconnect', async () => {
    await loadWs()
    ws.connectWs()
    const s = lastSocket()
    s.onerror()
    expect(s.closed).toBe(true)
    expect(ws.getWsStatus()).toBe('reconnecting')
    await vi.advanceTimersByTimeAsync(30000)
    expect(FakeWebSocket.instances.length).toBeGreaterThan(1)
  })

  it('keeps retrying when the WebSocket constructor throws', async () => {
    await loadWs()
    let calls = 0
    vi.stubGlobal('WebSocket', class {
      constructor() {
        calls++
        throw new Error('SecurityError')
      }
    })
    ws.connectWs()
    expect(ws.getWsStatus()).toBe('reconnecting')
    await vi.advanceTimersByTimeAsync(30000)
    expect(calls).toBeGreaterThan(1)
  })

  it('disconnectWs closes the socket without scheduling a reconnect', async () => {
    await loadWs()
    ws.connectWs()
    const s = lastSocket()
    s.open()
    ws.disconnectWs()
    expect(s.closed).toBe(true)
    expect(ws.getWsStatus()).toBe('disconnected')
    await vi.advanceTimersByTimeAsync(60000)
    expect(FakeWebSocket.instances).toHaveLength(1)
  })
})

describe('poll mode', () => {
  it('polls /runner/status instead of opening a socket and emits reconciled messages', async () => {
    await loadWs({ liveMode: 'poll' })
    const seen = []
    ws.onWsMessage((m) => seen.push(m))
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ active: true, job_id: 1, run_id: 2, queue: [] }))
      .mockResolvedValueOnce(jsonResponse({ active: false, queue: [] }))
    ws.connectWs()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(ws.getWsStatus()).toBe('polling')
    await vi.advanceTimersByTimeAsync(0)
    expect(seen.some((m) => m.type === 'runner_status_snapshot')).toBe(true)
    await vi.advanceTimersByTimeAsync(2000)
    expect(seen.some((m) => m.type === 'job_run_completed' && m.run_id === 2)).toBe(true)
    expect(ws.getWsStatus()).toBe('polling')
  })

  it('a second connectWs while a poll is scheduled does not start a parallel loop', async () => {
    await loadWs({ liveMode: 'poll' })
    ws.connectWs()
    await vi.advanceTimersByTimeAsync(0)
    ws.connectWs()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('reports disconnected on a failed poll and keeps polling', async () => {
    await loadWs({ liveMode: 'poll' })
    fetchMock.mockResolvedValueOnce(jsonResponse({}, false, 502))
    ws.connectWs()
    await vi.advanceTimersByTimeAsync(0)
    expect(ws.getWsStatus()).toBe('disconnected')
    await vi.advanceTimersByTimeAsync(2000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(ws.getWsStatus()).toBe('polling')
  })

  it('stops polling after disconnectWs, even with a poll in flight', async () => {
    await loadWs({ liveMode: 'poll' })
    let resolveFetch
    fetchMock.mockImplementationOnce(() => new Promise((r) => { resolveFetch = r }))
    ws.connectWs()
    ws.disconnectWs()
    resolveFetch(jsonResponse({ active: false, queue: [] }))
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
