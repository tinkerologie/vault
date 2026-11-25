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
    await page.getByRole('button', { name: /run backup now/i }).first().click();
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
