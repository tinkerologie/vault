import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// The app shell (#686, #691, #715): the connection-lost banner, the command
// palette and the shortcut hint.

/** Every WebSocket the app opens is refused, as when the daemon is down. */
async function refuseWebSocket(page: Page) {
  await page.routeWebSocket('**/api/v1/ws', (ws) => { ws.close(); });
}

const banner = (page: Page) => page.getByText('Live connection to the Vault daemon is unavailable', { exact: false });

test.describe('connection-lost banner (#686)', () => {
  test.beforeEach(async ({ page }) => {
    await page.clock.install();
    await setupVaultMockApi(page);
  });

  test('a lasting drop shows the banner once, after a grace period, without covering the page', async ({ page }) => {
    await refuseWebSocket(page);
    await page.goto('/#/jobs');
    const h1 = page.getByRole('heading', { level: 1 });
    await expect(h1).toBeVisible();
    const region = page.locator('[data-connection-banner]');
    await expect(region).toHaveRole('status');
    // Mark the live region, so a re-inserted one would show up as unmarked.
    await region.evaluate((el) => { el.dataset.marked = '1'; });

    await page.clock.runFor(2000);
    await expect(banner(page)).toHaveCount(0);
    await page.clock.runFor(1500);
    await expect(banner(page)).toBeVisible();

    // It stays up, in the same live region, through many retries.
    for (let i = 0; i < 5; i++) {
      await page.clock.runFor(5000);
      await expect(banner(page)).toBeVisible();
    }
    await expect(page.locator('[data-connection-banner][data-marked="1"]')).toHaveCount(1);
    await expect(page.getByRole('status').filter({ hasText: 'Live connection' })).toHaveCount(1);

    // The page heading moves down below it rather than sitting under it.
    const b = (await region.boundingBox())!;
    const h = (await h1.boundingBox())!;
    expect(h.y).toBeGreaterThanOrEqual(b.y + b.height);
  });

  test('the banner sits above the toasts, not over them', async ({ page }) => {
    await refuseWebSocket(page);
    await page.goto('/#/jobs');
    await page.clock.runFor(3500);
    await expect(banner(page)).toBeVisible();
    await page.getByRole('button', { name: /^Run Now:/ }).first().click();
    const toast = page.locator('[data-toast]').first();
    await expect(toast).toBeVisible();
    const b = (await page.locator('[data-connection-banner]').boundingBox())!;
    const t = (await toast.boundingBox())!;
    expect(t.y).toBeGreaterThanOrEqual(b.y + b.height);
  });

  test('a healthy connection never shows the banner', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.clock.runFor(10_000);
    await expect(banner(page)).toHaveCount(0);
  });
});

test.describe('version shown once (#715)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  const dashVersion = (page: Page) => page.locator('main [data-dash-version]');

  test('on desktop only the sidebar shows the version', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto('/#/');
    await expect(page.locator('aside footer')).toContainText(/^v\S+/);
    await expect(dashVersion(page)).toBeAttached();
    await expect(dashVersion(page)).toBeHidden();
  });

  test('without the sidebar the Dashboard header shows it', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto('/#/');
    await expect(dashVersion(page)).toBeVisible();
  });
});

test.describe('command palette (#691)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  const search = (page: Page) => page.getByRole('combobox', { name: 'Search commands' });

  test('the active option follows the arrow keys and stays in view', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.keyboard.press('Control+k');
    await expect(search(page)).toBeFocused();
    const list = page.getByRole('listbox', { name: 'Commands' });
    await expect(search(page)).toHaveAttribute('aria-controls', (await list.getAttribute('id'))!);
    await expect(list.getByRole('option').nth(12)).toBeAttached();
    for (let i = 0; i < 12; i++) await page.keyboard.press('ArrowDown');
    const active = list.getByRole('option', { selected: true });
    await expect(active).toHaveCount(1);
    await expect(search(page)).toHaveAttribute('aria-activedescendant', (await active.getAttribute('id'))!);
    await expect(active).toBeInViewport();
    const [a, l] = [(await active.boundingBox())!, (await list.boundingBox())!];
    expect(a.y).toBeGreaterThanOrEqual(l.y - 1);
    expect(a.y + a.height).toBeLessThanOrEqual(l.y + l.height + 1);
  });

  test('every sidebar page is in the palette', async ({ page }) => {
    await page.goto('/#/jobs');
    // The sidebar fills in as feature flags load; read it only once it has.
    const nav = page.locator('aside nav');
    await expect(nav.getByRole('link', { name: 'Anomalies' })).toBeVisible();
    const labels = (await nav.getByRole('link').allTextContents()).map((l) => l.trim());
    expect(labels).toEqual(expect.arrayContaining(['Replication', 'Disaster Recovery', 'Anomalies']));
    await page.keyboard.press('Control+k');
    const pages = page.getByRole('group', { name: 'Pages' });
    for (const label of labels) {
      await expect(pages.getByRole('option', { name: new RegExp(`^(Go to|Open) ${label}`) })).toHaveCount(1);
    }
  });

  test('announces the number of results', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.keyboard.press('Control+k');
    await search(page).fill('disaster');
    await expect(page.getByRole('dialog').getByRole('status')).toHaveText('1 result');
  });

  test('the sidebar shows the shortcut and opens the palette', async ({ page }) => {
    await page.goto('/#/jobs');
    const hint = page.getByRole('complementary', { name: 'Sidebar' }).getByRole('button', { name: 'Search' });
    await expect(hint).toBeVisible();
    await expect(hint).toHaveAttribute('aria-keyshortcuts', /Control\+K/);
    await expect(hint).toContainText(/(⌘|Ctrl\+)K/);
    await hint.click();
    await expect(search(page)).toBeFocused();
  });
});
