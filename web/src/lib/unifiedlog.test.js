import { describe, it, expect, vi, beforeEach } from 'vitest'

// Regression tests for the unified log store (issue #328).
// The store previously had zero coverage; the scenarios below pin the
// invariants that the console depends on:
//   1. The FIRST PAINT materializes every activity row of the newest page,
//      even when the newest terminal's run-log expansion exceeds the step
//      budget (the budget bounds RUN-LOG lines only). Previously the whole
//      page beyond the split point was deferred, so a set of rows (health
//      checks etc.) appeared only on the next merge — the visible "set
//      replaced by another set" flash on every refresh.
//   2. Streamed (WS) run-log lines for a live run are inserted in
//      CHRONOLOGICAL position, never appended at the bottom.
//   3. The newest set stays at the bottom of the buffer across the refresh
//      sequence (load -> fillViewport -> loadAll -> poll merges).

// Server model used by all tests:
//   - terminal run 60: activity id 400 (NEWEST, 15:58:04) + 200 run-log
//     lines (15:55:00..15:56:40) — the expansion exceeds the 150-line step
//     budget, exercising the split path exactly like a real long run
//   - 5 health checks (ids 300..304, 15:57:59..15:58:03)
//   - active run 7: plain "started" row (id 210, 15:54:00); its lines
//     arrive only over WS (ts 15:54:10..30, chronologically mid-buffer)

vi.mock('./api.js', () => {
  const entries = [
    {
      id: 400, level: 'info', category: 'backup',
      message: 'Backup completed: vms',
      details: JSON.stringify({ run_id: 60, run_log: true, job_id: 1, job_name: 'vms' }),
      created_at: '2026-08-21T15:58:04Z',
    },
    {
      id: 210, level: 'info', category: 'backup',
      message: 'Backup started: docker',
      details: JSON.stringify({ run_id: 7, job_id: 1, job_name: 'docker' }),
      created_at: '2026-08-21T15:54:00Z',
    },
  ]
  for (let i = 0; i < 5; i++) {
    entries.push({
      id: 300 + i, level: 'info', category: 'health',
      message: `Health check, container=c${i}, status=ok`,
      details: JSON.stringify({ container_name: `c${i}` }),
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 57, 59) + i * 1000).toISOString(),
    })
  }
  entries.sort((a, b) => b.id - a.id) // newest-first page

  const runLogs = { 60: [] }
  for (let i = 1; i <= 200; i++) {
    runLogs[60].push({
      id: 60000 + i, run_id: 60, level: 'info',
      message: `run 60 line ${i}`,
      data: '', ts: new Date(Date.UTC(2026, 7, 21, 15, 55, 0) + i * 500).toISOString(),
    })
  }

  const api = {
    getActivity: vi.fn(async (limit = 30, category = '', beforeId = 0) => {
      let rows = entries
      if (beforeId) rows = rows.filter(r => r.id < beforeId)
      return rows.slice(0, limit)
    }),
    getRunLogs: vi.fn(async (runId) => ({ entries: runLogs[runId] || [] })),
  }
  return { api }
})

let wsHandler = null
vi.mock('./ws.svelte.js', () => ({
  onWsMessage: (fn) => { wsHandler = fn; return () => { wsHandler = null } },
}))

import { createUnifiedLogStore } from './unifiedlog.svelte.js'

function bottomIds(store, n) {
  return store.entries.slice(-n).map(e => `${e.type}:${e.id}@${new Date(e.ts).toISOString().slice(11, 19)}`)
}

describe('unified log store', () => {
  let store
  beforeEach(() => { wsHandler = null; store = createUnifiedLogStore() })

  it('first paint includes every activity row even when a terminal expansion exceeds the budget', async () => {
    // The newest page is [terminal run 60 (200 lines), 5 health checks,
    // started row]. The terminal's expansion blows the 150-line step budget,
    // but the health checks and the started row MUST be present immediately
    // after load() — no loadOlder/merge may be required to surface them.
    await store.load()

    const ids = store.entries.map(e => e.id)
    expect(ids).toContain(210)   // "Backup started" row
    for (let i = 0; i < 5; i++) expect(ids).toContain(300 + i) // health checks

    // The newest rows at the bottom are the health checks, not run-log lines.
    expect(bottomIds(store, 3)).toEqual([
      'activity:302@15:58:01',
      'activity:303@15:58:02',
      'activity:304@15:58:03',
    ])
  })

  it('inserts WS-streamed run-log lines in chronological position, not at the bottom', async () => {
    await store.load()
    store.setupWs()

    // Baseline: the bottom of the buffer is the newest health-check set
    const baseline = bottomIds(store, 3)
    expect(baseline.every(s => s.includes('15:58:0'))).toBe(true)

    // Stream 3 lines for the ACTIVE run 7 (ts 15:54:10..30 — mid-buffer)
    wsHandler({ type: 'run_log', entry: { id: 70001, run_id: 7, level: 'info', message: 'docker: inspecting container', data: '', ts: '2026-08-21T15:54:10Z' } })
    wsHandler({ type: 'run_log', entry: { id: 70002, run_id: 7, level: 'info', message: 'docker: stopping container', data: '', ts: '2026-08-21T15:54:20Z' } })
    wsHandler({ type: 'run_log', entry: { id: 70003, run_id: 7, level: 'info', message: 'docker: backing up', data: '', ts: '2026-08-21T15:54:30Z' } })

    // The bottom set must NOT change: a streamed line is not the newest log.
    expect(bottomIds(store, 3)).toEqual(baseline)

    // The streamed lines sit chronologically between the "started" row and
    // the terminal run 60's lines.
    const rl = store.entries.filter(e => e.type === 'runlog' && e.runId === 7)
    expect(rl).toHaveLength(3)
    const first = store.entries.indexOf(rl[0])
    const last = store.entries.indexOf(rl[2])
    expect(new Date(store.entries[first - 1].ts).getTime()).toBeLessThanOrEqual(new Date(rl[0].ts).getTime())
    expect(new Date(store.entries[last + 1].ts).getTime()).toBeGreaterThanOrEqual(new Date(rl[2].ts).getTime())
  })

  it('keeps the newest set at the bottom across load -> fill -> loadAll -> poll', async () => {
    await store.load()
    const afterLoad = bottomIds(store, 3)

    await store.loadOlder() // drains the split terminal's deferred middle lines
    await store.loadOlder() // cursor exhausted -> hasMore false

    let guard = 0
    while (store.hasMore && guard++ < 10) await store.loadOlder({ limit: 1000, silent: true })

    await store.loadNewer()
    const stable = bottomIds(store, 3)
    expect(stable).toEqual(afterLoad)
  })
})
