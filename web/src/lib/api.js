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
  const data = await res.json()
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
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
  scanStorage: (id) => request('POST', `/storage/${id}/scan`),
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
  getRestorePoints: (id) => request('GET', `/jobs/${id}/restore-points`),
  runJob: (id) => request('POST', `/jobs/${id}/run`),
  restoreJob: (id, data) => request('POST', `/jobs/${id}/restore`, data),
  getNextRuns: () => request('GET', '/jobs/next-runs'),
  getNextRun: (id) => request('GET', `/jobs/${id}/next-run`),

  // Runner
  getRunnerStatus: () => request('GET', '/runner/status'),

  // Discovery
  browse: (path = '') => request('GET', `/browse${path ? '?path=' + encodeURIComponent(path) : ''}`),
  browseFiles: (path = '') => request('GET', `/browse?files=true${path ? '&path=' + encodeURIComponent(path) : ''}`),
  listContainers: () => request('GET', '/containers'),
  listVMs: () => request('GET', '/vms'),
  listFolders: () => request('GET', '/folders'),
  listPlugins: () => request('GET', '/plugins'),

  // Settings
  getSettings: () => request('GET', '/settings'),
  updateSettings: (data) => request('PUT', '/settings', data),
  getEncryptionStatus: () => request('GET', '/settings/encryption'),
  setEncryption: (passphrase) => request('POST', '/settings/encryption', { passphrase }),
  verifyEncryption: (passphrase) => request('POST', '/settings/encryption/verify', { passphrase }),
  getEncryptionPassphrase: () => request('GET', '/settings/encryption/passphrase'),

  // Staging
  getStagingInfo: () => request('GET', '/settings/staging'),
  getDatabaseInfo: () => request('GET', '/settings/database'),
  setSnapshotPath: (path) => request('PUT', '/settings/database', { snapshot_path: path }),
  setStagingOverride: (override) => request('PUT', '/settings/staging', { override }),

  // Discord
  testDiscordWebhook: (webhookUrl) => request('POST', '/settings/discord/test', { webhook_url: webhookUrl }),

  // Activity Log
  getActivity: (limit = 100, category = '') =>
    request('GET', `/activity?limit=${limit}${category ? '&category=' + encodeURIComponent(category) : ''}`),

  // Recovery
  getRecoveryPlan: () => request('GET', '/recovery/plan'),

  // Replication
  listReplicationSources: () => request('GET', '/replication'),
  getReplicationSource: (id) => request('GET', `/replication/${id}`),
  createReplicationSource: (data) => request('POST', '/replication', data),
  updateReplicationSource: (id, data) => request('PUT', `/replication/${id}`, data),
  deleteReplicationSource: (id) => request('DELETE', `/replication/${id}`),
  testReplicationSource: (id) => request('POST', `/replication/${id}/test`),
  testReplicationURL: (url) => request('POST', '/replication/test-url', { url }),
  syncReplicationSource: (id) => request('POST', `/replication/${id}/sync`),
  listReplicatedJobs: (id) => request('GET', `/replication/${id}/jobs`),
}
