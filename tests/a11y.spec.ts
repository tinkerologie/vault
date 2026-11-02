import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { settleMotion } from './helpers/contrast';
import { BANNER_STATES } from './helpers/backup-status';

// Structural axe audit (#529). Colour contrast has its own suite across every
// theme in contrast.spec.ts; that is the only rule left out here.
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];

async function audit(page: Page, label: string) {
  await page.evaluate(() => Promise.all(document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations !== Infinity).map((a) => a.finished.catch(() => undefined))));
  const { violations } = await new AxeBuilder({ page }).withTags(TAGS).disableRules(['color-contrast']).analyze();
  const summary = violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')} :: ${n.html.slice(0, 140)}`));
  expect(summary, `${label}: axe violations`).toEqual([]);
}

const PAGES: { name: string; hash: string; ready: string }[] = [
  { name: 'Dashboard', hash: '#/', ready: '.dash-tile' },
  { name: 'Jobs', hash: '#/jobs', ready: 'text=Docker & Appdata' },
  { name: 'Storage', hash: '#/storage', ready: 'text=Local Array Backup' },
  { name: 'History', hash: '#/history', ready: 'main span:has-text("Docker & Appdata")' },
  { name: 'Restore', hash: '#/restore', ready: 'text=plex' },
  { name: 'Logs', hash: '#/logs', ready: 'text=Storage capacity above 75%' },
  { name: 'Replication', hash: '#/replication', ready: 'h1' },
  { name: 'Recovery', hash: '#/recovery', ready: 'h1' },
  { name: 'Recover wizard', hash: '#/recover', ready: 'h1' },
  { name: 'Anomalies', hash: '#/anomalies', ready: 'text=/Backup size spike/i' },
  { name: 'Settings', hash: '#/settings', ready: '[role="tabpanel"]' },
];

test.describe('axe structural audit (#529)', () => {
  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
    await setupVaultMockApi(page);
  });

  for (const { name, hash, ready } of PAGES) {
    test(`${name} desktop`, async ({ page }) => {
      await page.goto(`/${hash}`);
      await expect(page.locator(ready).first()).toBeVisible();
      await audit(page, name);
    });

    test(`${name} at 375 px`, async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 812 });
      await page.goto(`/${hash}`);
      await expect(page.locator(ready).first()).toBeVisible();
      await audit(page, `${name} mobile`);
    });
  }

  for (const tab of ['Notifications', 'Security', 'Reference']) {
    test(`Settings ${tab} tab`, async ({ page }) => {
      await page.goto('/#/settings');
      await page.getByRole('tab', { name: tab, exact: true }).click();
      await expect(page.getByRole('tabpanel', { name: tab })).toBeVisible();
      await audit(page, `Settings ${tab}`);
    });
  }

  test('Dashboard in Customize mode', async ({ page }) => {
    await page.goto('/#/');
    await page.getByRole('button', { name: 'Customize' }).click();
    await expect(page.getByRole('button', { name: 'Done' })).toBeVisible();
    await audit(page, 'Customize');
  });

  for (const { name, options, title } of BANNER_STATES) {
    test(`Dashboard status banner: ${name}`, async ({ page }) => {
      await setupVaultMockApi(page, options);
      await page.goto('/#/');
      await expect(page.getByRole('region', { name: 'Backup status' }).getByRole('heading', { name: title })).toBeVisible();
      await audit(page, `Banner ${name}`);
    });
  }

  test('Replication with sources', async ({ page }) => {
    await setupVaultMockApi(page, { replicationSources: [{ id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 }] });
    await page.goto('/#/replication');
    await expect(page.getByRole('heading', { name: 'Primary Tower' })).toBeVisible();
    await audit(page, 'Replication sources');
  });

  test('Job editor dialog', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await audit(page, 'Job editor');
  });

  test('Restore wizard plan step', async ({ page }) => {
    await page.goto('/#/restore');
    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    const summary = page.locator('summary', { hasText: 'plex' });
    await summary.click();
    await expect(page.getByRole('textbox', { name: 'Filter paths in plex' })).toBeVisible();
    await audit(page, 'Restore plan');
  });

  test('open More sheet', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'More', exact: true }).click();
    await expect(page.getByRole('dialog', { name: 'More' })).toBeVisible();
    await audit(page, 'More sheet');
  });
});
