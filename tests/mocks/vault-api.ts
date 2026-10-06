import type { Page } from '@playwright/test';

export const mockJobs = [
  {
    id: 1,
    name: 'Docker & Appdata',
    target_type: 'container',
    storage_target_id: 1,
    schedule: '0 2 * * *',
    status: 'idle',
    item_count: 2,
    items: [
      { id: 101, job_id: 1, item_type: 'container', type: 'container', item_name: 'plex', name: 'plex' },
      { id: 102, job_id: 1, item_type: 'container', type: 'container', item_name: 'nextcloud', name: 'nextcloud' },
    ],
  },
  {
    id: 2,
    name: 'VM Backups',
    target_type: 'vm',
    storage_target_id: 1,
    schedule: '0 3 * * 0',
    status: 'idle',
    item_count: 1,
    items: [
      { id: 201, job_id: 2, item_type: 'vm', type: 'vm', item_name: 'home-assistant', name: 'home-assistant' },
    ],
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
 * Intercepts Vault API endpoints to provide deterministic, hermetic responses for Playwright tests.
 */
export async function setupVaultMockApi(page: Page) {
  try {
    await page.routeWebSocket('**/api/v1/ws', () => {
      // Keep WebSocket mock open without throwing
    });
  } catch {
    // Graceful fallback if routeWebSocket is not supported
  }

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

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

    if (path.endsWith('/runner/status')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ active: false, queue: [] }),
      });
    }

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

    if (path.endsWith('/restore')) {
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'restore started', restore_point_id: 1001, items: 1 }),
      });
    }

    if (path.endsWith('/jobs') || path.includes('/jobs?')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockJobs),
      });
    }

    if (path.endsWith('/storage')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            id: 1,
            name: 'Local Array Backup',
            type: 'local',
            path: '/mnt/user/backups',
            free_space_bytes: 1000000000000,
            total_space_bytes: 4000000000000,
            is_connected: true,
          },
        ]),
      });
    }

    if (path.includes('/anomalies')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ anomalies: [] }),
      });
    }

    if (path.endsWith('/settings')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          retention_days: 30,
          theme: 'dark',
        }),
      });
    }

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

    if (path.includes('/mounts')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }

    if (path.includes('/discover/')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [], available: true }),
      });
    }

    if (path.includes('/browse')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ current_path: '/mnt/user', entries: [] }),
      });
    }

    if (path.includes('/history')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }

    if (path.includes('/logs')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ entries: [], total: 0 }),
      });
    }

    if (path.includes('/next-runs')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({}),
      });
    }

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
