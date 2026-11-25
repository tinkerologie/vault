// History page UX (#681, #690, #693, #695, #699, #711) in a real browser.
import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

const GB = 1024 ** 3;
const TREND = {
  period: '30d',
  bucket: 'day',
  points: ['2026-10-01', '2026-10-02', '2026-10-03', '2026-10-04'].map((day, i) => ({
    start: `${day}T00:00:00Z`,
    total_bytes: (i + 2) * GB,
    categories: { containers: GB, vms: (i + 1) * GB },
  })),
};

async function withTrend(page: Page) {
  await page.route('**/api/v1/history/trend**', (route) => route.fulfill({ json: TREND }));
}

const sizeChart = (page: Page) => page.locator('[data-chart="size"]');

for (const width of [375, 1280]) {
  test(`chart axis labels are at least 11 px at ${width} px`, async ({ page }) => {
    await setupVaultMockApi(page);
    await withTrend(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/#/history');
    const labels = sizeChart(page).locator('[data-axis-label]');
    await expect(labels.first()).toBeVisible();
    const sizes = await labels.evaluateAll((els) => els.map((el) => parseFloat(getComputedStyle(el).fontSize)));
    expect(sizes.length).toBeGreaterThan(3);
    for (const size of sizes) expect(size).toBeGreaterThanOrEqual(11);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
}

test('Tab reaches each size bar and shows its tooltip; a data table is exposed', async ({ page }) => {
  await setupVaultMockApi(page);
  await withTrend(page);
  await page.goto('/#/history');
  const bars = sizeChart(page).getByRole('img');
  await expect(bars).toHaveCount(4);
  await page.getByRole('button', { name: '1y' }).focus();
  await page.keyboard.press('Tab');
  await expect(bars.first()).toBeFocused();
  await expect(sizeChart(page).locator('[data-chart-tooltip]')).toContainText('2 GB total');
  await page.keyboard.press('Tab');
  await expect(bars.nth(1)).toBeFocused();
  await expect(sizeChart(page).locator('[data-chart-tooltip]')).toContainText('3 GB total');

  const table = page.getByRole('table', { name: 'Backup size by date' });
  await expect(table.getByRole('columnheader')).toHaveText(['Date', 'Total', 'Containers', 'VMs']);
  await expect(table.getByRole('row')).toHaveCount(5);
});

test('a period with fewer than two buckets says so', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.route('**/api/v1/history/trend**', (route) => route.fulfill({ json: { ...TREND, points: TREND.points.slice(0, 1) } }));
  await page.goto('/#/history');
  await expect(sizeChart(page)).toContainText('Not enough data for this period');
});

test('a failed trend fetch shows an error with Retry in the chart area', async ({ page }) => {
  await setupVaultMockApi(page);
  let fail = true;
  await page.route('**/api/v1/history/trend**', (route) => (fail
    ? route.fulfill({ status: 500, json: { error: 'trend unavailable' } })
    : route.fulfill({ json: TREND })));
  await page.goto('/#/history');
  await expect(page.getByText("Couldn't load the size trend for 30d")).toBeVisible();
  await expect(sizeChart(page)).toHaveCount(0);
  fail = false;
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(sizeChart(page).getByRole('img')).toHaveCount(4);
});

test('a failed history load offers Retry that reloads History', async ({ page }) => {
  await setupVaultMockApi(page);
  let fail = true;
  await page.route('**/api/v1/history?**', (route) => (fail
    ? route.fulfill({ status: 500, json: { error: 'database is locked' } })
    : route.fallback()));
  await page.goto('/#/history');
  await expect(page.getByText("Couldn't load history")).toBeVisible();
  fail = false;
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
});

test('filters live in the URL and survive reload and Back/Forward', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/history');
  const main = page.locator('main');
  await main.getByRole('button', { name: 'Failed', exact: true }).click();
  await main.getByRole('button', { name: 'Backups', exact: true }).click();
  await page.getByRole('combobox', { name: 'Filter by job' }).selectOption({ label: 'VM Backups' });
  await page.getByRole('textbox', { name: 'Search runs' }).fill('VM');
  await expect(page).toHaveURL(/#\/history\?status=failed&job=2&kind=backup&q=VM$/);

  await page.reload();
  await expect(main.getByRole('button', { name: 'Failed', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(main.getByRole('button', { name: 'Backups', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByRole('combobox', { name: 'Filter by job' })).toHaveValue('2');
  await expect(page.getByRole('textbox', { name: 'Search runs' })).toHaveValue('VM');

  await page.evaluate(() => { window.location.hash = '/logs'; });
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText(/History/);
  await page.goBack();
  await expect(page).toHaveURL(/status=failed&job=2&kind=backup&q=VM$/);
  await expect(main.getByRole('button', { name: 'Failed', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByRole('textbox', { name: 'Search runs' })).toHaveValue('VM');
  await page.goForward();
  await expect(page).toHaveURL(/#\/logs$/);
});

test('a fresh install shows a setup step instead of no matches', async ({ page }) => {
  await setupVaultMockApi(page, { historyRuns: [], jobs: [] });
  await page.goto('/#/history');
  await expect(page.getByText('No runs yet')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Create a job' })).toHaveAttribute('href', '#/jobs');
  await expect(page.getByText('No matching runs')).toHaveCount(0);
});

test('Clear filters brings back the runs a search hid', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/history?q=nothing-matches');
  await expect(page.getByText('No matching runs')).toBeVisible();
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
  await expect(page).toHaveURL(/#\/history$/);
});

test('the bulk toolbar appears only once runs are selected', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/history');
  const toolbar = page.getByRole('region', { name: 'Run acknowledgement' });
  await expect(page.getByRole('checkbox', { name: /Select run 902/ })).toBeVisible();
  await expect(toolbar).toHaveCount(0);
  await page.getByRole('checkbox', { name: /Select run 902/ }).check();
  await expect(toolbar).toBeVisible();
  await expect(toolbar.getByRole('button', { name: 'Acknowledge selected' })).toBeVisible();
});

test('at 1440×900 the first runs are above the fold and the request panel is one line', async ({ page }) => {
  await setupVaultMockApi(page);
  await withTrend(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/#/history');
  const firstRun = page.locator('main span', { hasText: 'Docker & Appdata' }).first();
  await expect(firstRun).toBeVisible();
  const box = await firstRun.boundingBox();
  expect(box && box.y + box.height).toBeLessThanOrEqual(900);
  const panel = page.getByRole('region', { name: 'Request history' });
  await expect(panel).toContainText('No Restore Requests in history.');
  const height = await panel.evaluate((el) => el.getBoundingClientRect().height);
  expect(height).toBeLessThanOrEqual(56);
});

test('Purge lives in the More actions menu and keeps typed confirmation', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/history');
  await expect(page.getByRole('button', { name: /purge/i })).toHaveCount(0);
  const more = page.getByRole('button', { name: 'More actions' });
  await more.click();
  await expect(more).toHaveAttribute('aria-expanded', 'true');
  const item = page.getByRole('menuitem', { name: 'Purge history…' });
  await expect(item).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('menu')).toHaveCount(0);
  await expect(more).toBeFocused();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Purge All History' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('textbox')).toBeVisible();
});

test('a Docker socket error explains the fix and keeps the raw text under Details', async ({ page }) => {
  const raw = 'backup chunked: backup volume /var/run/docker.sock: folder: open source root: open /run/docker.sock: no such device or address';
  await setupVaultMockApi(page, {
    historyRuns: [{
      id: 950, job_id: 1, status: 'partial', started_at: '2026-10-03T02:00:00Z', completed_at: '2026-10-03T02:05:00Z',
      log: JSON.stringify([
        { name: 'portainer', type: 'container', status: 'failed', error: raw },
        { name: 'plex', type: 'container', status: 'ok' },
      ]),
    }],
  });
  await page.goto('/#/history');
  await expect(page.getByText("The Docker socket can't be backed up.")).toBeVisible();
  await expect(page.getByText(/Exclude \/var\/run\/docker\.sock/)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Edit Docker & Appdata' })).toHaveAttribute('href', '#/jobs');
  const raws = page.locator('details').getByText(raw);
  await expect(raws).toBeHidden();
  await page.getByText('Details', { exact: true }).click();
  await expect(raws).toBeVisible();
});
