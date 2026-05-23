import { buildApiRequest } from './runtime-config.js'

/** @type {boolean} */
let _isReplica = false

/** Returns true if connected to a read-only replica instance. */
export function isReplicaMode() { return _isReplica }

/** Set replica mode (called once during app init). */
export function setReplicaMode(val) { _isReplica = val }

async function request(method, path, body = null) {
  const { url, options } = buildApiRequest(method, path, { body })
  const res = await fetch(url, options)
  if (res.status === 204) return null
  // Read as text first so we don't throw on empty / non-JSON bodies
  // (e.g. 502 from an upstream proxy). Errors should surface a clean
  // HTTP status message rather than a JSON parse error.
  const text = await res.text()
  let data = null
  if (text) {
    try { data = JSON.parse(text) } catch { /* non-JSON body */ }
  }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`)
  return data
}

export const api = {
  // Health
  health: () => request('GET', '/health'),
  getHealthSummary: () => request('GET', '/health/summary'),

  // Storage
  listStorage: () => request('GET', '/storage'),
  getStorage: (id) => request('GET', `/storage/${id}`),
  createStorage: (data) => request('POST', '/storage', data),
  updateStorage: (id, data) => request('PUT', `/storage/${id}`, data),
  deleteStorage: (id, { deleteFiles = false, force = false } = {}) => {
    const params = new URLSearchParams()
    if (deleteFiles) params.set('deleteFiles', 'true')
    if (force) params.set('force', 'true')
    const qs = params.toString()
    return request('DELETE', `/storage/${id}${qs ? '?' + qs : ''}`)
  },
  testStorage: (id) => request('POST', `/storage/${id}/test`),
  // Manually close the circuit breaker for a storage destination. The
  // breaker is otherwise managed automatically by the runner / pre-flight
  // check. Returns 204 on success and resets consecutive_failures to 0.
  closeBreaker: (id) => request('POST', `/storage/${id}/breaker/close`),
  scanStorage: (id, path = '') => request('POST', `/storage/${id}/scan${path ? '?path=' + encodeURIComponent(path) : ''}`),
  importBackups: (id, backups) => request('POST', `/storage/${id}/import`, { backups }),
  restoreDB: (id, storagePath) => request('POST', `/storage/${id}/restore-db`, { storage_path: storagePath }),
  getDependentJobs: (id) => request('GET', `/storage/${id}/jobs`),

  // Jobs
  listJobs: () => request('GET', '/jobs'),
  getJob: (id) => request('GET', `/jobs/${id}`),
  createJob: (data) => request('POST', '/jobs', data),
  updateJob: (id, data) => request('PUT', `/jobs/${id}`, data),
  deleteJob: (id, deleteFiles = false) => request('DELETE', `/jobs/${id}${deleteFiles ? '?deleteFiles=true' : ''}`),
  getJobHistory: (id, limit = 50) => request('GET', `/jobs/${id}/history?limit=${limit}`),
  // Manually trigger a storage destination health check; returns
  // {status: "ok"|"failed", error: string}.
  healthCheckStorage: (id) => request('POST', `/storage/${id}/health-check`),
  // Orphan GC: scan returns {orphans, total_bytes}; delete consumes a
  // paths array that must still be orphaned (server re-checks).
  scanStorageOrphans: (id) => request('POST', `/storage/${id}/scan-orphans`),
  deleteStorageOrphans: (id, paths) => request('POST', `/storage/${id}/delete-orphans`, { paths }),
  // Dedup: per-destination chunk-store stats (refreshed every 30s by the
  // Storage page) and async mark-and-sweep GC (returns 202 + gc_run_id;
  // dedup_gc_complete WS event carries the final result).
  dedupStats: (id) => request('GET', `/storage/${id}/dedup-stats`),
  runDedupGC: (id) => request('POST', `/storage/${id}/gc`),
  getRestorePoints: (id) => request('GET', `/jobs/${id}/restore-points`),
  // getRetentionPreview asks the server what a hypothetical GFS retention
  // policy would do to a job's current restore points. Used by the Jobs
  // wizard to show "would keep X of Y" as the user tunes the keep_* fields.
  getRetentionPreview: (id, policy) => {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(policy)) {
      if (Number.isFinite(v) && v > 0) qs.set(k, String(v))
    }
    return request('GET', `/jobs/${id}/retention-preview?${qs.toString()}`)
  },
  deleteRestorePoint: (jobId, rpId) => request('DELETE', `/jobs/${jobId}/restore-points/${rpId}`),
  // getRestorePointContents fetches the tar-index sidecar for one item at a
  // restore point, returning {version, archive, files:[{path,size,mode,modtime,is_dir}]}.
  // `file` is optional; omit to let the server pick the first index sidecar it finds
  // in the item's directory (right call for single-archive items like folders/plugins).
  getRestorePointContents: (jobId, rpId, item, file) =>
    request('GET', `/jobs/${jobId}/restore-points/${rpId}/contents?item=${encodeURIComponent(item)}${file ? `&file=${encodeURIComponent(file)}` : ''}`),
  runJob: (id) => request('POST', `/jobs/${id}/run`),
  restoreJob: (id, data) => request('POST', `/jobs/${id}/restore`, data),
  getNextRuns: () => request('GET', '/jobs/next-runs'),
  getNextRun: (id) => request('GET', `/jobs/${id}/next-run`),

  // Runner
  getRunnerStatus: () => request('GET', '/runner/status'),

  // Discovery
  browse: (path = '', { includeZfs = false } = {}) => {
    const params = new URLSearchParams()
    if (path) params.set('path', path)
    if (includeZfs) params.set('include_zfs', 'true')
    const qs = params.toString()
    return request('GET', `/browse${qs ? '?' + qs : ''}`)
  },
  browseFiles: (path = '', { includeZfs = false } = {}) => {
    const params = new URLSearchParams({ files: 'true' })
    if (path) params.set('path', path)
    if (includeZfs) params.set('include_zfs', 'true')
    return request('GET', `/browse?${params.toString()}`)
  },
  // Lightweight existence check used by the Items wizard to validate custom
  // folder items. Returns {exists, is_dir} and never errors for missing
  // paths.
  pathExists: (path) => request('GET', `/path-exists?path=${encodeURIComponent(path)}`),
  listContainers: () => request('GET', '/containers'),
  listVMs: () => request('GET', '/vms'),
  listFolders: () => request('GET', '/folders'),
  listPlugins: () => request('GET', '/plugins'),
  listZFSDatasets: () => request('GET', '/zfs'),

  // Settings
  getSettings: () => request('GET', '/settings'),
  updateSettings: (data) => request('PUT', '/settings', data),
  getEncryptionStatus: () => request('GET', '/settings/encryption'),
  setEncryption: (passphrase) => request('POST', '/settings/encryption', { passphrase }),
  verifyEncryption: (passphrase) => request('POST', '/settings/encryption/verify', { passphrase }),
  getEncryptionPassphrase: () => request('GET', '/settings/encryption/passphrase'),

  // API Key
  getAPIKeyStatus: () => request('GET', '/settings/api-key'),
  generateAPIKey: () => request('POST', '/settings/api-key/generate'),
  revealAPIKey: () => request('GET', '/settings/api-key/key'),
  rotateAPIKey: () => request('POST', '/settings/api-key/rotate'),
  revokeAPIKey: () => request('DELETE', '/settings/api-key'),

  // Staging
  getStagingInfo: () => request('GET', '/settings/staging'),
  getDatabaseInfo: () => request('GET', '/settings/database'),
  setSnapshotPath: (path) => request('PUT', '/settings/database', { snapshot_path: path }),
  setStagingOverride: (override) => request('PUT', '/settings/staging', { override }),

  // Discord
  testDiscordWebhook: (webhookUrl) => request('POST', '/settings/discord/test', { webhook_url: webhookUrl }),

  // Diagnostics
  downloadDiagnostics: async () => {
    const { url, options } = buildApiRequest('GET', '/settings/diagnostics', {})
    const res = await fetch(url, options)
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error(data.error || `HTTP ${res.status}`)
    }
    return res.blob()
  },

  // Activity Log
  getActivity: (limit = 100, category = '') =>
    request('GET', `/activity?limit=${limit}${category ? '&category=' + encodeURIComponent(category) : ''}`),
  purgeActivity: () => request('DELETE', '/activity'),

  // History
  purgeHistory: () => request('DELETE', '/history'),

  // Recovery
  getRecoveryPlan: () => request('GET', '/recovery/plan'),

  // Replication
  listReplicationSources: () => request('GET', '/replication'),
  getReplicationSource: (id) => request('GET', `/replication/${id}`),
  createReplicationSource: (data) => request('POST', '/replication', data),
  updateReplicationSource: (id, data) => request('PUT', `/replication/${id}`, data),
  deleteReplicationSource: (id) => request('DELETE', `/replication/${id}`),
  testReplicationSource: (id) => request('POST', `/replication/${id}/test`),
  testReplicationURL: (url, apiKey = '') => request('POST', '/replication/test-url', { url, api_key: apiKey }),
  syncReplicationSource: (id) => request('POST', `/replication/${id}/sync`),
  listReplicatedJobs: (id) => request('GET', `/replication/${id}/jobs`),
}
