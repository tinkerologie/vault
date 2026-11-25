import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Status pages (#681, #686, #693, #695, #697): failed loads say so with Retry,
// filters survive reload and Back, Anomalies explains when detection is off,
// and the Replication and Anomalies rows fit a phone.

const fail500 = (page: Page, pattern: string) =>
  page.route(pattern, (route) => route.fulfill({ status: 500, json: { error: 'boom' } }));

test.describe('failed loads offer Retry (#681)', () => {
  test('Replication shows an error, never "No replication targets"', async ({ page }) => {
    await setupVaultMockApi(page, { replicationSources: [{ id: 1, name: 'Peer', type: 'remote_vault', url: 'https://peer.lan:24085', enabled: true, schedule: '0 3 * * *', created_at: '2026-10-01T00:00:00Z' }] });
    let failing = true;
    await page.route('**/api/v1/replication', (route) => (failing ? route.fulfill({ status: 500, json: { error: 'boom' } }) : route.fallback()));
    await page.goto('/#/replication');
    await expect(page.getByText("Couldn't load replication targets")).toBeVisible();
    await expect(page.getByText('No replication targets')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Add Target/ })).toHaveCount(0);
    failing = false;
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByRole('heading', { name: 'Peer' })).toBeVisible();
  });

  test('Anomalies shows an error with Retry', async ({ page }) => {
    await setupVaultMockApi(page);
    let failing = true;
    await page.route('**/api/v1/anomalies*', (route) => (failing ? route.fulfill({ status: 500, json: { error: 'boom' } }) : route.fallback()));
    await page.goto('/#/anomalies');
    await expect(page.getByText("Couldn't load anomalies")).toBeVisible();
    await expect(page.getByText('No anomalies found')).toHaveCount(0);
    failing = false;
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByText(/Backup size spike/i).first()).toBeVisible();
  });

  test('Logs shows an error with Retry', async ({ page }) => {
    await setupVaultMockApi(page);
    await fail500(page, '**/api/v1/activity*');
    await page.goto('/#/logs');
    await expect(page.getByText("Couldn't load logs")).toBeVisible();
    await page.unroute('**/api/v1/activity*');
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByRole('region', { name: 'Log console' })).toBeVisible();
  });
});

test.describe('filters live in the URL (#693)', () => {
  test('Logs filters survive reload and Back', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/logs');
    const consoleBox = page.getByRole('region', { name: 'Log console' });
    await expect(consoleBox).toBeVisible();
    await page.getByLabel('Level').selectOption('error');
    await page.getByPlaceholder('Filter logs…').fill('remote');
    await expect(page).toHaveURL(/#\/logs\?level=error&q=remote$/);

    await page.reload();
    await expect(page.getByLabel('Level')).toHaveValue('error');
    await expect(page.getByPlaceholder('Filter logs…')).toHaveValue('remote');

    await page.goto('/#/jobs');
    await expect(page.getByRole('heading', { level: 1, name: 'Backup Jobs' })).toBeVisible();
    await page.goBack();
    await expect(page.getByLabel('Level')).toHaveValue('error');
    await expect(page.getByPlaceholder('Filter logs…')).toHaveValue('remote');
  });

  test('Anomalies filters survive reload and Back', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/anomalies');
    await page.getByLabel('Severity').selectOption('critical');
    await page.getByLabel('State').selectOption('');
    await expect(page).toHaveURL(/#\/anomalies\?state=all&severity=critical$/);
    await page.reload();
    await expect(page.getByLabel('Severity')).toHaveValue('critical');
    await expect(page.getByLabel('State')).toHaveValue('');
    await page.goto('/#/jobs');
    await page.goBack();
    await expect(page.getByLabel('Severity')).toHaveValue('critical');
  });
});

test('Anomalies with detection off explains why instead of redirecting (#695)', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.route('**/api/v1/settings', (route) => (route.request().method() === 'GET'
    ? route.fulfill({ json: { anomaly_detection_enabled: 'false' } })
    : route.fallback()));
  await page.goto('/#/anomalies');
  await expect(page.getByRole('heading', { name: 'Anomaly detection is off' })).toBeVisible();
  await page.waitForTimeout(300);
  await expect(page).toHaveURL(/#\/anomalies$/);
  await expect(page.getByRole('link', { name: /Settings/ }).last()).toHaveAttribute('href', '#/settings/general');
});

test('the Logs console is a labelled region, not a live log (#686)', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/logs');
  await expect(page.getByRole('region', { name: 'Log console' })).toBeVisible();
  await expect(page.getByRole('log')).toHaveCount(0);
  await expect(page.locator('[data-log-console] [aria-live]')).toHaveCount(0);
  const status = page.locator('[data-log-announcer]');
  await expect(status).toHaveAttribute('role', 'status');
  await expect(status).toHaveText('');
});

test.describe('phone layout (#697)', () => {
  test.use({ viewport: { width: 375, height: 812 } });

  test('a long Replication URL wraps and keeps the badge inside the card', async ({ page }) => {
    const url = `https://${'replica-'.repeat(7)}example.lan:24085`;
    await setupVaultMockApi(page, { replicationSources: [{ id: 1, name: 'Long peer', type: 'remote_vault', url, enabled: true, schedule: '0 3 * * *', last_sync_status: 'success', created_at: '2026-10-01T00:00:00Z' }] });
    await page.goto('/#/replication');
    const urlText = page.getByText(url);
    await expect(urlText).toBeVisible();
    const card = page.locator('.rounded-xl', { has: urlText }).first();
    const badge = card.getByText('Up to date');
    const [cardBox, badgeBox, textBox] = [await card.boundingBox(), await badge.boundingBox(), await urlText.boundingBox()];
    expect(badgeBox!.x + badgeBox!.width).toBeLessThanOrEqual(cardBox!.x + cardBox!.width);
    expect(textBox!.x + textBox!.width).toBeLessThanOrEqual(cardBox!.x + cardBox!.width);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  });

  test('an Anomalies row keeps a wide summary and shows scope, state and first seen', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/anomalies');
    const summary = page.getByRole('heading', { level: 2, name: /Backup size spike/i });
    await expect(summary).toBeVisible();
    expect((await summary.boundingBox())!.width).toBeGreaterThanOrEqual(200);
    const row = page.locator('.divide-y > div').first();
    await expect(row.getByRole('link', { name: 'Docker & Appdata' })).toBeVisible();
    // The State column is hidden on a phone; the meta line carries it instead.
    await expect(row.getByText('Open', { exact: true }).filter({ visible: true })).toHaveCount(1);
    await expect(row.getByText(/^First seen/).filter({ visible: true })).toHaveCount(1);
    await expect(row.getByRole('button', { name: /^Dismiss:/ })).toBeVisible();
  });
});
