import { describe, it, expect, vi, beforeEach } from 'vitest'

// Regression tests for the unified log store (issue #328, #454).
// The scenarios below pin the invariants that the console depends on:
//   1. The FIRST PAINT materializes every activity row of the newest page,
//      even when the newest terminal's run-log expansion exceeds the step
//      budget (the budget bounds RUN-LOG lines only).
//   2. Streamed (WS) run-log lines for a live run are inserted in
//      CHRONOLOGICAL position, never appended at the bottom.
//   3. The newest set stays at the bottom of the buffer across the refresh
//      sequence (load -> fillViewport -> loadAll -> poll merges).
//   4. Concurrent history loaders (loadAll, search) share the in-flight
//      loadOlder promise, yield to the macrotask event loop, and terminate
//      when no progress is made (#454).

const { defaultEntries, defaultRunLogs } = vi.hoisted(() => {
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
  return { defaultEntries: entries, defaultRunLogs: runLogs }
})

vi.mock('./api.js', () => ({
  api: {
    getActivity: vi.fn(),
    getRunLogs: vi.fn(),
  },
}))

let wsHandler = null
vi.mock('./ws.svelte.js', () => ({
  onWsMessage: (fn) => { wsHandler = fn; return () => { wsHandler = null } },
}))

import { createUnifiedLogStore } from './unifiedlog.svelte.js'
import { api } from './api.js'

function bottomIds(store, n) {
  return store.entries.slice(-n).map(e => `${e.type}:${e.id}@${new Date(e.ts).toISOString().slice(11, 19)}`)
}

function deferred() {
  let resolve, reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('unified log store', () => {
  let store
  beforeEach(() => {
    wsHandler = null
    api.getActivity.mockImplementation(async (limit = 30, category = '', beforeId = 0) => {
      let rows = defaultEntries
      if (category) rows = rows.filter(r => r.category === category)
      if (beforeId) rows = rows.filter(r => r.id < beforeId)
      return rows.slice(0, limit)
    })
    api.getRunLogs.mockImplementation(async (runId) => ({ entries: defaultRunLogs[runId] || [] }))
    store = createUnifiedLogStore()
  })

  it('first paint includes every activity row even when a terminal expansion exceeds the budget', async () => {
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

    const baseline = bottomIds(store, 3)
    expect(baseline.every(s => s.includes('15:58:0'))).toBe(true)

    // Stream 3 lines for the ACTIVE run 7 (ts 15:54:10..30 — mid-buffer)
    wsHandler({ type: 'run_log', entry: { id: 70001, run_id: 7, level: 'info', message: 'docker: inspecting container', data: '', ts: '2026-08-21T15:54:10Z' } })
    wsHandler({ type: 'run_log', entry: { id: 70002, run_id: 7, level: 'info', message: 'docker: stopping container', data: '', ts: '2026-08-21T15:54:20Z' } })
    wsHandler({ type: 'run_log', entry: { id: 70003, run_id: 7, level: 'info', message: 'docker: backing up', data: '', ts: '2026-08-21T15:54:30Z' } })

    expect(bottomIds(store, 3)).toEqual(baseline)

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

  it('yields to macrotask queue when loadOlder is pending during loadAll', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    const hold = deferred()
    let olderCalls = 0

    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      olderCalls++
      return hold.promise
    })

    try {
      await store.load()
      expect(store.hasMore).toBe(true)

      let sentinelFired = false
      const timer = setTimeout(() => {
        sentinelFired = true
      }, 5)

      let loadOlderCalls = 0
      const origLoadOlder = store.loadOlder
      store.loadOlder = (...args) => {
        loadOlderCalls++
        if (loadOlderCalls > 50) {
          store.setError('busy loop detected')
          clearTimeout(timer)
        }
        return origLoadOlder.apply(store, args)
      }

      const olderPromise = store.loadOlder()
      const allPromise = store.loadAll()

      await new Promise((resolve) => setTimeout(resolve, 20))

      if (!sentinelFired) {
        store.setError('stop')
        hold.resolve([])
      }
      expect(sentinelFired).toBe(true)
      expect(loadOlderCalls).toBeLessThanOrEqual(2)
      expect(olderCalls).toBe(1)

      hold.resolve([])
      await olderPromise
      await allPromise
      expect(store.loadingOlder).toBe(false)
    } finally {
      hold.resolve([])
    }
  })

  it('yields to macrotask queue when loadOlder is pending during setSearchFilter', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    const hold = deferred()
    let olderCalls = 0

    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      olderCalls++
      return hold.promise
    })

    try {
      await store.load()
      expect(store.hasMore).toBe(true)

      let sentinelFired = false
      const timer = setTimeout(() => {
        sentinelFired = true
      }, 5)

      let loadOlderCalls = 0
      const origLoadOlder = store.loadOlder
      store.loadOlder = (...args) => {
        loadOlderCalls++
        if (loadOlderCalls > 50) {
          store.setError('busy loop detected')
          store.setSearchFilter('')
          clearTimeout(timer)
        }
        return origLoadOlder.apply(store, args)
      }

      const olderPromise = store.loadOlder()
      store.setSearchFilter('needle')

      await new Promise((resolve) => setTimeout(resolve, 20))

      if (!sentinelFired) {
        store.setError('stop')
        store.setSearchFilter('')
        hold.resolve([])
      }
      expect(sentinelFired).toBe(true)
      expect(loadOlderCalls).toBeLessThanOrEqual(2)
      expect(olderCalls).toBe(1)

      hold.resolve([])
      await olderPromise
      await vi.waitFor(() => expect(store.searching).toBe(false))
      expect(store.loadingOlder).toBe(false)
    } finally {
      hold.resolve([])
    }
  })

  it('discards older page for stale category and does not block new category loadAll', async () => {
    const backupRows = Array.from({ length: 30 }, (_, i) => ({
      id: 2000 - i,
      level: 'info',
      category: 'backup',
      message: `backup ${2000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (2000 - i) * 1000).toISOString(),
    }))
    const restoreRows = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'restore',
      message: `restore ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    const holdA = deferred()

    api.getActivity.mockImplementation(async (limit = 30, category = '', beforeId = 0) => {
      if (category === 'backup') {
        if (!beforeId) return backupRows.slice(0, limit)
        return holdA.promise
      }
      if (category === 'restore') {
        if (!beforeId) return restoreRows.slice(0, limit)
        return []
      }
      return []
    })

    try {
      await store.setCategory('backup')
      expect(store.hasMore).toBe(true)

      const olderAPromise = store.loadOlder()

      await store.setCategory('restore')

      const olderBackupRows = Array.from({ length: 30 }, (_, i) => ({
        id: 1970 - i,
        level: 'info',
        category: 'backup',
        message: `backup ${1970 - i}`,
        details: null,
        created_at: new Date(Date.UTC(2026, 7, 21, 14, 0, 0) + (1970 - i) * 1000).toISOString(),
      }))
      holdA.resolve(olderBackupRows)
      await olderAPromise

      expect(store.category).toBe('restore')
      expect(store.entries.every(e => e.category === 'restore')).toBe(true)
      expect(store.entries.some(e => e.category === 'backup')).toBe(false)
      expect(store.hasMore).toBe(false)
      expect(store.loadingOlder).toBe(false)
    } finally {
      holdA.resolve([])
    }
  })

  it('terminates loadAll after bounded calls when cursor does not advance', async () => {
    const fixedRows = Array.from({ length: 30 }, (_, i) => ({
      id: 500 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${500 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (500 - i) * 1000).toISOString(),
    }))

    let calls = 0
    api.getActivity.mockImplementation(async (limit = 30, _category = '', _beforeId = 0) => {
      calls++
      return fixedRows.slice(0, limit)
    })

    await store.load()
    expect(store.hasMore).toBe(true)
    const initialCalls = calls

    await store.loadAll()
    expect(calls - initialCalls).toBe(1)
    expect(store.hasMore).toBe(false)
    expect(store.loadingOlder).toBe(false)
  })

  it('terminates loadAll after one silent failure and preserves existing rows', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    let calls = 0
    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      calls++
      if (beforeId) {
        throw new Error('network down')
      }
      return plain30.slice(0, limit)
    })

    await store.load()
    expect(store.hasMore).toBe(true)
    const rowCountBefore = store.entries.length
    expect(rowCountBefore).toBe(30)

    await store.loadAll()
    expect(calls).toBe(2)
    expect(store.entries.length).toBe(rowCountBefore)
    expect(store.error).toBe('')
    expect(store.hasMore).toBe(true)
    expect(store.loadingOlder).toBe(false)
  })

  it('dispose increments sequences and stops background loading without clearing entries', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    const hold = deferred()
    let calls = 0
    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      calls++
      if (!beforeId) return plain30.slice(0, limit)
      return hold.promise
    })

    await store.load()
    const count = store.entries.length
    expect(count).toBe(30)
    expect(store.hasMore).toBe(true)

    // Start loadAll which triggers an older-page load that is held
    const allPromise = store.loadAll()
    expect(calls).toBe(2)

    // Dispose while older load is pending
    store.dispose()

    // Release held older rows
    const olderRows = Array.from({ length: 30 }, (_, i) => ({
      id: 970 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${970 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 14, 0, 0) + (970 - i) * 1000).toISOString(),
    }))
    hold.resolve(olderRows)
    await allPromise

    // Stale older rows must not be added to store entries
    expect(store.entries.length).toBe(count)
    // No subsequent calls should have run
    expect(calls).toBe(2)
    expect(store.loadingOlder).toBe(false)
  })

  it('loadOlder expands terminal run logs with split budgeting and smooth latency', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    const older30 = [
      {
        id: 970,
        level: 'info',
        category: 'backup',
        message: 'Backup completed: old-job',
        details: JSON.stringify({ run_id: 88, run_log: true, job_id: 2, job_name: 'old-job' }),
        created_at: '2026-08-21T14:58:00Z',
      },
      ...Array.from({ length: 29 }, (_, i) => ({
        id: 969 - i,
        level: 'info',
        category: 'backup',
        message: `msg ${969 - i}`,
        details: null,
        created_at: new Date(Date.UTC(2026, 7, 21, 14, 0, 0) + (969 - i) * 1000).toISOString(),
      })),
    ]

    const runLogs88 = Array.from({ length: 200 }, (_, i) => ({
      id: 88000 + i,
      run_id: 88,
      level: 'info',
      message: `run 88 line ${i}`,
      data: '',
      ts: new Date(Date.UTC(2026, 7, 21, 14, 50, 0) + i * 500).toISOString(),
    }))

    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      return older30.slice(0, limit)
    })
    api.getRunLogs.mockImplementation(async (runId) => {
      if (Number(runId) === 88) return { entries: runLogs88 }
      return { entries: [] }
    })

    await store.load()
    expect(store.hasMore).toBe(true)

    // Call loadOlder with smooth: true
    const outcome = await store.loadOlder({ smooth: true })
    expect(outcome).toBe('advanced')
    expect(store.hasMore).toBe(true)

    // Next loadOlder drains pending run-log lines
    const drainOutcome = await store.loadOlder()
    expect(drainOutcome).toBe('advanced')
  })

  it('loadOlder surfaces error when not silent', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      throw new Error('fetch older failed')
    })

    await store.load()
    const outcome = await store.loadOlder({ silent: false })
    expect(outcome).toBe('failed')
    expect(store.error).toBe('fetch older failed')
  })

  it('setSearchFilter pins full history when search completes all older pages', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      return []
    })

    await store.load()
    expect(store.hasMore).toBe(true)

    store.setSearchFilter('needle')
    await vi.waitFor(() => expect(store.searching).toBe(false))
    expect(store.hasMore).toBe(false)
  })

  it('dispose prevents post-dispose calls and clears loading states', async () => {
    let resolveActivity
    api.getActivity.mockImplementation(() => new Promise((resolve) => {
      resolveActivity = resolve
    }))

    const pendingLoad = store.load()
    expect(store.loading).toBe(true)

    store.dispose()
    expect(store.loading).toBe(false)
    expect(store.loadingOlder).toBe(false)

    // Late resolve should not mutate store or resurrect loading state
    resolveActivity([])
    await pendingLoad
    expect(store.loading).toBe(false)
    expect(store.entries).toEqual([])

    // Subsequent calls are no-ops
    api.getActivity.mockClear()
    expect(await store.loadOlder()).toBe('no-progress')
    await store.loadAll()
    await store.setCategory('backup')
    store.setSearchFilter('test')
    expect(api.getActivity).not.toHaveBeenCalled()
  })

  it('surfaces error when non-silent caller joins in-flight silent loadOlder', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    let rejectOlder
    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      return new Promise((_, reject) => {
        rejectOlder = reject
      })
    })

    await store.load()
    expect(store.hasMore).toBe(true)

    // First caller: silent background load
    const silentPromise = store.loadOlder({ silent: true })
    // Second caller: user scrolling (non-silent) joins the in-flight load
    const userPromise = store.loadOlder({ silent: false })

    rejectOlder(new Error('network error'))
    const [res1, res2] = await Promise.all([silentPromise, userPromise])

    expect(res1).toBe('failed')
    expect(res2).toBe('failed')
    expect(store.error).toBe('network error')
  })

  it('setSearchFilter retries transient failures during older loads before stopping', async () => {
    const plain30 = Array.from({ length: 30 }, (_, i) => ({
      id: 1000 - i,
      level: 'info',
      category: 'backup',
      message: `msg ${1000 - i}`,
      details: null,
      created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + (1000 - i) * 1000).toISOString(),
    }))

    let attempts = 0
    api.getActivity.mockImplementation(async (limit = 30, _category = '', beforeId = 0) => {
      if (!beforeId) return plain30.slice(0, limit)
      attempts++
      if (attempts === 1) throw new Error('transient error')
      return []
    })

    await store.load()
    expect(store.hasMore).toBe(true)

    store.setSearchFilter('needle')
    await vi.waitFor(() => expect(store.searching).toBe(false), { timeout: 3000 })
    expect(attempts).toBe(2)
    expect(store.hasMore).toBe(false)
  })
})
