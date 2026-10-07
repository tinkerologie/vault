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

describe('queue', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('cancels queued entry by id', async () => {
    const fetch = vi.fn(async () => new Response('{"message":"queue entry cancellation requested","entry_id":"q-123"}', {
      status: 202,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.cancelQueueEntry('q-123')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/queue/q-123/cancel')
    expect(fetch.mock.calls[0][1].method).toBe('POST')
  })
})

describe('storage file download', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('downloads storage file as blob when response is ok', async () => {
    const fakeBlob = new Blob(['binary data'], { type: 'application/octet-stream' })
    const fetch = vi.fn(async () => new Response(fakeBlob, {
      status: 200,
      headers: { 'content-disposition': 'attachment; filename="test.tar.zst"' },
    }))
    vi.stubGlobal('fetch', fetch)

    const blob = await api.downloadStorageFile(42, 'backups/test.tar.zst')

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/42/files?path=backups%2Ftest.tar.zst')
    expect(blob).toBeInstanceOf(Blob)
  })

  it('throws error with message from error json when response is not ok', async () => {
    const fetch = vi.fn(async () => new Response('{"error":"file not found: missing"}', {
      status: 404,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await expect(api.downloadStorageFile(42, 'backups/missing.tar.zst'))
      .rejects.toThrow('file not found: missing')
  })

  it('throws authorization error on 401 response', async () => {
    const fetch = vi.fn(async () => new Response('Unauthorized', {
      status: 401,
      headers: { 'content-type': 'text/plain' },
    }))
    vi.stubGlobal('fetch', fetch)

    await expect(api.downloadStorageFile(42, 'backups/test.tar.zst'))
      .rejects.toThrow('Not authorized — your session or API key may have expired.')
  })

  it('falls back to text body when response error is non-JSON', async () => {
    const fetch = vi.fn(async () => new Response('Bad Gateway from proxy', {
      status: 502,
      headers: { 'content-type': 'text/plain' },
    }))
    vi.stubGlobal('fetch', fetch)

    await expect(api.downloadStorageFile(42, 'backups/test.tar.zst'))
      .rejects.toThrow('Bad Gateway from proxy')
  })
})

describe('mounts', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('lists active mounts with active filter', async () => {
    const fetch = vi.fn(async () => new Response('[]', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.listMounts(true)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/mounts?active=true')
  })

  it('gets mount details by id', async () => {
    const fetch = vi.fn(async () => new Response('{"id":42}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    const res = await api.getMount(42)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/mounts/42')
    expect(res).toEqual({ id: 42 })
  })

  it('mounts restore point for job', async () => {
    const fetch = vi.fn(async () => new Response('{"id":1,"status":"active"}', {
      status: 201,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    const res = await api.mountRestorePoint(10, 20)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/jobs/10/restore-points/20/mount')
    expect(fetch.mock.calls[0][1]?.method).toBe('POST')
    expect(res).toEqual({ id: 1, status: 'active' })
  })

  it('unmounts session by id', async () => {
    const fetch = vi.fn(async () => new Response('{"ok":true}', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.unmount(5)

    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/mounts/5/unmount')
    expect(fetch.mock.calls[0][1]?.method).toBe('POST')
  })
})


describe('restore-point contents timeout (issue #449)', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  // fetch that never resolves on its own and rejects like the browser when
  // the request's AbortSignal fires.
  function stubHangingFetch() {
    const fetch = vi.fn((_url, { signal }) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => {
        reject(new DOMException('The operation was aborted.', 'AbortError'))
      })
    }))
    vi.stubGlobal('fetch', fetch)
    return fetch
  }

  it('waits past the default 15 s and aborts at 130 s', async () => {
    vi.useFakeTimers()
    const fetch = stubHangingFetch()

    const pending = api.getRestorePointContents(1, 2, 'appdata')
    let settled = false
    pending.then(() => { settled = true }, () => { settled = true })

    await vi.advanceTimersByTimeAsync(15000)
    expect(settled).toBe(false)
    expect(fetch.mock.calls[0][1].signal.aborted).toBe(false)

    const assertion = expect(pending).rejects.toThrow('Request timed out')
    await vi.advanceTimersByTimeAsync(130000 - 15000)
    await assertion
  })

  it('keeps the 15 s default for other requests', async () => {
    vi.useFakeTimers()
    stubHangingFetch()

    const pending = api.listJobs()
    const assertion = expect(pending).rejects.toThrow('Request timed out')
    await vi.advanceTimersByTimeAsync(15000)
    await assertion
  })

  it('exposes the HTTP status on error responses', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"vault daemon request timed out"}', {
      status: 504,
      headers: { 'content-type': 'application/json' },
    })))

    const err = await api.getRestorePointContents(1, 2, 'appdata').catch(e => e)
    expect(err).toBeInstanceOf(Error)
    expect(err.message).toBe('vault daemon request timed out')
    expect(err.status).toBe(504)
  })

  it('exposes the status on 401 responses too', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"unauthorized"}', {
      status: 401,
      headers: { 'content-type': 'application/json' },
    })))

    const err = await api.getRestorePointContents(1, 2, 'appdata').catch(e => e)
    expect(err.message).toBe('Not authorized — your session or API key may have expired.')
    expect(err.status).toBe(401)
  })
})

describe('dedup key recovery (issue #451)', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('posts the passphrase to the rewrap route with the long timeout', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn((_url, { signal }) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))
    vi.stubGlobal('fetch', fetch)

    const pending = api.rewrapDedupKeys('pw')
    let settled = false
    pending.then(() => { settled = true }, () => { settled = true })
    await vi.advanceTimersByTimeAsync(15000)
    expect(settled).toBe(false)
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/storage/dedup-keys/rewrap')
    expect(fetch.mock.calls[0][1].method).toBe('POST')
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ passphrase: 'pw' })
    const assertion = expect(pending).rejects.toThrow('Request timed out')
    await vi.advanceTimersByTimeAsync(130000 - 15000)
    await assertion
  })

  it('downloads the server key as a blob and surfaces errors with status', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Uint8Array(32), { status: 200 })))
    const blob = await api.downloadServerKey()
    expect(blob.size).toBe(32)

    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"server key is not configured"}', {
      status: 503, headers: { 'content-type': 'application/json' },
    })))
    const err = await api.downloadServerKey().catch(e => e)
    expect(err.message).toBe('server key is not configured')
    expect(err.status).toBe(503)
  })
})
