import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Frame, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { settleMotion } from './helpers/contrast';
import { BANNER_STATES } from './helpers/backup-status';
import { FRAME_ID, ROUTES, VIEWPORTS, openPresentation } from './helpers/presentation';

// Structural axe audit (#529). Colour contrast has its own suite across every
// theme in contrast.spec.ts; that is the only rule left out here.
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];

async function audit(page: Page, label: string, frame?: Frame) {
  await (frame ?? page).evaluate(() => Promise.all(document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations !== Infinity).map((a) => a.finished.catch(() => undefined))));
  let builder = new AxeBuilder({ page }).withTags(TAGS).disableRules(['color-contrast']);
  // Embedded: audit only Vault's document inside the host's frame.
  if (frame) builder = builder.include([`#${FRAME_ID}`, 'html'] as never);
  const { violations, passes } = await builder.analyze();
  // Guard against an empty audit: inside a frame, axe qualifies each target with the frame.
  if (frame) expect(passes.some((r) => r.nodes.some((n) => n.target.length > 1)), `${label}: audited the frame`).toBe(true);
  const summary = violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')} :: ${n.html.slice(0, 140)}`));
  expect(summary, `${label}: axe violations`).toEqual([]);
}

const PAGES = ROUTES;

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

// The embedded Backups view (#526) renders its own link row instead of the
// sidebar and bottom bar, so each page gets the same audit inside the frame.
test.describe('axe structural audit, embedded (#529)', () => {
  for (const viewport of VIEWPORTS) {
    test.describe(`at ${viewport.name}`, () => {
      test.use({ viewport: { width: viewport.width, height: viewport.height } });
      test.beforeEach(async ({ page }) => {
        await settleMotion(page);
        await setupVaultMockApi(page);
      });

      for (const { name, hash, ready } of PAGES) {
        test(name, async ({ page }) => {
          const frame = await openPresentation(page, 'embedded', hash);
          await expect(frame.locator(ready).first()).toBeVisible();
          await audit(page, `embedded ${name} ${viewport.name}`, frame);
        });
      }
    });
  }
});
