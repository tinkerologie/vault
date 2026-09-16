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

describe('history trend', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('requests the size trend by default', async () => {
    const fetch = vi.fn(async () => new Response('{}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.getHistoryTrend('30d')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/history/trend?period=30d&metric=size')
  })

  it('requests the duration trend when metric is duration', async () => {
    const fetch = vi.fn(async () => new Response('{}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.getHistoryTrend('7d', 'duration')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/history/trend?period=7d&metric=duration')
  })
})

describe('storage scan and import', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('scans storage with default parameters', async () => {
    const fetch = vi.fn(async () => new Response('{"backups":[]}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.scanStorage(42)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/42/scan')
    expect(fetch.mock.calls[0][1].body).toBeUndefined()
  })

  it('scans storage with custom path and passphrase', async () => {
    const fetch = vi.fn(async () => new Response('{"backups":[]}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.scanStorage(42, 'backups/sub', 'secret123')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/42/scan?path=backups%2Fsub&passphrase=secret123')
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      path: 'backups/sub',
      passphrase: 'secret123',
    })
  })

  it('imports backups without passphrase', async () => {
    const fetch = vi.fn(async () => new Response('{"imported":1,"total":1}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.importBackups(42, [{ job_name: 'job1' }])

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/42/import')
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      backups: [{ job_name: 'job1' }],
    })
  })

  it('imports backups with passphrase', async () => {
    const fetch = vi.fn(async () => new Response('{"imported":1,"total":1}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.importBackups(42, [{ job_name: 'job1' }], 'secret123')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/42/import')
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      backups: [{ job_name: 'job1' }],
      passphrase: 'secret123',
    })
  })
})

