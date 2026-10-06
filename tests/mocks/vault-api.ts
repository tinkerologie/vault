import type { Page, Route, Request } from '@playwright/test';

export interface MockJobItem {
  id: number;
  job_id: number;
  item_type: string;
  type?: string;
  item_name: string;
  name?: string;
  settings?: string;
  sort_order?: number;
  missing_since?: string;
}

export interface MockJob {
  id: number;
  name: string;
  description?: string;
  target_type?: string;
  storage_target_id?: number;
  storage_dest_id?: number;
  schedule?: string;
  status?: string;
  enabled?: boolean;
  item_count?: number;
  retention_count?: number;
  retention_days?: number;
  keep_latest?: number;
  keep_daily?: number;
  keep_weekly?: number;
  keep_monthly?: number;
  keep_yearly?: number;
  verify_schedule?: string;
  full_backup_schedule?: string;
  verify_mode?: string;
  compression?: string;
  compression_level?: string;
  container_mode?: string;
  vm_mode?: string;
  pre_script?: string;
  post_script?: string;
  notify_on?: string;
  verify_backup?: boolean;
  defer_remote_upload?: boolean;
  adaptive_enabled?: boolean;
  auto_include_containers?: boolean;
  encryption?: string;
  retry_max_override?: string | number | null;
  retry_delays_override?: string | null;
  anomaly_sensitivity?: string;
  max_parallel_uploads?: number;
  backup_type_chain?: string;
  created_at?: string;
  items: MockJobItem[];
  baseline?: { job_id: number; sample_count: number };
}

export interface MockStorageDestination {
  id: number;
  name: string;
  type: string;
  path?: string;
  config: string;
  free_space_bytes: number;
  total_space_bytes: number;
  is_connected: boolean;
  dedup_enabled?: boolean;
  backup_database_enabled?: boolean;
  stage_beside_destination?: boolean;
  capacity?: { free_bytes: number; total_bytes: number };
}

/**
 * Factory creating initial test backup jobs for isolated test sessions.
 */
export const createInitialMockJobs = (): MockJob[] => [
  {
    id: 1,
    name: 'Docker & Appdata',
    description: 'Production container backup',
    target_type: 'container',
    storage_target_id: 1,
    storage_dest_id: 1,
    schedule: '0 2 * * *',
    status: 'idle',
    enabled: true,
    item_count: 2,
    retention_count: 5,
    retention_days: 30,
    keep_daily: 7,
    keep_weekly: 4,
    keep_monthly: 12,
    compression: 'zstd',
    container_mode: 'one_by_one',
    vm_mode: 'snapshot',
    notify_on: 'failure',
    verify_backup: true,
    encryption: 'none',
    backup_type_chain: 'full',
    created_at: '2026-09-01T00:00:00Z',
    items: [
      { id: 101, job_id: 1, item_type: 'container', type: 'container', item_name: 'plex', name: 'plex', settings: '{}' },
      { id: 102, job_id: 1, item_type: 'container', type: 'container', item_name: 'nextcloud', name: 'nextcloud', settings: '{}' },
    ],
    baseline: { job_id: 1, sample_count: 5 },
  },
  {
    id: 2,
    name: 'VM Backups',
    description: 'Virtual machines backup',
    target_type: 'vm',
    storage_target_id: 1,
    storage_dest_id: 1,
    schedule: '0 3 * * 0',
    status: 'idle',
    enabled: true,
    item_count: 1,
    retention_count: 3,
    retention_days: 14,
    compression: 'zstd',
    container_mode: 'one_by_one',
    vm_mode: 'snapshot',
    notify_on: 'failure',
    verify_backup: true,
    encryption: 'none',
    backup_type_chain: 'full',
    created_at: '2026-09-05T00:00:00Z',
    items: [
      { id: 201, job_id: 2, item_type: 'vm', type: 'vm', item_name: 'home-assistant', name: 'home-assistant', settings: '{}' },
    ],
    baseline: { job_id: 2, sample_count: 3 },
  },
];

export const mockJob1Points = [
  {
    id: 1001,
    job_id: 1,
    job_name: 'Docker & Appdata',
    backup_type: 'full',
    created_at: '2026-10-01T02:00:00Z',
    size_bytes: 10737418240,
    chain_depth: 1,
    metadata: JSON.stringify({
      item_sizes: { plex: 5368709120, nextcloud: 5368709120 },
    }),
  },
];

export const mockJob2Points = [
  {
    id: 2001,
    job_id: 2,
    job_name: 'VM Backups',
    backup_type: 'full',
    created_at: '2026-10-01T03:00:00Z',
    size_bytes: 21474836480,
    chain_depth: 1,
    metadata: JSON.stringify({
      item_sizes: { 'home-assistant': 21474836480 },
    }),
  },
];

/**
 * Factory creating initial test storage targets for isolated test sessions.
 */
export const createInitialMockStorage = (): MockStorageDestination[] => [
  {
    id: 1,
    name: 'Local Array Backup',
    type: 'local',
    path: '/mnt/user/backups',
    config: JSON.stringify({ path: '/mnt/user/backups', bandwidth_limit_mbps: 0 }),
    free_space_bytes: 1000000000000,
    total_space_bytes: 4000000000000,
    is_connected: true,
    dedup_enabled: true,
    backup_database_enabled: true,
    stage_beside_destination: false,
    capacity: { free_bytes: 1000000000000, total_bytes: 4000000000000 },
  },
];

/**
 * Intercepts Vault API endpoints to provide deterministic, hermetic responses for Playwright tests.
 */
export async function setupVaultMockApi(page: Page) {
  // Local state per page session
  let jobsList = createInitialMockJobs();
  let storageList = createInitialMockStorage();
  let nextJobId = 3;
  let nextStorageId = 2;

  let activityLogs = [
    { id: 1, timestamp: '2026-10-06T12:00:00Z', level: 'info', category: 'backup', message: 'Job "Docker & Appdata" completed successfully', details: 'duration=15m bytes=10GB' },
    { id: 2, timestamp: '2026-10-06T12:05:00Z', level: 'warn', category: 'health', message: 'Storage capacity above 75%', details: 'free=1TB' },
    { id: 3, timestamp: '2026-10-06T12:10:00Z', level: 'error', category: 'system', message: 'Failed to reach remote endpoint', details: 'host=remote.example.com err=connection refused' },
  ];

  let historyRuns = [
    {
      id: 901,
      job_id: 1,
      job_name: 'Docker & Appdata',
      status: 'completed',
      backup_type: 'full',
      started_at: '2026-10-01T02:00:00Z',
      completed_at: '2026-10-01T02:15:00Z',
      bytes_written: 10737418240,
      duration_seconds: 900,
      dedup_ratio: '1.8x',
      error_message: null,
      log: '[INFO] Backup completed successfully\n[INFO] 120 files scanned\n[INFO] Deduplication savings 53%',
    },
    {
      id: 902,
      job_id: 2,
      job_name: 'VM Backups',
      status: 'failed',
      backup_type: 'full',
      started_at: '2026-10-02T03:00:00Z',
      completed_at: '2026-10-02T03:05:00Z',
      bytes_written: 0,
      duration_seconds: 300,
      dedup_ratio: '1.0x',
      error_message: 'libvirt domain suspended during snapshot',
    },
  ];

  let anomaliesList = [
    {
      id: 1,
      scope_kind: 'job',
      scope_id: 1,
      title: 'Unusual Backup Size Spike',
      description: 'Docker & Appdata backup size increased significantly above baseline',
      severity: 'warning',
      state: 'open',
      metric_name: 'size_bytes',
      expected_value: 5368709120,
      observed_value: 10737418240,
      detected_at: '2026-10-05T02:00:00Z',
    },
  ];

  let settingsState: Record<string, string> = {
    retention_days: '30',
    theme: 'dark',
    style: 'default',
    backup_targets: '1',
    notification_discord: 'false',
    log_level: 'info',
    max_parallel_uploads: '3',
    upload_throttle_mbps: '0',
    anomalies_enabled: 'true',
    replication_enabled: 'true',
  };

  try {
    await page.routeWebSocket('**/api/v1/ws', () => {
      // Keep WebSocket mock open without throwing
    });
  } catch {
    // Graceful fallback if routeWebSocket is not supported
  }

  await page.route('**/api/v1/**', async (route: Route, request: Request) => {
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();

    // Health
    if (path.endsWith('/health/summary')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          unraid_connected: true,
          server_version: '2026.09.01',
          degraded: false,
          uptime: 3600,
        }),
      });
    }

    if (path.endsWith('/health')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'ok', version: '2026.09.01' }),
      });
    }

    // Runner
    if (path.endsWith('/runner/status')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ active: false, queue: [] }),
      });
    }

    // Restore Preflight
    if (path.includes('/preflight')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: true,
          checks: [
            { id: 'storage', label: 'Storage reachable', status: 'ok', detail: 'Destination storage is online and accessible' },
            { id: 'archive', label: 'Archive present', status: 'ok', detail: 'Backup archive verified' },
          ],
        }),
      });
    }

    // Specific restore points
    if (path.includes('/jobs/1/restore-points')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockJob1Points),
      });
    }

    if (path.includes('/jobs/2/restore-points')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockJob2Points),
      });
    }

    if (path.includes('/restore-points')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }

    // Restore Execution
    if (path.endsWith('/restore')) {
      let restorePointId = 1001;
      try {
        const body = request.postDataJSON();
        if (body?.restore_point_id) restorePointId = body.restore_point_id;
      } catch { /* ignore */ }

      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'restore started', restore_point_id: restorePointId, items: 1 }),
      });
    }

    // Discovery endpoints
    if (path.endsWith('/containers')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          available: true,
          items: [
            { name: 'plex', image: 'linuxserver/plex:latest', running: true, state: 'running', status: 'running' },
            { name: 'nextcloud', image: 'nextcloud:apache', running: true, state: 'running', status: 'running' },
            { name: 'postgres', image: 'postgres:15', running: true, state: 'running', status: 'running', settings: { database_dump: true, database_kind: 'postgres' } },
          ],
        }),
      });
    }

    if (path.endsWith('/vms')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          available: true,
          items: [
            { name: 'home-assistant', running: true, state: 'running', settings: { disk_format: 'qcow2', supports_incremental: true } },
            { name: 'ubuntu-vm', running: true, state: 'running', settings: { disk_format: 'qcow2', supports_incremental: true } },
          ],
        }),
      });
    }

    if (path.endsWith('/folders')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          available: true,
          items: [
            { name: 'Flash Drive', path: '/boot', settings: { preset: 'flash', path: '/boot' } },
            { name: 'Appdata', path: '/mnt/user/appdata', settings: { path: '/mnt/user/appdata', recommended_exclusions: ['.Recycle.Bin'] } },
          ],
        }),
      });
    }

    if (path.endsWith('/plugins')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          available: true,
          items: [
            { name: 'community.applications', path: '/boot/config/plugins/community.applications.plg' },
          ],
        }),
      });
    }

    if (path.endsWith('/zfs')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          available: true,
          items: [
            { name: 'tank/appdata', mountpoint: '/mnt/tank/appdata' },
          ],
        }),
      });
    }

    if (path.endsWith('/path-exists')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ exists: true, is_dir: true }),
      });
    }

    if (path.endsWith('/browse')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          current_path: '/mnt/user',
          entries: [
            { name: 'appdata', path: '/mnt/user/appdata', is_dir: true, size: 4096 },
            { name: 'backups', path: '/mnt/user/backups', is_dir: true, size: 4096 },
          ],
        }),
      });
    }

    // Jobs CRUD & Actions
    if (path.endsWith('/jobs/next-runs')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          '1': '2026-10-07T02:00:00Z',
          '2': '2026-10-11T03:00:00Z',
        }),
      });
    }

    // Job Next Run
    const nextRunMatch = path.match(/\/jobs\/(\d+)\/next-run$/);
    if (nextRunMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ next_run: '2026-10-07T02:00:00Z' }),
      });
    }

    // Job Retention Preview
    if (path.includes('/retention-preview')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          total_points: 10,
          keep_points: 5,
          prune_points: 5,
        }),
      });
    }

    // Job Stale Items
    if (path.includes('/stale-items/remove')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 0 }),
      });
    }
    if (path.includes('/stale-items')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [] }),
      });
    }

    // Job Run Now
    const runMatch = path.match(/\/jobs\/(\d+)\/run$/);
    if (runMatch && method === 'POST') {
      const jobId = Number.parseInt(runMatch[1], 10);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: `Job ${jobId} queued`, run_id: 888 }),
      });
    }

    // Job Cancel
    const cancelMatch = path.match(/\/jobs\/(\d+)\/cancel$/);
    if (cancelMatch && method === 'POST') {
      const jobId = Number.parseInt(cancelMatch[1], 10);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: `Job ${jobId} cancelled` }),
      });
    }

    // Specific Job by ID
    const singleJobMatch = path.match(/\/jobs\/(\d+)$/);
    if (singleJobMatch) {
      const jobId = Number.parseInt(singleJobMatch[1], 10);
      const existingJob = jobsList.find(j => j.id === jobId);

      if (method === 'GET') {
        if (!existingJob) {
          return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'Job not found' }) });
        }
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ job: existingJob, items: existingJob.items || [] }),
        });
      }

      if (method === 'PUT') {
        const payload = request.postDataJSON();
        if (existingJob) {
          Object.assign(existingJob, payload);
          if (payload.items) existingJob.items = payload.items;
        }
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ id: jobId, message: 'Job updated successfully' }),
        });
      }

      if (method === 'DELETE') {
        jobsList = jobsList.filter(j => j.id !== jobId);
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ message: 'Job deleted successfully' }),
        });
      }
    }

    // Jobs Collection (List / Create)
    if (path === '/api/v1/jobs') {
      if (method === 'POST') {
        const payload = request.postDataJSON();
        const createdJob: MockJob = {
          id: nextJobId++,
          name: payload.name || 'New Backup Job',
          description: payload.description || '',
          storage_dest_id: payload.storage_dest_id || 1,
          schedule: payload.schedule || '0 2 * * *',
          status: 'idle',
          enabled: payload.enabled ?? true,
          item_count: (payload.items || []).length,
          retention_count: payload.retention_count || 5,
          retention_days: payload.retention_days || 30,
          compression: payload.compression || 'zstd',
          encryption: payload.encryption || 'none',
          backup_type_chain: payload.backup_type_chain || 'full',
          items: payload.items || [],
          baseline: { job_id: nextJobId - 1, sample_count: 0 },
        };
        jobsList.push(createdJob);
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ id: createdJob.id, message: 'Job created successfully' }),
        });
      }

      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(jobsList),
      });
    }

    // Storage CRUD & Operations
    if (path.endsWith('/storage/test') && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ success: true }),
      });
    }

    // Storage specific actions
    const storageTestMatch = path.match(/\/storage\/(\d+)\/test$/);
    if (storageTestMatch && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ success: true }),
      });
    }

    const storageCapMatch = path.match(/\/storage\/(\d+)\/capacity-check$/);
    if (storageCapMatch && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          capacity: { free_bytes: 1000000000000, total_bytes: 4000000000000 },
        }),
      });
    }

    const storageBreakerMatch = path.match(/\/storage\/(\d+)\/breaker\/close$/);
    if (storageBreakerMatch && method === 'POST') {
      return route.fulfill({
        status: 204,
        body: '',
      });
    }

    const storageJobsMatch = path.match(/\/storage\/(\d+)\/jobs$/);
    if (storageJobsMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ job_count: 1, jobs: ['Docker & Appdata'] }),
      });
    }

    const storageDedupMatch = path.match(/\/storage\/(\d+)\/dedup-stats$/);
    if (storageDedupMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          chunk_count: 1500,
          total_bytes: 15000000000,
          dedup_bytes: 8000000000,
          savings_pct: 53.3,
        }),
      });
    }

    const storageListFilesMatch = path.match(/\/storage\/(\d+)\/list$/);
    if (storageListFilesMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { name: 'vault-manifest.json', path: 'vault-manifest.json', size: 1024, is_dir: false, mod_time: '2026-10-01T02:00:00Z' },
          { name: 'backups', path: 'backups', size: 4096, is_dir: true, mod_time: '2026-10-01T02:00:00Z' },
          { name: 'backup-snapshot.tar.gz', path: 'backups/backup-snapshot.tar.gz', size: 1073741824, is_dir: false, mod_time: '2026-10-01T02:15:00Z' },
        ]),
      });
    }

    const storageDownloadMatch = path.match(/\/storage\/(\d+)\/files$/);
    if (storageDownloadMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/octet-stream',
        body: 'MOCK_VAULT_BACKUP_SNAPSHOT_CONTENT',
      });
    }

    const storageDBBackupsMatch = path.match(/\/storage\/(\d+)\/db-backups$/);
    if (storageDBBackupsMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            path: '_vault/vault.db.20261005.gz',
            name: 'vault.db.20261005.gz',
            size: 1048576,
            encrypted: false,
            timestamp: '2026-10-05T00:00:00Z',
            is_latest: true,
          },
        ]),
      });
    }

    const storageRestoreDBMatch = path.match(/\/storage\/(\d+)\/restore-db$/);
    if (storageRestoreDBMatch && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Database verified / restored successfully', success: true }),
      });
    }

    // Capacity Trajectory
    const trajectoryMatch = path.match(/\/destinations\/(\d+)\/capacity-trajectory$/);
    if (trajectoryMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          samples: [
            { date: '2026-09-01', free_bytes: 1200000000000, total_bytes: 4000000000000 },
            { date: '2026-10-01', free_bytes: 1000000000000, total_bytes: 4000000000000 },
          ],
        }),
      });
    }

    // Single Storage item by ID
    const singleStorageMatch = path.match(/\/storage\/(\d+)$/);
    if (singleStorageMatch) {
      const storageId = Number.parseInt(singleStorageMatch[1], 10);
      const existingStorage = storageList.find(s => s.id === storageId);

      if (method === 'GET') {
        if (!existingStorage) {
          return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'Storage not found' }) });
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(existingStorage) });
      }

      if (method === 'PUT') {
        const payload = request.postDataJSON();
        if (existingStorage) Object.assign(existingStorage, payload);
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: storageId, message: 'Storage updated' }) });
      }

      if (method === 'DELETE') {
        storageList = storageList.filter(s => s.id !== storageId);
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ message: 'Storage deleted' }) });
      }
    }

    // Storage List / Create
    if (path === '/api/v1/storage') {
      if (method === 'POST') {
        const payload = request.postDataJSON();
        const createdStorage: MockStorageDestination = {
          id: nextStorageId++,
          name: payload.name || 'New Storage Target',
          type: payload.type || 'local',
          config: payload.config || '{}',
          free_space_bytes: 1000000000000,
          total_space_bytes: 4000000000000,
          is_connected: true,
          dedup_enabled: !!payload.dedup_enabled,
          backup_database_enabled: !!payload.backup_database_enabled,
          stage_beside_destination: !!payload.stage_beside_destination,
          capacity: { free_bytes: 1000000000000, total_bytes: 4000000000000 },
        };
        storageList.push(createdStorage);
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(createdStorage),
        });
      }

      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(storageList),
      });
    }

    // Activity Log
    if (path.includes('/activity')) {
      if (method === 'DELETE') {
        activityLogs = [];
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ message: 'activity purged' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(activityLogs),
      });
    }

    // History & Trends
    if (path.includes('/history/trend')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          points: [
            { date: '2026-10-01', value: 10737418240 },
            { date: '2026-10-02', value: 0 },
          ],
        }),
      });
    }

    if (path.includes('/history')) {
      if (method === 'DELETE') {
        historyRuns = [];
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ message: 'history purged' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(historyRuns),
      });
    }

    // Run specific logs
    const runLogsMatch = path.match(/\/runs\/(\d+)\/logs/);
    if (runLogsMatch) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          lines: [
            '[INFO] Job started at 2026-10-01 02:00:00',
            '[INFO] Inspecting container plex...',
            '[INFO] Snapshotting volumes',
            '[INFO] Uploading tar archive to destination',
            '[INFO] Backup complete in 900s',
          ],
          total: 5,
        }),
      });
    }

    // Anomalies
    if (path.includes('/anomalies/ack-bulk') && method === 'POST') {
      anomaliesList = [];
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 1 }),
      });
    }

    const anomalyAckMatch = path.match(/\/anomalies\/(\d+)\/ack/);
    if (anomalyAckMatch && method === 'POST') {
      const anomalyId = Number.parseInt(anomalyAckMatch[1], 10);
      anomaliesList = anomaliesList.filter(a => a.id !== anomalyId);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Anomaly acknowledged' }),
      });
    }

    if (path.includes('/anomalies')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ anomalies: anomaliesList }),
      });
    }

    // Settings
    if (path.endsWith('/settings/encryption/verify') && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ valid: true }),
      });
    }

    if (path.endsWith('/settings/encryption') && method === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ enabled: false }),
      });
    }

    if (path.endsWith('/settings/api-key/generate') && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ key: 'vlt_sec_key_mock_99999' }),
      });
    }

    if (path.endsWith('/settings/api-key/key') && method === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ key: 'vlt_sec_key_mock_99999' }),
      });
    }

    if (path.endsWith('/settings/api-key') && method === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ has_key: true, created_at: '2026-09-01T00:00:00Z' }),
      });
    }

    if (path.endsWith('/settings/api-key') && method === 'DELETE') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'API key revoked' }),
      });
    }

    if (path.endsWith('/settings/staging')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          path: '/mnt/cache/staging',
          is_override: false,
          free_bytes: 500000000000,
          total_bytes: 1000000000000,
        }),
      });
    }

    if (path.endsWith('/settings/database')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          path: '/boot/config/plugins/vault/vault.db',
          size_bytes: 1048576,
          last_snapshot: '2026-10-05T00:00:00Z',
          snapshot_path: '/boot/config/plugins/vault/backups',
        }),
      });
    }

    if (path.endsWith('/settings/appdata')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ path: '/mnt/user/appdata' }),
      });
    }

    if (path.endsWith('/settings/discord/test') && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ success: true }),
      });
    }

    if (path.endsWith('/settings/diagnostics')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/zip',
        body: 'PK_MOCK_DIAGNOSTICS_ZIP_CONTENT',
      });
    }

    if (path === '/api/v1/settings') {
      if (method === 'PUT') {
        const payload = request.postDataJSON();
        settingsState = { ...settingsState, ...payload };
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ message: 'Settings saved' }),
        });
      }

      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(settingsState),
      });
    }

    // Recovery
    if (path.endsWith('/recovery/plan')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ can_recover: true, steps: [] }),
      });
    }

    if (path.endsWith('/recovery/path-audit')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          entries: [
            { kind: 'folder', id: 1, name: 'Appdata', original_path: '/mnt/user/appdata', current_path: '/mnt/user/appdata', exists: true },
          ],
        }),
      });
    }

    if (path.endsWith('/recovery/path-remap') && method === 'POST') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ results: [{ kind: 'folder', id: 1, name: 'Appdata', applied: true }] }),
      });
    }

    // Release metadata
    if (path.includes('/release/changelog')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ releases: [] }),
      });
    }

    if (path.includes('/release/latest')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ version: '2026.09.01' }),
      });
    }

    // Mounts
    if (path.includes('/mounts')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }

    // Replication
    if (path.includes('/replication')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }

    return route.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ error: `Mock endpoint not found: ${path}` }),
    });
  });
}
