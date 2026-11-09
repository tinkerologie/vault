import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import { createInitialMockJobs, createInitialMockStorage, setupVaultMockApi, type VaultMockOptions } from './mocks/vault-api';
import { ALL_ITEM_KEYS, BANNER_STATES, HEALTHY, run, summary } from './helpers/backup-status';
import { seedTheme } from './helpers/contrast';

// The Dashboard's single backup verdict (#527).
const LAYOUT_KEY = 'vault:dash:layout:v1';
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

const banner = (page: Page) => page.getByRole('region', { name: 'Backup status' });
const title = (page: Page) => banner(page).getByRole('heading', { level: 2 });
const announcer = (page: Page) => banner(page).locator('[data-status-announcer]');
const h1 = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true });
const healthTile = (page: Page) => page.getByRole('link', { name: /^Health score/ });

async function open(page: Page, options: VaultMockOptions = {}) {
  await setupVaultMockApi(page, options);
  await page.goto('/#/');
}

/** 19 discovered items: the audit's server, where 3 are in no job at all. */
const AUDIT_CONTAINERS = Array.from({ length: 19 }, (_, i) => ({ name: `app-${i}`, image: 'x', running: true, state: 'running', status: 'running' }));
const AUDIT_KEYS = AUDIT_CONTAINERS.slice(0, 16).map((c) => `container:${c.name}`);

async function openAuditCase(page: Page) {
  await setupVaultMockApi(page, { healthSummary: summary(AUDIT_KEYS), historyRuns: [run(1, 'completed')] });
  await page.route('**/api/v1/containers', (route) => route.fulfill(json({ available: true, items: AUDIT_CONTAINERS })));
  await page.route('**/api/v1/vms', (route) => route.fulfill(json({ available: true, items: [] })));
  await page.route('**/api/v1/folders', (route) => route.fulfill(json({ available: true, items: [] })));
  await page.goto('/#/');
}

test.describe('banner states', () => {
  test('healthy: full coverage and no problems', async ({ page }) => {
    await open(page, HEALTHY);
    await expect(title(page)).toHaveText('Backups healthy');
    await expect(banner(page)).toContainText('All 7 items protected.');
    await expect(banner(page).getByRole('link')).toHaveCount(0);
    await expect(healthTile(page)).toContainText('All backups healthy');
  });

  test('the audit case: score 100 with 3 of 19 unprotected never reads as plain healthy', async ({ page }) => {
    await openAuditCase(page);
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('3 of 19 items unprotected');
    await expect(banner(page)).not.toContainText(/healthy/i);
    await expect(healthTile(page)).toContainText('100');
    await expect(healthTile(page)).toContainText('Backups OK · 3 unprotected');
    await expect(healthTile(page)).not.toContainText(/healthy/i);
    const stroke = await healthTile(page).locator('circle').nth(1).getAttribute('stroke');
    expect(stroke).toBe('var(--color-warning)');
  });

  test('failed runs', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 2 }), historyRuns: [run(1, 'failed'), run(2, 'failed')] });
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('2 recent failures');
    await expect(healthTile(page)).toContainText('2 recent failures');
  });

  test('a failure only the server counted still needs attention', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'completed')] });
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('1 recent failure');
  });

  test('warnings', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'partial')] });
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('1 run finished with warnings');
    await expect(healthTile(page)).toContainText('Backups OK · 1 with warnings');
  });

  test('mixed: every problem is named and has its own link', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4), { recent_failed: 1 }), historyRuns: [run(1, 'failed'), run(2, 'partial')] });
    await expect(banner(page)).toContainText('1 recent failure · 1 run finished with warnings · 3 of 7 items unprotected');
    await expect(banner(page).getByRole('link')).toHaveText(['Review failures', 'Review warnings', 'Add to a backup job']);
  });

  test('first run without storage', async ({ page }) => {
    const state = BANNER_STATES.find((s) => s.name === 'first run, no storage')!;
    await open(page, state.options);
    await expect(title(page)).toHaveText('Set up your first backup');
    await expect(page.getByRole('heading', { name: /welcome/i }).first()).toBeVisible();
  });

  test('storage without a job', async ({ page }) => {
    const state = BANNER_STATES.find((s) => s.name === 'no jobs')!;
    await open(page, state.options);
    await expect(title(page)).toHaveText('Create your first backup job');
    await expect(page.getByRole('heading', { name: 'Getting Started' })).toBeVisible();
  });

  test('jobs configured but nothing has run: waiting, not healthy', async ({ page }) => {
    const state = BANNER_STATES.find((s) => s.name === 'waiting for first backup')!;
    await open(page, state.options);
    await expect(title(page)).toHaveText('No backups completed yet');
    await expect(banner(page)).toContainText('7 items waiting for first backup');
    await expect(healthTile(page)).toContainText('No backups completed yet');
  });

  test('never claims healthy while data is loading', async ({ page }) => {
    await setupVaultMockApi(page, HEALTHY);
    let release = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/v1/health/summary', async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Checking backup status…');
    await expect(banner(page)).not.toContainText(/healthy/i);
    release();
    await expect(title(page)).toHaveText('Backups healthy');
  });

  test('an API error says so instead of claiming health', async ({ page }) => {
    await setupVaultMockApi(page, HEALTHY);
    await page.route('**/api/v1/jobs', (route) => route.fulfill(json({ error: 'database is locked' }, 500)));
    await page.goto('/#/');
    await expect(title(page)).toHaveText("Couldn't load backup status");
    await expect(page.getByText('database is locked')).toBeVisible();
    await banner(page).getByRole('link', { name: 'Open Logs' }).click();
    await expect(h1(page, 'Logs')).toBeVisible();
  });

  test('a failed health summary is unknown, and the score is not shown as 0', async ({ page }) => {
    await setupVaultMockApi(page, { historyRuns: [run(1, 'completed')] });
    await page.route('**/api/v1/health/summary', (route) => route.fulfill(json({ error: 'nope' }, 500)));
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Backup health unavailable');
    await expect(banner(page)).not.toContainText(/unprotected/);
    await expect(healthTile(page)).toContainText('Health unavailable');
    await expect(healthTile(page)).toContainText('–');
    await expect(page.getByText('Protection status unavailable')).toBeVisible();
    await expect(page.getByRole('link', { name: /unprotected →/ })).toHaveCount(0);
  });

  for (const endpoint of ['containers', 'vms', 'folders']) {
    test(`failed ${endpoint} discovery can't read as healthy`, async ({ page }) => {
      await setupVaultMockApi(page, HEALTHY);
      await page.route(`**/api/v1/${endpoint}`, (route) => route.fulfill(json({ error: 'nope' }, 500)));
      await page.goto('/#/');
      await expect(title(page)).toHaveText('Backup health unavailable');
    });
  }

  test('failed job history can\'t read as healthy', async ({ page }) => {
    await setupVaultMockApi(page, HEALTHY);
    await page.route('**/api/v1/jobs/*/history*', (route) => route.fulfill(json({ error: 'nope' }, 500)));
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Backup health unavailable');
  });

  test('Docker reported unavailable while jobs back up containers can\'t read as healthy', async ({ page }) => {
    await setupVaultMockApi(page, HEALTHY);
    await page.route('**/api/v1/containers', (route) => route.fulfill(json({ available: false, items: [] })));
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Backup health unavailable');
  });

  test('replica: judged from server data, since a replica discovers no items', async ({ page }) => {
    await open(page, { replica: true, ...HEALTHY });
    await expect(title(page)).toHaveText('Backups healthy');
    await expect(banner(page)).toContainText('All 7 items protected.');
    await expect(banner(page)).not.toContainText(/unavailable/);
    await expect(page.getByText(/^Read-only replica/).first()).toBeVisible();
  });

  test('replica: a failure links only to a page the replica has', async ({ page }) => {
    const state = BANNER_STATES.find((s) => s.name === 'replica with a failure')!;
    await open(page, state.options);
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('1 recent failure');
    await expect(banner(page).getByRole('link')).toHaveText(['Review failures']);
    await banner(page).getByRole('link', { name: 'Review failures' }).click();
    await expect(h1(page, 'Backup & Restore History')).toBeVisible();
  });

  test('replica: waiting offers no link to the hidden Jobs page', async ({ page }) => {
    await open(page, { replica: true, healthSummary: summary([], { health_score: 0, recent_success: 0, last_success_at: null, pending_keys: ['container:plex'], total_items: 1 }), historyRuns: [] });
    await expect(title(page)).toHaveText('No backups completed yet');
    await expect(banner(page)).toContainText('1 item waiting for first backup');
    await expect(banner(page).getByRole('link')).toHaveCount(0);
  });

  test('replica with nothing synced points at Replication', async ({ page }) => {
    await open(page, { replica: true, jobs: [], storage: [], historyRuns: [] });
    await expect(title(page)).toHaveText('No backup data on this replica yet');
    await banner(page).getByRole('link', { name: 'Open Replication' }).click();
    await expect(h1(page, 'Replication')).toBeVisible();
  });
});

test.describe('states that used to read as healthy', () => {
  const STALE = { id: 1, name: 'Docker & Appdata', enabled: false, last_success_at: '2025-03-12T02:15:00Z' };
  const REVIEW_SUMMARY = summary(ALL_ITEM_KEYS, {
    health_score: 40, recent_success: 0, recent_skipped: 5, recent_cancelled: 1, success_rate: 0, last_success_at: '2025-03-12T02:15:00Z', stale_jobs: [STALE],
  });
  const brokenStorage = (fields: Record<string, unknown>) => createInitialMockStorage().map((dest, i) => (i === 0 ? { ...dest, ...fields } : dest));

  const CASES: { name: string; options: VaultMockOptions; detail: string; link: string; heading: string; tile: string }[] = [
    { name: 'skipped run', options: { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'skipped')] }, detail: '1 run skipped', link: 'Review skipped runs', heading: 'Backup & Restore History', tile: 'Needs attention · 1 skipped' },
    { name: 'cancelled run', options: { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'cancelled')] }, detail: '1 run cancelled', link: 'Review cancelled runs', heading: 'Backup & Restore History', tile: 'Needs attention · 1 cancelled' },
    { name: 'failed storage health check', options: { ...HEALTHY, storage: brokenStorage({ name: 'Offline NAS', last_health_check_status: 'failed', last_health_check_at: '2026-10-06T02:00:00Z' }) as never }, detail: 'Storage "Offline NAS" is unhealthy', link: 'Check storage', heading: 'Storage Destinations', tile: 'Needs attention · storage unhealthy' },
    { name: 'open breaker', options: { ...HEALTHY, storage: brokenStorage({ name: 'Offline NAS', breaker_state: 'open' }) as never }, detail: 'Storage "Offline NAS" is unhealthy', link: 'Check storage', heading: 'Storage Destinations', tile: 'Needs attention · storage unhealthy' },
    { name: 'disabled job gone stale', options: { healthSummary: summary(ALL_ITEM_KEYS, { stale_jobs: [STALE] }), historyRuns: [run(1, 'completed')] }, detail: 'Disabled job "Docker & Appdata" last backed up 2025-03-12', link: 'Review jobs', heading: 'Backup Jobs', tile: 'Needs attention · 1 overdue' },
    { name: 'low score', options: { healthSummary: summary(ALL_ITEM_KEYS, { health_score: 40 }), historyRuns: [run(1, 'completed')] }, detail: 'Health score 40 of 100', link: 'View history', heading: 'Backup & Restore History', tile: 'Needs attention' },
  ];

  for (const { name, options, detail, link, heading, tile } of CASES) {
    test(`${name}: names the problem, agrees with the tile, and links to it`, async ({ page }) => {
      await open(page, options);
      await expect(title(page)).toHaveText('Needs attention');
      await expect(banner(page)).toContainText(detail);
      await expect(healthTile(page)).toContainText(tile);
      await expect(healthTile(page)).not.toContainText('All backups healthy');
      await banner(page).getByRole('link', { name: link }).click();
      await expect(h1(page, heading)).toBeVisible();
    });
  }

  test('the review scenario: skipped and cancelled runs, score 40, last success in 2025', async ({ page }) => {
    await open(page, { healthSummary: REVIEW_SUMMARY, historyRuns: [run(1, 'skipped'), run(2, 'cancelled')] });
    await expect(title(page)).toHaveText('Needs attention');
    for (const text of ['5 runs skipped', '1 run cancelled', 'last backed up 2025-03-12', 'Health score 40 of 100']) {
      await expect(banner(page)).toContainText(text);
    }
    await expect(healthTile(page)).toContainText(/Health score\s+40\s+Needs attention/);
    await expect(healthTile(page)).not.toContainText(/healthy/i);
  });

  test('a partial run on an eleventh job, past the sample, still needs attention', async ({ page }) => {
    const base = createInitialMockJobs()[0];
    const jobs = Array.from({ length: 11 }, (_, i) => ({ ...base, id: i + 1, name: `Job ${i + 1}` }));
    const eleventh = { ...run(11, 'partial'), job_id: 11 };
    await open(page, { jobs, healthSummary: summary(ALL_ITEM_KEYS, { recent_partial: 1 }), historyRuns: [run(1, 'completed'), eleventh] });
    await expect(title(page)).toHaveText('Needs attention');
    await expect(banner(page)).toContainText('1 run finished with warnings');
    await expect(healthTile(page)).toContainText('Backups OK · 1 with warnings');
  });

  test('failed discovery makes the tile qualify itself too, not only the banner', async ({ page }) => {
    await setupVaultMockApi(page, HEALTHY);
    await page.route('**/api/v1/vms', (route) => route.fulfill(json({ error: 'nope' }, 500)));
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Backup health unavailable');
    await expect(healthTile(page)).toContainText('Backups OK · some data unavailable');
  });

  test('a Needs attention tile counts them rather than reading 0', async ({ page }) => {
    await page.addInitScript((key) => localStorage.setItem(key, JSON.stringify(['attention'])), LAYOUT_KEY);
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'skipped')] });
    const tile = page.getByRole('link', { name: /^Needs attention/ });
    await expect(tile).toContainText('1');
    await expect(tile).toContainText('No failures · 1 other issue');
  });

  test('fits at 375 px with every problem listed', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open(page, { healthSummary: REVIEW_SUMMARY, historyRuns: [run(1, 'skipped'), run(2, 'cancelled')] });
    await expect(title(page)).toHaveText('Needs attention');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect(await healthTile(page).evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  });
});

test.describe('banner actions', () => {
  const ACTIONS: { state: VaultMockOptions; name: string; heading: string; hash: string }[] = [
    { state: { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4)), historyRuns: [run(1, 'completed')] }, name: 'Add to a backup job', heading: 'Backup Jobs', hash: '#/jobs' },
    { state: { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'failed')] }, name: 'Review failures', heading: 'Backup & Restore History', hash: '#/history?status=failed&review=outstanding' },
    { state: { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'partial')] }, name: 'Review warnings', heading: 'Backup & Restore History', hash: '#/history?status=partial&review=outstanding' },
    { state: BANNER_STATES.find((s) => s.name === 'first run, no storage')!.options, name: 'Add storage', heading: 'Storage Destinations', hash: '#/storage' },
    { state: BANNER_STATES.find((s) => s.name === 'no jobs')!.options, name: 'Create a job', heading: 'Backup Jobs', hash: '#/jobs' },
    { state: BANNER_STATES.find((s) => s.name === 'waiting for first backup')!.options, name: 'View jobs', heading: 'Backup Jobs', hash: '#/jobs' },
  ];

  for (const { state, name, heading, hash } of ACTIONS) {
    test(`${name} opens ${hash} by mouse, and is a real link`, async ({ page }) => {
      await open(page, state);
      const link = banner(page).getByRole('link', { name });
      await expect(link).toHaveAttribute('href', hash);
      await link.click();
      await expect(h1(page, heading)).toBeVisible();
      expect(await page.evaluate(() => location.hash)).toBe(hash);
    });
  }

  test('works from the keyboard', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'failed')] });
    await banner(page).getByRole('link', { name: 'Review failures' }).focus();
    await page.keyboard.press('Enter');
    await expect(h1(page, 'Backup & Restore History')).toBeVisible();
  });

  test('the unknown state links to History', async ({ page }) => {
    await setupVaultMockApi(page, { historyRuns: [] });
    await page.route('**/api/v1/health/summary', (route) => route.fulfill(json({}, 500)));
    await page.goto('/#/');
    await banner(page).getByRole('link', { name: 'View history' }).click();
    await expect(h1(page, 'Backup & Restore History')).toBeVisible();
  });

  test('links meet the touch target size and nothing overflows at 375 px', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4), { recent_failed: 1 }), historyRuns: [run(1, 'failed'), run(2, 'partial')] });
    await expect(title(page)).toHaveText('Needs attention');
    for (const link of await banner(page).getByRole('link').all()) {
      expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    }
    expect(await banner(page).evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await banner(page).getByRole('link', { name: 'Review failures' }).click();
    await expect(h1(page, 'Backup & Restore History')).toBeVisible();
  });
});

test.describe('announcements', () => {
  /** Serves a summary the test can change, with a socket to trigger reloads. */
  async function liveDashboard(page: Page) {
    const state = { summary: summary(ALL_ITEM_KEYS) as Record<string, unknown> };
    let socket: WebSocketRoute | undefined;
    await setupVaultMockApi(page, HEALTHY);
    await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
    await page.route('**/api/v1/health/summary', (route) => route.fulfill(json(state.summary)));
    await page.goto('/#/');
    await expect(title(page)).toHaveText('Backups healthy');
    await expect.poll(() => socket).toBeDefined();
    const change = async (next: Record<string, unknown>, expected: string) => {
      state.summary = next;
      socket!.send(JSON.stringify({ type: 'config_changed' }));
      await expect(banner(page)).toContainText(expected);
    };
    return { change };
  }

  test('stays silent on arrival, speaks once on a change of verdict, and only politely', async ({ page }) => {
    const { change } = await liveDashboard(page);
    await expect(announcer(page)).toHaveText('');
    await expect(announcer(page)).toHaveAttribute('aria-live', 'polite');
    await expect(banner(page).locator('[role="alert"]')).toHaveCount(0);

    await change(summary(ALL_ITEM_KEYS.slice(0, 4)), '3 of 7 items unprotected');
    await expect(announcer(page)).toHaveText('Needs attention. 3 of 7 items unprotected');

    await change(summary(ALL_ITEM_KEYS), 'All 7 items protected');
    await expect(announcer(page)).toHaveText('Backups healthy. All 7 items protected.');
  });

  test('a changed count alone is not re-announced', async ({ page }) => {
    const { change } = await liveDashboard(page);
    await change(summary(ALL_ITEM_KEYS.slice(0, 4)), '3 of 7 items unprotected');
    await expect(announcer(page)).toHaveText('Needs attention. 3 of 7 items unprotected');
    await change(summary(ALL_ITEM_KEYS.slice(0, 5)), '2 of 7 items unprotected');
    await expect(announcer(page)).toHaveText('Needs attention. 3 of 7 items unprotected');
  });

  test('the visible banner is not a live region', async ({ page }) => {
    await open(page, HEALTHY);
    await expect(title(page)).toBeVisible();
    await expect(banner(page).locator('[aria-live]')).toHaveCount(1);
    await expect(banner(page)).not.toHaveAttribute('role', /.+/);
  });
});

test.describe('progress tile', () => {
  const tile = (page: Page) => page.locator('.dash-tile').filter({ hasText: 'Backup in progress' });

  test('appears only while a backup runs, then leaves the grid', async ({ page }) => {
    let socket: WebSocketRoute | undefined;
    await setupVaultMockApi(page);
    await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
    await page.goto('/#/');
    await expect(title(page)).toBeVisible();
    await expect(tile(page)).toHaveCount(0);
    await expect(page.getByText('No backup running')).toHaveCount(0);
    await expect.poll(() => socket).toBeDefined();

    socket!.send(JSON.stringify({ type: 'job_run_started', job_id: 1, run_id: 50, job_name: 'Docker & Appdata', run_type: 'backup', items_total: 2 }));
    await expect(tile(page)).toBeVisible();
    await expect(tile(page)).toContainText('Docker & Appdata');

    socket!.send(JSON.stringify({ type: 'job_run_completed', job_id: 1, run_id: 50, status: 'completed' }));
    await expect(tile(page)).toHaveCount(0, { timeout: 10_000 });
    await expect(page.getByText('No backup running')).toHaveCount(0);
  });

  test('shows for a run already active when the page opens', async ({ page }) => {
    await setupVaultMockApi(page, { runnerStatus: { active: true, job_id: 1, run_id: 7, job_name: 'Docker & Appdata', run_type: 'backup', items_total: 1, items_done: 0, queue: [] } });
    await page.goto('/#/');
    await expect(tile(page)).toBeVisible();
  });
});

test.describe('Customize and saved layouts', () => {
  const seed = (page: Page, value: unknown) => page.addInitScript(([key, stored]) => {
    if (!sessionStorage.getItem('seeded')) {
      sessionStorage.setItem('seeded', '1');
      localStorage.setItem(key as string, JSON.stringify(stored));
    }
  }, [LAYOUT_KEY, value]);
  const customize = (page: Page) => page.getByRole('button', { name: 'Customize' }).click();
  const done = (page: Page) => page.getByRole('button', { name: 'Done' }).click();

  for (const [format, value] of [
    ['legacy array', ['progress', 'health', 'jobs']],
    ['object', { order: ['health', 'progress', 'jobs'], spans: { progress: 12, health: 6 } }],
  ] as const) {
    test(`a saved ${format} layout containing the progress tile still loads`, async ({ page }) => {
      await seed(page, value);
      await setupVaultMockApi(page);
      await page.goto('/#/');
      await expect(title(page)).toBeVisible();
      // Idle: hidden, and the rest of the saved layout is intact.
      await expect(page.locator('.dash-tile')).toHaveCount(2);
      await expect(page.getByText('No backup running')).toHaveCount(0);
      const saved = await page.evaluate((key) => localStorage.getItem(key), LAYOUT_KEY);
      expect(JSON.parse(saved!)).toEqual(value);

      // Customize still lists it in its saved place.
      await customize(page);
      await expect(page.locator('.dash-tile')).toHaveCount(3);
      await expect(page.getByText('No backup running')).toBeVisible();
      const order = await page.locator('.dash-tile').evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.idx));
      expect(order).toEqual(['0', '1', '2']);
      await done(page);
      await expect(page.locator('.dash-tile')).toHaveCount(2);
    });
  }

  test('a saved width is kept for a running backup', async ({ page }) => {
    await seed(page, { order: ['health', 'progress'], spans: { progress: 12 } });
    await setupVaultMockApi(page, { runnerStatus: { active: true, job_id: 1, run_id: 7, job_name: 'Docker & Appdata', run_type: 'backup', items_total: 1, queue: [] } });
    await page.goto('/#/');
    await expect(page.locator('.dash-tile[data-span="12"]')).toContainText('Backup in progress');
  });

  test('the tile can be removed, re-added and moved while idle, and persists', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/');
    await customize(page);
    await page.getByRole('button', { name: 'Remove Backup in progress' }).click();
    await expect(page.getByText('No backup running')).toHaveCount(0);
    const add = page.getByRole('complementary', { name: 'Add tiles' }).getByRole('button', { name: /Backup in progress/ });
    await add.click();
    await expect(page.getByText('No backup running')).toBeVisible();
    await page.getByRole('button', { name: 'Move Backup in progress up' }).click();
    await done(page);
    await page.reload();
    await expect(title(page)).toBeVisible();
    const saved = JSON.parse((await page.evaluate((key) => localStorage.getItem(key), LAYOUT_KEY))!);
    expect(saved.order).toContain('progress');
    await expect(page.getByText('No backup running')).toHaveCount(0);
  });

  test('the banner stays out of the tile grid and the catalog', async ({ page }) => {
    await open(page, HEALTHY);
    await expect(page.locator('.dash-tile-grid').getByRole('region', { name: 'Backup status' })).toHaveCount(0);
    await customize(page);
    await expect(banner(page)).toBeVisible();
    await expect(page.getByRole('complementary', { name: 'Add tiles' }).getByText(/backup status/i)).toHaveCount(0);
  });

  test('Reset restores the defaults without the idle progress tile', async ({ page }) => {
    await seed(page, ['health']);
    await setupVaultMockApi(page);
    await page.goto('/#/');
    await customize(page);
    await page.getByRole('button', { name: 'Reset' }).click();
    await done(page);
    await expect(healthTile(page)).toBeVisible();
    await expect(page.getByText('No backup running')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: '3-2-1 Backup Rule' })).toBeVisible();
  });
});

test.describe('health text', () => {
  const LONG_CASES: { name: string; options: VaultMockOptions; text: string }[] = [
    { name: 'qualified', options: { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4)), historyRuns: [run(1, 'completed')] }, text: 'Backups OK · 3 unprotected' },
    { name: 'all qualifiers', options: { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 3), { pending_keys: ALL_ITEM_KEYS.slice(3, 5) }), historyRuns: [run(1, 'partial')] }, text: 'Backups OK · 2 unprotected · 1 with warnings · 2 pending' },
    { name: 'no backups', options: BANNER_STATES.find((s) => s.name === 'waiting for first backup')!.options, text: 'No backups completed yet' },
  ];

  for (const width of [375, 768, 1024, 1440]) {
    for (const { name, options, text } of LONG_CASES) {
      test(`${name} text is whole at ${width} px`, async ({ page }) => {
        await page.setViewportSize({ width, height: 900 });
        await open(page, options);
        const line = healthTile(page).getByText(text, { exact: true });
        await expect(line).toBeVisible();
        const fit = await line.evaluate((el) => ({
          scroll: el.scrollWidth,
          client: el.clientWidth,
          truncated: getComputedStyle(el).textOverflow === 'ellipsis',
        }));
        expect(fit.scroll).toBeLessThanOrEqual(fit.client);
        expect(fit.truncated).toBe(false);
        // The tile holds it too, rather than the text spilling past the card.
        expect(await healthTile(page).evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
      });
    }
  }

  test('fits in the wider Retro pixel font at 375 px', async ({ page }) => {
    await seedTheme(page, 'retro', 'dark');
    await page.setViewportSize({ width: 375, height: 900 });
    await open(page, LONG_CASES[1].options);
    const line = healthTile(page).getByText(LONG_CASES[1].text, { exact: true });
    await expect(line).toBeVisible();
    expect(await line.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    expect(await healthTile(page).evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  });
});

test.describe('Needs attention tile', () => {
  const seed = (page: Page) => page.addInitScript((key) => localStorage.setItem(key, JSON.stringify(['attention'])), LAYOUT_KEY);
  const tile = (page: Page) => page.getByRole('link', { name: /^Needs attention/ });

  test('claims full protection only when every item is protected', async ({ page }) => {
    await seed(page);
    await open(page, HEALTHY);
    await expect(tile(page)).toContainText('No failures · all items protected');
  });

  test('with items still pending, it does not', async ({ page }) => {
    await seed(page);
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 5), { pending_keys: ALL_ITEM_KEYS.slice(5) }), historyRuns: [run(1, 'completed')] });
    await expect(tile(page)).toContainText('No failures');
    await expect(tile(page)).not.toContainText('all items protected');
  });
});

test.describe('3-2-1 panel', () => {
  const panel = (page: Page) => page.getByRole('button', { name: /^3-2-1 Backup Rule/ });
  const FRESH = BANNER_STATES.find((s) => s.name === 'waiting for first backup')!.options;

  test('reads Configured, not Met, while nothing is backed up', async ({ page }) => {
    await open(page, FRESH);
    await expect(panel(page)).toContainText('Configured');
    await expect(panel(page)).not.toContainText('Met');
    await panel(page).click();
    await expect(page.getByText('Configured, but no items are backed up yet.')).toBeVisible();
    await expect(page.getByText(/goal is met\./)).toHaveCount(0);
  });

  test('reads Met once something is protected', async ({ page }) => {
    await open(page, HEALTHY);
    await expect(panel(page)).toContainText('Met');
    await expect(panel(page)).not.toContainText('Configured');
  });

  test('fits at 375 px', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open(page, FRESH);
    await expect(panel(page)).toContainText('Configured');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});

test('reviewed cancellations clear Dashboard attention but stay in History', async ({ page }) => {
  await open(page, {
    healthSummary: summary(ALL_ITEM_KEYS, { recent_success: 3, recent_cancelled: 2, success_rate: 60, health_score: 76 }),
    historyRuns: [run(1, 'completed'), run(2, 'cancelled', 2), run(3, 'cancelled', 2)],
  });
  await expect(banner(page)).toContainText('2 runs cancelled');
  await banner(page).getByRole('link', { name: 'Review cancelled runs' }).click();
  await expect(page.getByRole('button', { name: 'Cancelled', exact: true })).toHaveAttribute('aria-pressed', 'true');
  const review = page.getByRole('checkbox', { name: 'Needs review' });
  await expect(review).toBeChecked();
  await page.getByRole('button', { name: /Select all needing review \(2\)/ }).click();
  await page.getByRole('textbox', { name: 'Acknowledgement reason (optional)' }).fill('Reviewed both cancellations');
  await page.getByRole('button', { name: 'Acknowledge selected' }).press('Enter');
  await expect(page.getByText('Acknowledged 2 runs. Skipped 0.')).toBeVisible();
  await expect(page.getByText('No matching runs')).toBeVisible();
  await expect(review).toBeFocused();
  await review.uncheck();
  await expect(page.getByText('Acknowledged', { exact: true })).toHaveCount(2);
  await expect(page.getByRole('button', { name: 'VM Backups cancelled' })).toHaveCount(2);
  await page.getByRole('button', { name: 'VM Backups cancelled' }).first().click();
  await expect(page.getByText('Reviewed both cancellations', { exact: true })).toBeVisible();

  await page.goto('/#/');
  await expect(title(page)).toHaveText('Backups healthy');
  await expect(banner(page).getByRole('link', { name: 'Review cancelled runs' })).toHaveCount(0);
  await expect(healthTile(page)).toContainText('100');
});

test('an open Dashboard refreshes when another client acknowledges a run', async ({ page }) => {
  let current = summary(ALL_ITEM_KEYS, { recent_cancelled: 1, outstanding_cancelled: 1, health_score: 88 });
  let socket: WebSocketRoute | undefined;
  await setupVaultMockApi(page, { historyRuns: [run(1, 'completed')] });
  await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
  await page.route('**/api/v1/health/summary', (route) => route.fulfill(json(current)));
  await page.goto('/#/');
  await expect(banner(page)).toContainText('1 run cancelled');
  await expect.poll(() => socket).toBeDefined();

  current = { ...current, outstanding_cancelled: 0, health_score: 100 };
  socket!.send(JSON.stringify({ type: 'job_runs_acknowledged', acknowledged: 1, ids: [2] }));
  await expect(title(page)).toHaveText('Backups healthy');
  await expect(banner(page).getByRole('link', { name: 'Review cancelled runs' })).toHaveCount(0);
});
