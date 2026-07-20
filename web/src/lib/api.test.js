import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, setReplicaMode } from './api.js'

describe('replica discovery', () => {
  afterEach(() => {
    setReplicaMode(false)
    vi.unstubAllGlobals()
  })

  it('does not request daemon-only discovery routes', async () => {
    const fetch = vi.fn(() => {
      throw new Error('replica must not fetch daemon-only discovery routes')
    })
    vi.stubGlobal('fetch', fetch)
    setReplicaMode(true)

    const results = await Promise.all([
      api.listContainers(),
      api.listVMs(),
      api.listFolders(),
      api.listPlugins(),
      api.listZFSDatasets(),
    ])

    expect(fetch).not.toHaveBeenCalled()
    expect(results).toEqual(Array(5).fill({ items: [], available: false }))
  })
})

describe('history', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('loads all jobs through one bounded history request', async () => {
    const fetch = vi.fn(async () => new Response('[]', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.getHistory(200)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/history?limit_per_job=200')
  })
})

describe('jobs', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('requests bulk details when a page needs items and baselines', async () => {
    const fetch = vi.fn(async () => new Response('[]', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.listJobs(true)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/jobs?details=true')
  })
})
