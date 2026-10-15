import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api, isReplicaMode, setReplicaMode } from './api.js'

// Route contract tests for the API client (issue #458): each method must hit
// the documented method + path and send the expected JSON body. fetch is
// stubbed; no network is used.

let fetchMock

function respond(body, status = 200) {
  return new Response(body === null ? null : JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

beforeEach(() => {
  fetchMock = vi.fn(async () => respond({ ok: true }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  setReplicaMode(false)
  vi.unstubAllGlobals()
})

function lastCall() {
  const [url, opts] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
  return { url, method: opts.method, body: opts.body === undefined ? undefined : JSON.parse(opts.body) }
}

// [name, invoke, method, url, body?]
const routes = [
  ['health', () => api.health(), 'GET', '/api/v1/health'],
  ['getHealthSummary', () => api.getHealthSummary(), 'GET', '/api/v1/health/summary'],
  ['getLatestRelease', () => api.getLatestRelease(), 'GET', '/api/v1/release/latest'],
  ['listStorage', () => api.listStorage(), 'GET', '/api/v1/storage'],
  ['getStorage', () => api.getStorage(4), 'GET', '/api/v1/storage/4'],
  ['createStorage', () => api.createStorage({ name: 'a' }), 'POST', '/api/v1/storage', { name: 'a' }],
  ['updateStorage', () => api.updateStorage(4, { name: 'b' }), 'PUT', '/api/v1/storage/4', { name: 'b' }],
  ['deleteStorage default', () => api.deleteStorage(4), 'DELETE', '/api/v1/storage/4'],
  ['deleteStorage deleteFiles', () => api.deleteStorage(4, { deleteFiles: true }), 'DELETE', '/api/v1/storage/4?deleteFiles=true'],
  ['deleteStorage both', () => api.deleteStorage(4, { deleteFiles: true, force: true }), 'DELETE', '/api/v1/storage/4?deleteFiles=true&force=true'],
  ['testStorage', () => api.testStorage(4), 'POST', '/api/v1/storage/4/test'],
  ['testStorageConfig', () => api.testStorageConfig({ type: 'local' }), 'POST', '/api/v1/storage/test', { type: 'local' }],
  ['refreshCapacity', () => api.refreshCapacity(4), 'POST', '/api/v1/storage/4/capacity-check'],
  ['closeBreaker', () => api.closeBreaker(4), 'POST', '/api/v1/storage/4/breaker/close'],
  ['restoreDB verify', () => api.restoreDB(4, 'p.db', 'pw', true), 'POST', '/api/v1/storage/4/restore-db', { storage_path: 'p.db', passphrase: 'pw', verify_only: true }],
  ['restoreDB real', () => api.restoreDB(4, 'p.db'), 'POST', '/api/v1/storage/4/restore-db', { storage_path: 'p.db', passphrase: '', verify_only: false }],
  ['listDBBackups', () => api.listDBBackups(4), 'GET', '/api/v1/storage/4/db-backups'],
  ['getDependentJobs', () => api.getDependentJobs(4), 'GET', '/api/v1/storage/4/jobs'],
  ['listJobs', () => api.listJobs(), 'GET', '/api/v1/jobs'],
  ['getJob', () => api.getJob(2), 'GET', '/api/v1/jobs/2'],
  ['createJob', () => api.createJob({ name: 'j' }), 'POST', '/api/v1/jobs', { name: 'j' }],
  ['updateJob', () => api.updateJob(2, { name: 'k' }), 'PUT', '/api/v1/jobs/2', { name: 'k' }],
  ['deleteJob', () => api.deleteJob(2), 'DELETE', '/api/v1/jobs/2'],
  ['deleteJob files', () => api.deleteJob(2, true), 'DELETE', '/api/v1/jobs/2?deleteFiles=true'],
  ['getStaleItems', () => api.getStaleItems(2), 'GET', '/api/v1/jobs/2/stale-items'],
  ['deleteJobItem', () => api.deleteJobItem(2, 7), 'DELETE', '/api/v1/jobs/2/items/7'],
  ['removeStaleItems', () => api.removeStaleItems(2), 'POST', '/api/v1/jobs/2/stale-items/remove'],
  ['getJobHistory default', () => api.getJobHistory(2), 'GET', '/api/v1/jobs/2/history?limit=50'],
  ['getJobHistory limit', () => api.getJobHistory(2, 5), 'GET', '/api/v1/jobs/2/history?limit=5'],
  ['healthCheckStorage', () => api.healthCheckStorage(4), 'POST', '/api/v1/storage/4/health-check'],
  ['scanStorageOrphans', () => api.scanStorageOrphans(4), 'POST', '/api/v1/storage/4/scan-orphans'],
  ['deleteStorageOrphans', () => api.deleteStorageOrphans(4, ['a']), 'POST', '/api/v1/storage/4/delete-orphans', { paths: ['a'] }],
  ['dedupStats', () => api.dedupStats(4), 'GET', '/api/v1/storage/4/dedup-stats'],
  ['runDedupGC', () => api.runDedupGC(4), 'POST', '/api/v1/storage/4/gc'],
  ['getRestorePoints', () => api.getRestorePoints(2), 'GET', '/api/v1/jobs/2/restore-points'],
  ['getRetentionPreview', () => api.getRetentionPreview(2, { keep_daily: 7, keep_weekly: 0, keep_monthly: NaN, keep_yearly: 2 }), 'GET', '/api/v1/jobs/2/retention-preview?keep_daily=7&keep_yearly=2'],
  ['deleteRestorePoint', () => api.deleteRestorePoint(2, 9), 'DELETE', '/api/v1/jobs/2/restore-points/9'],
  ['getRestorePointContents with file', () => api.getRestorePointContents(2, 9, 'my item', 'a b.idx'), 'GET', '/api/v1/jobs/2/restore-points/9/contents?item=my%20item&file=a%20b.idx'],
  ['runJob', () => api.runJob(2), 'POST', '/api/v1/jobs/2/run'],
  ['cancelJob', () => api.cancelJob(2), 'POST', '/api/v1/jobs/2/cancel'],
  ['listStorageFiles', () => api.listStorageFiles(4), 'GET', '/api/v1/storage/4/list'],
  ['listStorageFiles prefix', () => api.listStorageFiles(4, 'a/b'), 'GET', '/api/v1/storage/4/list?prefix=a%2Fb'],
  ['restoreJob', () => api.restoreJob(2, { rp: 1 }), 'POST', '/api/v1/jobs/2/restore', { rp: 1 }],
  ['preflightRestore', () => api.preflightRestore(2, 9, { x: 1 }), 'POST', '/api/v1/jobs/2/restore-points/9/preflight', { x: 1 }],
  ['getNextRuns', () => api.getNextRuns(), 'GET', '/api/v1/jobs/next-runs'],
  ['getNextRun', () => api.getNextRun(2), 'GET', '/api/v1/jobs/2/next-run'],
  ['getRunnerStatus', () => api.getRunnerStatus(), 'GET', '/api/v1/runner/status'],
  ['listMounts all', () => api.listMounts(false), 'GET', '/api/v1/mounts?active=false'],
  ['browse root', () => api.browse(), 'GET', '/api/v1/browse'],
  ['browse path+zfs', () => api.browse('/mnt', { includeZfs: true }), 'GET', '/api/v1/browse?path=%2Fmnt&include_zfs=true'],
  ['browseFiles root', () => api.browseFiles(), 'GET', '/api/v1/browse?files=true'],
  ['browseFiles path+zfs', () => api.browseFiles('/mnt', { includeZfs: true }), 'GET', '/api/v1/browse?files=true&path=%2Fmnt&include_zfs=true'],
  ['pathExists', () => api.pathExists('/mnt/a b'), 'GET', '/api/v1/path-exists?path=%2Fmnt%2Fa%20b'],
  ['listContainers', () => api.listContainers(), 'GET', '/api/v1/containers'],
  ['listVMs', () => api.listVMs(), 'GET', '/api/v1/vms'],
  ['listFolders', () => api.listFolders(), 'GET', '/api/v1/folders'],
  ['listPlugins', () => api.listPlugins(), 'GET', '/api/v1/plugins'],
  ['listZFSDatasets', () => api.listZFSDatasets(), 'GET', '/api/v1/zfs'],
  ['getSettings', () => api.getSettings(), 'GET', '/api/v1/settings'],
  ['updateSettings', () => api.updateSettings({ a: '1' }), 'PUT', '/api/v1/settings', { a: '1' }],
  ['getEncryptionStatus', () => api.getEncryptionStatus(), 'GET', '/api/v1/settings/encryption'],
  ['setEncryption', () => api.setEncryption('pw'), 'POST', '/api/v1/settings/encryption', { passphrase: 'pw' }],
  ['verifyEncryption', () => api.verifyEncryption('pw'), 'POST', '/api/v1/settings/encryption/verify', { passphrase: 'pw' }],
  ['getEncryptionPassphrase', () => api.getEncryptionPassphrase(), 'GET', '/api/v1/settings/encryption/passphrase'],
  ['getAPIKeyStatus', () => api.getAPIKeyStatus(), 'GET', '/api/v1/settings/api-key'],
  ['generateAPIKey', () => api.generateAPIKey(), 'POST', '/api/v1/settings/api-key/generate'],
  ['revealAPIKey', () => api.revealAPIKey(), 'GET', '/api/v1/settings/api-key/key'],
  ['rotateAPIKey', () => api.rotateAPIKey(), 'POST', '/api/v1/settings/api-key/rotate'],
  ['revokeAPIKey', () => api.revokeAPIKey(), 'DELETE', '/api/v1/settings/api-key'],
  ['getStagingInfo', () => api.getStagingInfo(), 'GET', '/api/v1/settings/staging'],
  ['getDatabaseInfo', () => api.getDatabaseInfo(), 'GET', '/api/v1/settings/database'],
  ['setSnapshotPath', () => api.setSnapshotPath('/snap'), 'PUT', '/api/v1/settings/database', { snapshot_path: '/snap' }],
  ['setStagingOverride', () => api.setStagingOverride('/tmp'), 'PUT', '/api/v1/settings/staging', { override: '/tmp' }],
  ['getAppdataPath', () => api.getAppdataPath(), 'GET', '/api/v1/settings/appdata'],
  ['setAppdataPath', () => api.setAppdataPath('/ad'), 'PUT', '/api/v1/settings/appdata', { path: '/ad' }],
  ['testDiscordWebhook', () => api.testDiscordWebhook('https://d'), 'POST', '/api/v1/settings/discord/test', { webhook_url: 'https://d' }],
  ['getActivity default', () => api.getActivity(), 'GET', '/api/v1/activity?limit=100'],
  ['getActivity filtered', () => api.getActivity(10, 'a b', 55), 'GET', '/api/v1/activity?limit=10&category=a%20b&before_id=55'],
  ['purgeActivity', () => api.purgeActivity(), 'DELETE', '/api/v1/activity'],
  ['getRunLogs default', () => api.getRunLogs(3), 'GET', '/api/v1/runs/3/logs?after=0&limit=500'],
  ['getRunLogs tail', () => api.getRunLogs(3, { after: 5, limit: 10, tail: true }), 'GET', '/api/v1/runs/3/logs?after=5&limit=10&tail=true'],
  ['purgeHistory', () => api.purgeHistory(), 'DELETE', '/api/v1/history'],
  ['getRecoveryPlan', () => api.getRecoveryPlan(), 'GET', '/api/v1/recovery/plan'],
  ['pathAudit', () => api.pathAudit(), 'GET', '/api/v1/recovery/path-audit'],
  ['pathRemap', () => api.pathRemap([{ id: 1 }]), 'POST', '/api/v1/recovery/path-remap', { updates: [{ id: 1 }] }],
  ['listAnomalies none', () => api.listAnomalies(), 'GET', '/api/v1/anomalies'],
  ['listAnomalies all filters', () => api.listAnomalies({ state: 'open', severity: 'high', scope_kind: 'job', scope_id: 3, since: '2026-01-01', limit: 5, cursor: 'c' }), 'GET', '/api/v1/anomalies?state=open&severity=high&scope_kind=job&scope_id=3&since=2026-01-01&limit=5&cursor=c'],
  ['getAnomaly', () => api.getAnomaly(8), 'GET', '/api/v1/anomalies/8'],
  ['ackAnomaly', () => api.ackAnomaly(8, 'ack'), 'POST', '/api/v1/anomalies/8/ack', { action: 'ack', reason: '', by: '' }],
  ['bulkAckAnomalies', () => api.bulkAckAnomalies([1, 2], 'resolve', 'r', 'me'), 'POST', '/api/v1/anomalies/ack-bulk', { ids: [1, 2], action: 'resolve', reason: 'r', by: 'me' }],
  ['getJobBaseline', () => api.getJobBaseline(2), 'GET', '/api/v1/jobs/2/baseline'],
  ['getCapacityTrajectory', () => api.getCapacityTrajectory(4), 'GET', '/api/v1/destinations/4/capacity-trajectory'],
  ['listReplicationSources', () => api.listReplicationSources(), 'GET', '/api/v1/replication'],
  ['getReplicationSource', () => api.getReplicationSource(6), 'GET', '/api/v1/replication/6'],
  ['createReplicationSource', () => api.createReplicationSource({ url: 'u' }), 'POST', '/api/v1/replication', { url: 'u' }],
  ['updateReplicationSource', () => api.updateReplicationSource(6, { url: 'v' }), 'PUT', '/api/v1/replication/6', { url: 'v' }],
  ['deleteReplicationSource', () => api.deleteReplicationSource(6), 'DELETE', '/api/v1/replication/6'],
  ['testReplicationSource', () => api.testReplicationSource(6), 'POST', '/api/v1/replication/6/test'],
  ['testReplicationURL', () => api.testReplicationURL('http://x'), 'POST', '/api/v1/replication/test-url', { url: 'http://x', api_key: '' }],
  ['syncReplicationSource', () => api.syncReplicationSource(6), 'POST', '/api/v1/replication/6/sync'],
  ['listReplicatedJobs', () => api.listReplicatedJobs(6), 'GET', '/api/v1/replication/6/jobs'],
]

describe('api routes', () => {
  it.each(routes)('%s', async (_name, invoke, method, url, body) => {
    await invoke()
    const call = lastCall()
    expect(call.url).toBe(url)
    expect(call.method).toBe(method)
    expect(call.body).toEqual(body)
  })
})

describe('request handling', () => {
  it('returns parsed JSON for a 2xx response', async () => {
    fetchMock.mockResolvedValueOnce(respond({ status: 'ok' }))
    await expect(api.health()).resolves.toEqual({ status: 'ok' })
  })

  it('returns null for 204 No Content', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(api.closeBreaker(1)).resolves.toBeNull()
  })

  it('returns null for an empty or non-JSON 2xx body', async () => {
    fetchMock.mockResolvedValueOnce(new Response('', { status: 200 }))
    await expect(api.health()).resolves.toBeNull()
    fetchMock.mockResolvedValueOnce(new Response('<html>', { status: 200 }))
    await expect(api.health()).resolves.toBeNull()
  })

  it('surfaces a clean HTTP status for a non-JSON error body', async () => {
    fetchMock.mockResolvedValueOnce(new Response('Bad Gateway', { status: 502 }))
    await expect(api.health()).rejects.toMatchObject({ message: 'HTTP 502', status: 502 })
  })

  it('rethrows non-abort network errors unchanged', async () => {
    const err = new TypeError('Failed to fetch')
    fetchMock.mockRejectedValueOnce(err)
    await expect(api.health()).rejects.toBe(err)
  })
})

describe('getChangelog', () => {
  it('returns the releases array', async () => {
    fetchMock.mockResolvedValueOnce(respond({ releases: [{ v: '1' }] }))
    await expect(api.getChangelog()).resolves.toEqual([{ v: '1' }])
    expect(lastCall().url).toBe('/api/v1/release/changelog')
  })

  it('falls back to an empty list when releases are missing', async () => {
    fetchMock.mockResolvedValueOnce(respond({}))
    await expect(api.getChangelog()).resolves.toEqual([])
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(api.getChangelog()).resolves.toEqual([])
  })
})

describe('replica mode flag', () => {
  it('round-trips through setReplicaMode / isReplicaMode', () => {
    expect(isReplicaMode()).toBe(false)
    setReplicaMode(true)
    expect(isReplicaMode()).toBe(true)
  })
})

describe('downloadDiagnostics', () => {
  it('returns the response blob on success', async () => {
    fetchMock.mockResolvedValueOnce(new Response('zipdata', { status: 200 }))
    const blob = await api.downloadDiagnostics()
    expect(await blob.text()).toBe('zipdata')
    expect(lastCall().url).toBe('/api/v1/settings/diagnostics')
  })

  it('throws the server error message', async () => {
    fetchMock.mockResolvedValueOnce(respond({ error: 'no disk' }, 500))
    await expect(api.downloadDiagnostics()).rejects.toThrow('no disk')
  })

  it('falls back to the HTTP status for a non-JSON error', async () => {
    fetchMock.mockResolvedValueOnce(new Response('oops', { status: 503 }))
    await expect(api.downloadDiagnostics()).rejects.toThrow('HTTP 503')
  })
})

describe('downloadStorageFile', () => {
  it('falls back to the HTTP status when the error body is empty', async () => {
    fetchMock.mockResolvedValueOnce(new Response('', { status: 404 }))
    await expect(api.downloadStorageFile(1, 'a')).rejects.toThrow('HTTP 404')
  })
})
