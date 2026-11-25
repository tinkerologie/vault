import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import { setupVaultMockApi, type VaultMockOptions } from './mocks/vault-api';
import { ALL_ITEM_KEYS, run, summary } from './helpers/backup-status';

// Dashboard and Disaster Recovery status fixes from the UI/UX audit (#707).
const LAYOUT_KEY = 'vault:dash:layout:v1';
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
const banner = (page: Page) => page.getByRole('region', { name: 'Backup status' });
const h1 = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true });
const seedLayout = (page: Page, order: string[]) =>
  page.addInitScript(([key, value]) => localStorage.setItem(key, value), [LAYOUT_KEY, JSON.stringify(order)]);

const NINE_DAYS_AGO = new Date(Date.now() - 9 * 86400_000).toISOString();
// plex is out of date, ubuntu-vm waits for its first backup, postgres is in no job.
const MIXED = summary(ALL_ITEM_KEYS.filter((key) => !['container:postgres', 'vm:ubuntu-vm'].includes(key)), {
  pending_keys: ['vm:ubuntu-vm'],
  out_of_date_keys: ['container:plex'],
  item_last_backup_at: { 'container:plex': NINE_DAYS_AGO },
  total_items: 6,
});

async function open(page: Page, options: VaultMockOptions, hash = '#/') {
  await setupVaultMockApi(page, options);
  await page.goto(`/${hash}`);
}

test.describe('one protection classification (#708, #694)', () => {
  test('an out-of-date item is not shown as protected on the Dashboard', async ({ page }) => {
    await open(page, { healthSummary: MIXED, historyRuns: [run(1, 'completed')] });
    const plex = page.locator('[data-protection-state]').filter({ hasText: 'plex' });
    await expect(plex).toHaveAttribute('data-protection-state', 'out_of_date');
    await expect(plex).toContainText('Last backup 9d ago');
    await expect(page.locator('[data-protection-state="pending"]').filter({ hasText: 'ubuntu-vm' })).toContainText('pending first backup');
    await expect(page.locator('[data-protection-state="unprotected"]').filter({ hasText: 'postgres' })).toBeVisible();
    await expect(banner(page)).toContainText('1 of 7 items out of date');
    await expect(banner(page).getByRole('link', { name: 'Review out-of-date items' })).toHaveAttribute('href', '#/recovery');
  });

  test('Dashboard and Disaster Recovery report the same counts', async ({ page }) => {
    await open(page, { healthSummary: MIXED, historyRuns: [run(1, 'completed')] });
    // 4 of 7 protected: Flash Drive, Appdata, nextcloud, home-assistant.
    await expect(page.locator('.dash-tile').filter({ has: page.getByText('Protected', { exact: true }) }).first()).toContainText('4/7');
    await page.goto('/#/recovery');
    const hero = page.locator('[data-readiness]');
    await expect(hero).toContainText('57%');
    await expect(hero).toContainText('4 of 7 items protected and up to date.');
    await expect(hero).toContainText('1 out of date');
    await expect(hero).toContainText('1 pending first backup');
    await expect(hero).toContainText('1 unprotected');
    // Pending and unprotected items are listed apart.
    await expect(page.getByRole('heading', { name: /Pending first backup/ })).toBeVisible();
    await expect(page.getByRole('heading', { name: /Unprotected Items/ })).toBeVisible();
  });

  test('Recovery readiness is unknown, not 100%, when the summary fails', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.route('**/api/v1/health/summary', (route) => route.fulfill(json({ error: 'boom' }, 500)));
    await page.goto('/#/recovery');
    const hero = page.locator('[data-readiness]');
    await expect(hero).toContainText('readiness is unknown');
    await expect(hero).not.toContainText('100%');
    await expect(hero.getByRole('button', { name: 'Retry' })).toBeVisible();
  });
});

test.describe('3-2-1 tile (#708)', () => {
  test('with jobs and the rule off, says the check is off', async ({ page }) => {
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS) });
    await page.route('**/api/v1/settings', (route) => route.request().method() === 'GET'
      ? route.fulfill(json({ backup_rule_enabled: 'false' })) : route.fallback());
    await page.goto('/#/');
    await page.getByRole('button', { name: 'Customize' }).click();
    await expect(page.getByText('3-2-1 check is turned off.')).toBeVisible();
    await expect(page.getByText(/add a job to compute it/)).toHaveCount(0);
  });
});

test.describe('failed loads are not all-clear (#681)', () => {
  test('a failed anomaly count reads unavailable, not a green 0', async ({ page }) => {
    await seedLayout(page, ['anomalies']);
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS) });
    let fail = true;
    await page.route('**/api/v1/anomalies**', (route) => fail ? route.fulfill(json({ error: 'boom' }, 500)) : route.fallback());
    await page.goto('/#/');
    const tile = page.locator('.dash-tile').filter({ hasText: 'Anomalies' });
    await expect(tile).toContainText('Anomalies unavailable');
    await expect(tile).not.toContainText('No unusual runs detected');
    fail = false;
    await tile.getByRole('button', { name: 'Retry' }).click();
    await expect(tile).toContainText('open — review on Anomalies');
  });

  test('a failed size trend says so with Retry', async ({ page }) => {
    await seedLayout(page, ['sizeTrend']);
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS) });
    await page.route('**/api/v1/history/trend**', (route) => route.fulfill(json({ error: 'boom' }, 500)));
    await page.goto('/#/');
    const tile = page.locator('.dash-tile').filter({ hasText: 'Backup size trend' });
    await expect(tile).toContainText("Couldn't load the size trend");
    await expect(tile).not.toContainText('Not enough history yet');
    await expect(tile.getByRole('button', { name: 'Retry' })).toBeVisible();
  });

  test('a failed Dashboard load offers Retry', async ({ page }) => {
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS) });
    let fail = true;
    await page.route('**/api/v1/jobs', (route) => fail ? route.fulfill(json({ error: 'boom' }, 500)) : route.fallback());
    await page.goto('/#/');
    await expect(page.getByText("Couldn't load the dashboard")).toBeVisible();
    fail = false;
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByText("Couldn't load the dashboard")).toHaveCount(0);
    await expect(page.locator('.dash-tile').first()).toBeVisible();
  });

  test('a failed recovery plan offers Retry', async ({ page }) => {
    await setupVaultMockApi(page);
    let fail = true;
    await page.route('**/api/v1/recovery/plan', (route) => fail ? route.fulfill(json({ error: 'boom' }, 500)) : route.fallback());
    await page.goto('/#/recovery');
    await expect(page.getByText("Couldn't load the recovery plan")).toBeVisible();
    fail = false;
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.locator('[data-readiness]')).toBeVisible();
  });
});

test.describe('progress announcements (#686)', () => {
  test('the progress card is not a live region; one announcer speaks on start only', async ({ page }) => {
    let socket: WebSocketRoute | undefined;
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS) });
    await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
    await page.goto('/#/');
    await expect.poll(() => socket).toBeDefined();
    socket!.send(JSON.stringify({ type: 'job_run_started', job_id: 1, run_id: 50, job_name: 'Docker & Appdata', run_type: 'backup', items_total: 2 }));
    const card = page.locator('.dash-tile').filter({ hasText: 'Backup in progress' });
    await expect(card).toBeVisible();
    await expect(card.locator('[aria-live], [role="status"], [role="log"]')).toHaveCount(0);
    const announcer = page.locator('[data-progress-announcer]');
    await expect(announcer).toHaveAttribute('role', 'status');
    await expect(announcer).toHaveText('Backup of Docker & Appdata started');
    // Ticking timers and counts never reach the announcer.
    await page.waitForTimeout(1500);
    await expect(announcer).toHaveText('Backup of Docker & Appdata started');
  });
});

test.describe('Needs attention tile (#693)', () => {
  test('with failures it opens History filtered to them', async ({ page }) => {
    await seedLayout(page, ['attention']);
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'failed')] });
    await expect(page.getByRole('link', { name: /^Needs attention/ })).toHaveAttribute('href', '#/history?status=failed&review=outstanding');
  });

  test('with only unprotected items it opens Disaster Recovery', async ({ page }) => {
    await seedLayout(page, ['attention']);
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4)), historyRuns: [run(1, 'completed')] });
    await expect(page.getByRole('link', { name: /^Needs attention/ })).toHaveAttribute('href', '#/recovery');
  });
});

test.describe('vault.key caution (#714)', () => {
  test('shows on the Dashboard and Disaster Recovery when the key is at risk, and clears on acknowledgement', async ({ page }) => {
    let atRisk = true;
    const posted: Record<string, string>[] = [];
    await setupVaultMockApi(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'completed')] });
    await page.route('**/api/v1/health/summary', (route) => route.fulfill(json(summary(ALL_ITEM_KEYS, { server_key_at_risk: atRisk }))));
    await page.route('**/api/v1/settings', async (route) => {
      if (route.request().method() === 'PUT') {
        posted.push(route.request().postDataJSON());
        atRisk = false;
        return route.fulfill(json({}));
      }
      return route.fallback();
    });
    await page.goto('/#/');
    const save = banner(page).getByRole('link', { name: 'Save vault.key' });
    await expect(save).toHaveAttribute('href', '#/settings');
    await expect(banner(page)).toContainText('vault.key is the only key to your deduplicated backups');

    await page.goto('/#/recovery');
    const caution = page.locator('[data-server-key-caution]');
    await expect(caution).toContainText('Save a copy of vault.key, or set a backup password');
    await caution.getByRole('button', { name: 'I keep a copy elsewhere' }).click();
    await expect(caution).toHaveCount(0);
    expect(Object.keys(posted[0])).toEqual(['server_key_saved_at']);
  });

  test('stays hidden when the key is not at risk', async ({ page }) => {
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'completed')] });
    await expect(banner(page).getByRole('link', { name: 'Save vault.key' })).toHaveCount(0);
    await page.goto('/#/recovery');
    await expect(page.locator('[data-server-key-caution]')).toHaveCount(0);
  });
});

const PLAN = {
  server_info: { vault_version: '2026.11.03', total_protected_items: 2 },
  steps: [
    { step: 1, title: 'Install Vault Plugin', description: 'Install Vault.', status: 'ready' },
    {
      step: 2, title: 'Restore Containers (2)', description: 'Restore all Docker container appdata from backup.', status: 'warning',
      items: [
        { name: 'a-container-with-a-really-quite-long-name-for-testing-purposes', type: 'container', last_backup: NINE_DAYS_AGO, storage_name: 'Offsite NAS share', size_bytes: 1024 ** 3, has_restore_point: true, out_of_date: true },
        { name: 'plex', type: 'container', last_backup: null, storage_name: 'Local', size_bytes: 0, has_restore_point: false, out_of_date: false },
      ],
    },
  ],
  warnings: [],
};

test.describe('Disaster Recovery steps (#692, #697)', () => {
  test('steps are headings with a native disclosure button', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.route('**/api/v1/recovery/plan', (route) => route.fulfill(json(PLAN)));
    await page.goto('/#/recovery');
    const heading = page.getByRole('heading', { level: 3, name: 'Restore Containers (2)' });
    const toggle = heading.getByRole('button', { name: 'Restore Containers (2)' });
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(page.locator('#recovery-step-2')).toContainText('Pending first backup');
    await expect(page.locator('#recovery-step-2')).toContainText('Out of date');
    await toggle.press('Enter');
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  });

  test('at 375 px a long item name stays readable beside its details', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await setupVaultMockApi(page);
    await page.route('**/api/v1/recovery/plan', (route) => route.fulfill(json(PLAN)));
    await page.goto('/#/recovery');
    await page.getByRole('button', { name: 'Restore Containers (2)' }).click();
    const name = page.getByText(PLAN.steps[1].items![0].name, { exact: true });
    expect((await name.boundingBox())!.width).toBeGreaterThan(200);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});

test.describe('layout (#699, #715)', () => {
  test('at 375 px the status banner is the first card under the heading', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'completed')] });
    await expect(h1(page, 'Dashboard')).toBeVisible();
    const customize = page.getByRole('button', { name: 'Customize' });
    await expect(customize).toBeVisible();
    const bannerBox = (await banner(page).boundingBox())!;
    const customizeBox = (await customize.boundingBox())!;
    const headingBox = (await h1(page, 'Dashboard').boundingBox())!;
    expect(customizeBox.y).toBeGreaterThan(bannerBox.y + bannerBox.height - 1);
    // Nothing but the heading's own description sits between it and the banner.
    expect(bannerBox.y - (headingBox.y + headingBox.height)).toBeLessThan(80);
  });

  test('at 1440 px the Flash Drive name and Customize labels are not truncated', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page, { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'completed')] });
    await page.getByRole('button', { name: 'Show items' }).click();
    const flash = page.locator('[data-protection-state]').filter({ hasText: 'USB boot drive' });
    const name = flash.locator('span.break-words').first();
    await expect(name).toBeVisible();
    expect(await name.evaluate((el) => el.scrollWidth <= el.clientWidth && el.getBoundingClientRect().width > 40)).toBe(true);

    await page.getByRole('button', { name: 'Customize' }).click();
    const labels = page.locator('[data-tile-label]');
    await expect(labels.first()).toBeVisible();
    for (const label of await labels.all()) {
      const text = (await label.textContent())!.trim();
      expect(await label.evaluate((el) => el.scrollWidth <= el.clientWidth), text).toBe(true);
      expect((await label.boundingBox())!.width, text).toBeGreaterThan(text.length * 4);
    }
  });
});
