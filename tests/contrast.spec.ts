import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { auditContrast, expectNoContrastViolations, seedTheme, settleMotion, type ThemeMode, type ThemeStyle } from './helpers/contrast';

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

async function open(page: Page, hash: string, ready: (page: Page) => Promise<void>) {
  await page.goto(`/${hash}`);
  await ready(page);
}

const heading = (name: string | RegExp) => async (page: Page) => {
  await expect(page.getByRole('heading', { name }).first()).toBeVisible();
};

// Every route in web/src/App.svelte's navigation, plus the hash-only recovery wizard.
const PAGES: { name: string; hash: string; ready: (page: Page) => Promise<void> }[] = [
  { name: 'Dashboard', hash: '#/', ready: async (p) => { await expect(p.locator('main').getByText('Docker & Appdata').first()).toBeVisible(); } },
  { name: 'Jobs', hash: '#/jobs', ready: heading('Backup Jobs') },
  { name: 'Storage', hash: '#/storage', ready: async (p) => { await expect(p.getByRole('heading', { name: 'Local Array Backup' })).toBeVisible(); } },
  { name: 'Restore', hash: '#/restore', ready: async (p) => { await expect(p.getByRole('button', { name: /plex/i })).toBeVisible(); } },
  { name: 'History', hash: '#/history', ready: async (p) => { await expect(p.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible(); } },
  { name: 'Recovery', hash: '#/recovery', ready: heading(/recovery/i) },
  { name: 'Recover wizard', hash: '#/recover', ready: heading(/Connect the storage/i) },
  { name: 'Replication', hash: '#/replication', ready: heading(/replication/i) },
  { name: 'Logs', hash: '#/logs', ready: async (p) => { await expect(p.getByText('Storage capacity above 75%').first()).toBeVisible(); } },
  { name: 'Anomalies', hash: '#/anomalies', ready: async (p) => { await expect(p.getByText(/Backup size spike/i).first()).toBeVisible(); } },
];

const SETTINGS_TABS = ['General', 'Security', 'Notifications', 'Reference'];

for (const mode of ['light', 'dark'] as ThemeMode[]) {
  test.describe(`WCAG AA contrast: default ${mode}`, () => {
    test.beforeEach(async ({ page }) => {
      await seedTheme(page, 'default', mode);
      await settleMotion(page);
      await setupVaultMockApi(page);
      // Tall enough that axe measures content instead of reporting it as scrolled under layers.
      await page.setViewportSize({ width: 1280, height: 2400 });
    });

    for (const { name, hash, ready } of PAGES) {
      test(`${name} page`, async ({ page }) => {
        await open(page, hash, ready);
        await expect(page.locator('html').first()).toHaveClass(mode === 'dark' ? /dark/ : /^(?!.*dark)/);
        await expectNoContrastViolations(page, `${mode} ${name}`);
      });
    }

    for (const tab of SETTINGS_TABS) {
      test(`Settings ${tab} tab`, async ({ page }) => {
        await page.goto('/#/settings');
        await page.getByRole('button', { name: tab, exact: true }).click();
        await page.waitForTimeout(100);
        await expectNoContrastViolations(page, `${mode} Settings ${tab}`);
      });
    }

    test('onboarding on an unconfigured server', async ({ page }) => {
      await page.route('**/api/v1/jobs', (route) => (route.request().method() === 'GET' ? route.fulfill(json([])) : route.fallback()));
      await page.goto('/#/');
      await expect(page.getByRole('heading', { name: 'Getting Started' })).toBeVisible();
      await expectNoContrastViolations(page, `${mode} getting started`);

      await page.route('**/api/v1/storage', (route) => (route.request().method() === 'GET' ? route.fulfill(json([])) : route.fallback()));
      await page.reload();
      await expect(page.getByRole('button', { name: /storage|start/i }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} welcome`);
    });

    test('active navigation and hovered primary button', async ({ page }) => {
      await page.goto('/#/jobs');
      const active = page.locator('aside nav button[aria-current="page"]');
      await expect(active).toContainText('Jobs');
      await expectNoContrastViolations(page, `${mode} active nav`, { include: 'aside nav' });

      const newJob = page.getByRole('button', { name: /new job/i });
      await newJob.hover();
      await newJob.evaluate((el) => el.setAttribute('data-audit', 'hover'));
      await expectNoContrastViolations(page, `${mode} hovered primary`, { include: '[data-audit="hover"]' });
    });

    test('hovered destructive button', async ({ page }) => {
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /delete storage/i }).first().click();
      const confirm = page.getByRole('button', { name: 'Delete Storage', exact: true });
      await confirm.hover();
      await page.waitForTimeout(50);
      await expectNoContrastViolations(page, `${mode} delete dialog with hovered danger button`);
    });

    test('History filters, run type, chart tooltip and run inspector', async ({ page }) => {
      await page.route('**/api/v1/history/trend**', (route) =>
        route.fulfill(json({
          period: '30d',
          bucket: 'day',
          points: ['2026-10-01', '2026-10-02', '2026-10-03'].map((day) => ({
            start: `${day}T00:00:00Z`,
            total_bytes: 2147483648,
            categories: { containers: 1073741824, vms: 1073741824 },
          })),
        })),
      );
      await page.goto('/#/history');
      await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();

      const main = page.locator('main');
      await main.getByRole('button', { name: 'Failed', exact: true }).click();
      await expect(page.locator('main span', { hasText: 'VM Backups' }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} History failed filter`);
      await main.getByRole('button', { name: 'All', exact: true }).first().click();

      await main.getByRole('button', { name: /^Restores?$/ }).click();
      await expect(page.getByText('restore', { exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} History restore runs`);
      await main.getByRole('button', { name: 'All', exact: true }).nth(1).click();

      await page.locator('main svg g[role="img"]').first().hover();
      await expect(page.locator('main .pointer-events-none.bg-surface-3')).toBeVisible();
      await expectNoContrastViolations(page, `${mode} History chart tooltip`);

      await page.locator('div[role="button"]', { hasText: 'Docker & Appdata' }).first().click();
      await expect(page.getByText('Deduplication savings 53%')).toBeVisible();
      await expectNoContrastViolations(page, `${mode} run inspector`);
    });

    test('Logs levels and focused row', async ({ page }) => {
      await page.goto('/#/logs');
      await expect(page.getByText('Failed to reach remote endpoint').first()).toBeVisible();
      await expect(page.getByText('debug', { exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} Logs levels`);

      for (const message of ['Failed to reach remote endpoint', 'Verbose scheduler tick']) {
        await page.getByText(message).first().click();
        await expectNoContrastViolations(page, `${mode} Logs focused row: ${message}`);
      }
    });

    test('Settings appearance controls', async ({ page }) => {
      await page.goto('/#/settings');
      const selected = page.getByRole('button', { name: mode === 'dark' ? 'Dark' : 'Light', exact: true });
      await expect(selected).toHaveClass(/bg-vault/);
      await expectNoContrastViolations(page, `${mode} selected appearance`, { include: '#set-appearance' });
    });

    test('success and error toasts', async ({ page }) => {
      await page.goto('/#/settings');
      await page.getByRole('button', { name: 'Notifications', exact: true }).click();
      await page.locator('#discord-url').fill('https://discord.com/api/webhooks/123456/abcdef');
      await page.locator('#discord-url').locator('..').getByRole('button', { name: 'Test' }).click();
      const toast = page.getByRole('alert');
      await expect(toast.filter({ hasText: /Test notification sent/i })).toBeVisible();
      await expect(toast.locator('[data-toast-icon="success"]')).toHaveAttribute('aria-hidden', 'true');
      await expectNoContrastViolations(page, `${mode} success toast`, { include: '[role="alert"]' });

      await page.route('**/api/v1/settings/discord/test', (route) =>
        route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'webhook rejected' }) }),
      );
      await page.locator('#discord-url').locator('..').getByRole('button', { name: 'Test' }).click();
      await expect(toast.filter({ hasText: /webhook rejected/i })).toBeVisible();
      await expect(toast.locator('[data-toast-icon="error"]')).toBeVisible();
      await expectNoContrastViolations(page, `${mode} error toast`, { include: '[role="alert"]' });
    });

    test('job wizard, delete dialog and run queued toast', async ({ page }) => {
      await page.goto('/#/jobs');
      await page.getByRole('button', { name: /run backup now/i }).first().click();
      await expect(page.getByText(/queued for execution/i).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} job queued`);

      await page.getByRole('button', { name: /new job/i }).click();
      await expect(page.getByText('Select one or more backup types.')).toBeVisible();
      await page.getByRole('button', { name: /Containers/i }).first().click();
      await page.getByRole('button', { name: /plex/i }).first().click();
      await expectNoContrastViolations(page, `${mode} job wizard items`);

      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${mode} job wizard second step`);

      await page.reload();
      await page.getByRole('button', { name: /delete job/i }).first().click();
      await expect(page.getByRole('dialog').first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} delete job dialog`);
    });

    test('storage form and storage browser', async ({ page }) => {
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /add storage/i }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog.getByRole('heading', { name: /add storage/i })).toBeVisible();
      await expectNoContrastViolations(page, `${mode} add storage`);
      await dialog.getByRole('button', { name: 'S3 / S3-Compatible' }).click();
      await expect(dialog.getByRole('button', { name: 'Wasabi' })).toBeVisible();
      await expectNoContrastViolations(page, `${mode} add S3 storage`);

      await page.reload();
      await page.getByRole('button', { name: /browse destination files/i }).first().click();
      await expect(page.getByText('Browse Local Array Backup')).toBeVisible();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${mode} storage browser`);
    });

    test('restore wizard steps', async ({ page }) => {
      await page.goto('/#/restore');
      await page.getByRole('button', { name: /plex/i }).click();
      await page.getByRole('button', { name: /home-assistant/i }).click();
      await expectNoContrastViolations(page, `${mode} restore items`);

      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await expect(page.getByText(/Your selection comes from 2 backup jobs/i)).toBeVisible();
      await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
      await page.getByRole('button', { name: /VM Backups/i }).first().click();
      await expectNoContrastViolations(page, `${mode} restore points`);
    });

    test('command palette and changelog modal', async ({ page }) => {
      await page.route('**/api/v1/release/changelog', (route) =>
        route.fulfill(json({ releases: [{ version: 'v2026.09.01', date: '2026-09-01', sections: { Fixed: ['Readable text'] } }] })),
      );
      await page.goto('/#/settings');
      await page.keyboard.press('Control+k');
      await expect(page.locator('kbd').first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} command palette`);
      await page.keyboard.press('Escape');

      await page.getByRole('button', { name: /view changelog/i }).click();
      await expect(page.getByText('v2026.09.01').first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} changelog modal`);
    });

    test('dashboard customisation', async ({ page }) => {
      await page.goto('/#/');
      await page.getByRole('button', { name: /customi[sz]e|edit/i }).first().click();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${mode} dashboard edit mode`);
    });

    test('mobile navigation at 375 px', async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 812 });
      await page.goto('/#/jobs');
      const nav = page.locator('nav[aria-label="Mobile navigation"]');
      await expect(nav).toBeVisible();
      await expectNoContrastViolations(page, `${mode} mobile jobs`);

      await page.locator('header button[aria-expanded], button[aria-label*="menu" i]').first().click();
      await expect(page.getByRole('button', { name: 'Logs', exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${mode} mobile menu`);
    });

    test('skip link when focused', async ({ page }) => {
      await page.goto('/#/jobs');
      await page.keyboard.press('Tab');
      await expect(page.getByRole('link', { name: /skip/i })).toBeFocused();
      await expectNoContrastViolations(page, `${mode} skip link`, { include: 'a[href="#main-content"]' });
    });
  });
}

// Retro palettes are out of scope for the audit (#532 extends it). This only guards
// that token and markup changes above did not leak default values into them.
const RETRO: { style: ThemeStyle; mode: ThemeMode; selected: string }[] = [
  { style: '1bit', mode: 'light', selected: 'rgb(255, 255, 255)' },
  { style: '1bit', mode: 'dark', selected: 'rgb(0, 0, 0)' },
  { style: '8bit', mode: 'light', selected: 'rgb(255, 255, 255)' },
  { style: '8bit', mode: 'dark', selected: 'rgb(255, 255, 255)' },
  { style: '16bit', mode: 'light', selected: 'rgb(255, 255, 255)' },
  { style: '16bit', mode: 'dark', selected: 'rgb(255, 255, 255)' },
];

async function cssColor(page: Page, token: string) {
  return page.evaluate((name) => {
    const probe = document.createElement('div');
    probe.style.color = `var(${name})`;
    document.body.appendChild(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    return color;
  }, token);
}

for (const { style, mode, selected } of RETRO) {
  test(`retro palette unchanged: ${style} ${mode}`, async ({ page }) => {
    await seedTheme(page, style, mode);
    await settleMotion(page);
    await setupVaultMockApi(page);

    await page.goto('/#/storage');
    const primary = page.locator('.btn-primary').first();
    if (await primary.count()) {
      await expect(primary).toHaveCSS('color', await cssColor(page, '--color-surface'));
    }

    await page.goto('/#/history');
    const allFilter = page.getByRole('button', { name: 'All', exact: true }).first();
    await expect(allFilter).toHaveClass(/bg-vault/);
    await expect(allFilter).toHaveCSS('color', selected);

    await page.setViewportSize({ width: 375, height: 812 });
    const activeMobile = page.locator('nav[aria-label="Mobile navigation"] button[aria-current="page"]');
    await expect(activeMobile).toHaveCSS('color', await cssColor(page, '--color-vault'));
  });
}

test('placeholders use the readable muted token in default themes', async ({ page }) => {
  await seedTheme(page, 'default', 'light');
  await settleMotion(page);
  await setupVaultMockApi(page);
  await page.goto('/#/storage');
  await page.getByRole('button', { name: /add storage/i }).click();
  const color = await page.locator('[role="dialog"] input[placeholder]').first().evaluate((el) => getComputedStyle(el, '::placeholder').color);
  expect(color).toBe(await cssColor(page, '--color-text-muted'));
});

test('the audit reports a real failure with its measured colours', async ({ page }) => {
  await seedTheme(page, 'default', 'light');
  await setupVaultMockApi(page);
  await page.goto('/#/jobs');
  await expect(page.getByRole('heading', { name: 'Backup Jobs' })).toBeVisible();
  await page.evaluate(() => {
    const probe = document.createElement('p');
    probe.id = 'contrast-probe';
    probe.textContent = 'Deliberately unreadable';
    probe.style.cssText = 'color:#f59e0b;background:#ffffff;font-size:14px';
    document.querySelector('main')?.prepend(probe);
  });
  const { violations } = await auditContrast(page, { include: '#contrast-probe' });
  expect(violations).toHaveLength(1);
  expect(violations[0]).toMatchObject({ foreground: '#f59e0b', background: '#ffffff', required: '4.5:1' });
  expect(violations[0].ratio).toBeLessThan(4.5);
});
