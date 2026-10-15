import { beforeEach, describe, expect, it, vi } from 'vitest'

// Additional unified log store coverage (issue #458): the live WebSocket
// branches, client-side filters, reset, category switching and the merge
// (poll) path. api.js and ws.svelte.js are mocked.

vi.mock('./api.js', () => ({
  api: {
    getActivity: vi.fn(),
    getRunLogs: vi.fn(),
  },
}))

let wsHandler = null
vi.mock('./ws.svelte.js', () => ({
  onWsMessage: (fn) => {
    wsHandler = fn
    return () => { wsHandler = null }
  },
}))

import { createUnifiedLogStore } from './unifiedlog.svelte.js'
import { api } from './api.js'

const t = (sec) => new Date(Date.UTC(2026, 0, 1, 12, 0, sec)).toISOString()

function activity(id, sec, extra = {}) {
  return { id, level: 'info', category: 'backup', message: `a${id}`, created_at: t(sec), ...extra }
}

function runLine(id, runId, sec, extra = {}) {
  return { id, run_id: runId, level: 'info', message: `rl${id}`, ts: t(sec), ...extra }
}

beforeEach(() => {
  wsHandler = null
  api.getActivity.mockReset()
  api.getRunLogs.mockReset()
  api.getRunLogs.mockResolvedValue({ entries: [] })
})

describe('live WebSocket updates', () => {
  it('appends plain activity rows and dedupes repeated ids', async () => {
    const store = createUnifiedLogStore()
    const unsub = store.setupWs()
    wsHandler({ type: 'activity', entry: activity(1, 1, { details: JSON.stringify({ job_id: 3, job_name: 'docker' }) }) })
    wsHandler({ type: 'activity', entry: activity(1, 1) })
    expect(store.entries).toHaveLength(1)
    expect(store.entries[0]).toMatchObject({ type: 'activity', jobName: 'docker', jobId: 3 })
    unsub()
    expect(wsHandler).toBeNull()
  })

  it('ignores unrelated messages and activity without an entry', () => {
    const store = createUnifiedLogStore()
    store.setupWs()
    wsHandler({ type: 'activity' })
    wsHandler({ type: 'run_log' })
    wsHandler({ type: 'queue_update' })
    expect(store.entries).toEqual([])
  })

  it('caps the WS dedup ring', () => {
    const store = createUnifiedLogStore()
    store.setupWs()
    for (let i = 0; i < 205; i++) wsHandler({ type: 'activity', entry: activity(i, i % 60) })
    expect(store.entries).toHaveLength(205)
    // The oldest ids fell out of the ring, so a replay is accepted again.
    wsHandler({ type: 'activity', entry: activity(0, 0) })
    expect(store.entries.filter((e) => e.id === 0)).toHaveLength(2)
  })

  it('drops activity from another category when a category is active', async () => {
    api.getActivity.mockResolvedValue([])
    const store = createUnifiedLogStore()
    store.setupWs()
    await store.setCategory('health')
    wsHandler({ type: 'activity', entry: activity(1, 1, { category: 'backup' }) })
    expect(store.entries).toEqual([])
    store.dispose()
  })

  it('keeps a terminal summary only when no run-log lines streamed for the run', () => {
    const store = createUnifiedLogStore()
    store.setupWs()
    const terminal = (id, runId) => activity(id, 30, { details: JSON.stringify({ run_id: runId, run_log: true }) })
    wsHandler({ type: 'activity', entry: terminal(1, 7) })
    expect(store.entries.map((e) => e.type)).toEqual(['summary'])

    wsHandler({ type: 'activity', entry: activity(2, 1, { details: JSON.stringify({ run_id: 8, job_name: 'vms', job_id: 2 }) }) })
    wsHandler({ type: 'run_log', entry: runLine(50, 8, 10, { data: JSON.stringify({ k: 1 }) }) })
    wsHandler({ type: 'activity', entry: terminal(3, 8) })
    const runEntries = store.entries.filter((e) => e.runId === 8)
    expect(runEntries.map((e) => e.type)).toEqual(['activity', 'runlog'])
    expect(runEntries[1]).toMatchObject({ jobName: 'vms', jobId: 2, meta: { k: 1 } })
  })

  it('inherits run context for streamed lines, defaulting when the run is unknown', () => {
    const store = createUnifiedLogStore()
    store.setupWs()
    wsHandler({ type: 'run_log', entry: runLine(1, 99, 5, { level: '' }) })
    expect(store.entries[0]).toMatchObject({ id: 'rl-1', category: 'backup', jobName: '', jobId: null, level: 'info', meta: null })
  })

  it('drops streamed lines whose run is outside the active category', async () => {
    api.getActivity.mockResolvedValue([])
    const store = createUnifiedLogStore()
    store.setupWs()
    await store.setCategory('restore')
    wsHandler({ type: 'run_log', entry: runLine(1, 99, 5) })
    expect(store.entries).toEqual([])
    wsHandler({ type: 'activity', entry: activity(2, 1, { category: 'restore', details: JSON.stringify({ run_id: 99 }) }) })
    wsHandler({ type: 'run_log', entry: runLine(3, 99, 6) })
    expect(store.entries.map((e) => e.id)).toEqual([2, 'rl-3'])
    expect(store.entries[1].category).toBe('restore')
    store.dispose()
  })
})

describe('client-side filters', () => {
  function seeded() {
    const store = createUnifiedLogStore()
    store.setupWs()
    wsHandler({ type: 'activity', entry: activity(1, 1, { level: 'warning', details: JSON.stringify({ job_id: 1, job_name: 'b' }) }) })
    wsHandler({ type: 'activity', entry: activity(2, 2, { level: 'warn', details: JSON.stringify({ job_id: 2, job_name: 'a' }) }) })
    wsHandler({ type: 'activity', entry: activity(3, 3, { level: 'error', message: 'disk exploded' }) })
    return store
  }

  it('lists distinct job names, sorted', () => {
    expect(seeded().jobs).toEqual(['a', 'b'])
  })

  it('treats warn and warning as the same level', () => {
    const store = seeded()
    store.setLevelFilter('warn')
    expect(store.levelFilter).toBe('warn')
    expect(store.filtered.map((e) => e.id)).toEqual([1, 2])
    store.setLevelFilter('error')
    expect(store.filtered.map((e) => e.id)).toEqual([3])
    store.setLevelFilter('')
    expect(store.filtered).toHaveLength(3)
  })

  it('filters by job and composes with level', () => {
    const store = seeded()
    store.setJobFilter('a')
    expect(store.jobFilter).toBe('a')
    expect(store.filtered.map((e) => e.id)).toEqual([2])
    store.setLevelFilter('error')
    expect(store.filtered).toEqual([])
  })

  it('filters by search text without background loading when the buffer is complete', () => {
    const store = seeded()
    store.setSearchFilter('exploded')
    expect(store.search).toBe('exploded')
    expect(store.searching).toBe(false)
    expect(store.filtered.map((e) => e.id)).toEqual([3])
    store.setSearchFilter('')
    expect(store.filtered).toHaveLength(3)
    expect(api.getActivity).not.toHaveBeenCalled()
  })
})

describe('reset, errors and category switching', () => {
  it('reset clears the buffer and load state', async () => {
    api.getActivity.mockResolvedValue([activity(1, 1)])
    const store = createUnifiedLogStore()
    await store.load()
    expect(store.loaded).toBe(true)
    expect(store.entries).toHaveLength(1)
    store.setError('boom')
    expect(store.error).toBe('boom')
    store.reset()
    expect(store.entries).toEqual([])
    expect(store.loaded).toBe(false)
    expect(store.error).toBe('')
    expect(store.hasMore).toBe(false)
    expect(store.pruned).toBe(false)
    expect(store.loading).toBe(false)
    expect(store.loadingOlder).toBe(false)
  })

  it('surfaces a load error, with a default message when none is given', async () => {
    const store = createUnifiedLogStore()
    api.getActivity.mockRejectedValueOnce(new Error('down'))
    await store.load()
    expect(store.error).toBe('down')
    api.getActivity.mockRejectedValueOnce({})
    await store.load()
    expect(store.error).toBe('Failed to load logs')
    expect(store.loading).toBe(false)
  })

  it('treats a null activity page as empty', async () => {
    api.getActivity.mockResolvedValue(null)
    const store = createUnifiedLogStore()
    await store.load()
    expect(store.entries).toEqual([])
    expect(store.loaded).toBe(true)
  })

  it('setCategory reloads for the category and clears a job filter with no entries left', async () => {
    api.getActivity.mockResolvedValue([activity(1, 1, { category: 'health' })])
    const store = createUnifiedLogStore()
    store.setJobFilter('gone')
    await store.setCategory('health')
    expect(store.category).toBe('health')
    expect(api.getActivity).toHaveBeenCalledWith(30, 'health')
    expect(store.jobFilter).toBe('')
    store.dispose()
  })

  it('setCategory keeps a job filter that still has entries', async () => {
    api.getActivity.mockResolvedValue([activity(1, 1, { details: JSON.stringify({ job_id: 1, job_name: 'keep' }) })])
    const store = createUnifiedLogStore()
    store.setJobFilter('keep')
    await store.setCategory('backup')
    expect(store.jobFilter).toBe('keep')
    store.dispose()
  })

  it('setCategory and setSearchFilter are no-ops after dispose', async () => {
    const store = createUnifiedLogStore()
    store.dispose()
    await store.setCategory('x')
    store.setSearchFilter('y')
    expect(store.category).toBe('')
    expect(store.search).toBe('')
    await store.loadNewer()
    expect(api.getActivity).not.toHaveBeenCalled()
  })

  it('setCategory stops if the store is disposed while loading', async () => {
    let resolve
    api.getActivity.mockImplementationOnce(() => new Promise((r) => { resolve = r }))
    const store = createUnifiedLogStore()
    store.setJobFilter('gone')
    const pending = store.setCategory('health')
    store.dispose()
    resolve([])
    await pending
    expect(store.jobFilter).toBe('gone')
    expect(api.getActivity).toHaveBeenCalledTimes(1)
  })
})

describe('merge (poll) path', () => {
  it('merges a newer page without duplicating rows and expands new terminals', async () => {
    api.getActivity.mockResolvedValueOnce([activity(2, 2), activity(1, 1)])
    const store = createUnifiedLogStore()
    await store.load()
    api.getActivity.mockResolvedValueOnce([
      activity(4, 10, { details: JSON.stringify({ run_id: 5, run_log: true, job_name: 'j', job_id: 1 }) }),
      activity(3, 3),
      activity(2, 2),
    ])
    api.getRunLogs.mockResolvedValueOnce({ entries: [runLine(1, 5, 8), runLine(2, 5, 9)] })
    await store.loadNewer()
    expect(store.entries.map((e) => e.id)).toEqual([1, 2, 3, 'rl-1', 'rl-2'])
    expect(api.getRunLogs).toHaveBeenCalledWith('5', { tail: true, limit: 1000 })
  })

  it('retries an expanded terminal whose run log was empty, then stops once lines exist', async () => {
    const terminal = activity(4, 10, { details: JSON.stringify({ run_id: 5, run_log: true }) })
    api.getActivity.mockResolvedValue([terminal])
    api.getRunLogs.mockRejectedValueOnce(new Error('transient'))
    const store = createUnifiedLogStore()
    await store.load()
    expect(store.entries.map((e) => e.type)).toEqual(['summary'])

    api.getRunLogs.mockResolvedValueOnce({ entries: [runLine(1, 5, 9)] })
    await store.loadNewer()
    expect(store.entries.map((e) => e.id)).toEqual(['rl-1'])

    await store.loadNewer()
    expect(api.getRunLogs).toHaveBeenCalledTimes(2)
  })

  it('discards a stale merge response superseded by a newer load', async () => {
    api.getActivity.mockResolvedValueOnce([activity(1, 1)])
    const store = createUnifiedLogStore()
    await store.load()
    let resolveOld
    api.getActivity.mockImplementationOnce(() => new Promise((r) => { resolveOld = r }))
    const stale = store.loadNewer()
    api.getActivity.mockResolvedValueOnce([activity(9, 9)])
    await store.load()
    resolveOld([activity(5, 5)])
    await stale
    expect(store.entries.map((e) => e.id)).toEqual([9])
  })
})
