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

const THEMES = (['default', '1bit', 'retro'] as ThemeStyle[]).flatMap((style) =>
  (['light', 'dark'] as ThemeMode[]).map((mode) => ({ style, mode, label: `${style} ${mode}` })),
);

// Routes App.svelte keeps on a replica (route-guard.js hides Jobs, Restore and Recovery).
const REPLICA_PAGES = PAGES.filter(({ hash }) => !['#/jobs', '#/restore', '#/recovery', '#/recover'].includes(hash));

const REPLICATION_SOURCES = [
  { id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 },
  { id: 2, name: 'Offsite Tower', url: 'http://offsite.example.test:24085', enabled: true, schedule: '0 4 * * *', last_sync_at: '2026-10-06T04:00:00Z', last_sync_status: 'failed', last_sync_error: 'connection refused', storage_dest_id: 1 },
];

const RUNNING_RESTORE = {
  active: true,
  job_id: 1,
  run_id: 77,
  job_name: 'Docker & Appdata',
  run_type: 'restore',
  items_total: 3,
  items_done: 1,
  items_failed: 1,
  started_at: '2026-10-06T12:00:00Z',
  current_item: 'plex',
  current_item_type: 'container',
  current_item_message: 'Restoring volumes',
  current_item_percent: 42,
  phase: 'transferring',
  bytes_done: 2147483648,
  bytes_total: 5368709120,
  progress_determinate: true,
  rate_bytes_per_sec: 5242880,
  eta_seconds: 600,
  queue: [{ id: 2, job_id: 2, job_name: 'VM Backups', run_type: 'backup', status: 'queued' }],
};

for (const { style, mode, label } of THEMES) {
  test.describe(`WCAG AA contrast: ${label}`, () => {
    test.beforeEach(async ({ page }) => {
      await seedTheme(page, style, mode);
      await settleMotion(page);
      await setupVaultMockApi(page);
      // Tall enough that axe measures content instead of reporting it as scrolled under layers.
      await page.setViewportSize({ width: 1280, height: 2400 });
    });

    for (const { name, hash, ready } of PAGES) {
      test(`${name} page`, async ({ page }) => {
        await open(page, hash, ready);
        await expect(page.locator('html').first()).toHaveClass(mode === 'dark' ? /dark/ : /^(?!.*dark)/);
        await expectNoContrastViolations(page, `${label} ${name}`);
      });
    }

    for (const tab of SETTINGS_TABS) {
      test(`Settings ${tab} tab`, async ({ page }) => {
        await page.goto('/#/settings');
        await page.getByRole('button', { name: tab, exact: true }).click();
        await page.waitForTimeout(100);
        await expectNoContrastViolations(page, `${label} Settings ${tab}`);
      });
    }

    test('onboarding on an unconfigured server', async ({ page }) => {
      await page.route('**/api/v1/jobs', (route) => (route.request().method() === 'GET' ? route.fulfill(json([])) : route.fallback()));
      await page.goto('/#/');
      await expect(page.getByRole('heading', { name: 'Getting Started' })).toBeVisible();
      await expectNoContrastViolations(page, `${label} getting started`);

      await page.route('**/api/v1/storage', (route) => (route.request().method() === 'GET' ? route.fulfill(json([])) : route.fallback()));
      await page.reload();
      await expect(page.getByRole('button', { name: /storage|start/i }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} welcome`);
    });

    test('active navigation and hovered primary button', async ({ page }) => {
      await page.goto('/#/jobs');
      const active = page.locator('aside nav button[aria-current="page"]');
      await expect(active).toContainText('Jobs');
      await expectNoContrastViolations(page, `${label} active nav`, { include: 'aside nav' });

      const newJob = page.getByRole('button', { name: /new job/i });
      await newJob.hover();
      await newJob.evaluate((el) => el.setAttribute('data-audit', 'hover'));
      await expectNoContrastViolations(page, `${label} hovered primary`, { include: '[data-audit="hover"]' });
    });

    test('hovered destructive button', async ({ page }) => {
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /delete storage/i }).first().click();
      const confirm = page.getByRole('button', { name: 'Delete Storage', exact: true });
      await confirm.hover();
      await page.waitForTimeout(50);
      await expectNoContrastViolations(page, `${label} delete dialog with hovered danger button`);
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
      await expectNoContrastViolations(page, `${label} History failed filter`);
      await main.getByRole('button', { name: 'All', exact: true }).first().click();

      await main.getByRole('button', { name: /^Restores?$/ }).click();
      await expect(page.getByText('restore', { exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} History restore runs`);
      await main.getByRole('button', { name: 'All', exact: true }).nth(1).click();

      await page.locator('main svg g[role="img"]').first().hover();
      await expect(page.locator('main .pointer-events-none.bg-surface-3')).toBeVisible();
      await expectNoContrastViolations(page, `${label} History chart tooltip`);

      await page.locator('div[role="button"]', { hasText: 'Docker & Appdata' }).first().click();
      await expect(page.getByText('Deduplication savings 53%')).toBeVisible();
      await expectNoContrastViolations(page, `${label} run inspector`);
    });

    test('Logs levels and focused row', async ({ page }) => {
      await page.goto('/#/logs');
      await expect(page.getByText('Failed to reach remote endpoint').first()).toBeVisible();
      await expect(page.getByText('debug', { exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} Logs levels`);

      for (const message of ['Failed to reach remote endpoint', 'Verbose scheduler tick']) {
        await page.getByText(message).first().click();
        await expectNoContrastViolations(page, `${label} Logs focused row: ${message}`);
      }
    });

    test('Settings appearance controls', async ({ page }) => {
      await page.goto('/#/settings');
      const selected = page.getByRole('button', { name: mode === 'dark' ? 'Dark' : 'Light', exact: true });
      await expect(selected).toHaveClass(/bg-vault/);
      await expectNoContrastViolations(page, `${label} selected appearance`, { include: '#set-appearance' });
    });

    test('success and error toasts', async ({ page }) => {
      await page.goto('/#/settings');
      await page.getByRole('button', { name: 'Notifications', exact: true }).click();
      await page.locator('#discord-url').fill('https://discord.com/api/webhooks/123456/abcdef');
      await page.locator('#discord-url').locator('..').getByRole('button', { name: 'Test' }).click();
      const toast = page.getByRole('alert');
      await expect(toast.filter({ hasText: /Test notification sent/i })).toBeVisible();
      await expect(toast.locator('[data-toast-icon="success"]')).toHaveAttribute('aria-hidden', 'true');
      await expectNoContrastViolations(page, `${label} success toast`, { include: '[role="alert"]' });

      await page.route('**/api/v1/settings/discord/test', (route) =>
        route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'webhook rejected' }) }),
      );
      await page.locator('#discord-url').locator('..').getByRole('button', { name: 'Test' }).click();
      await expect(toast.filter({ hasText: /webhook rejected/i })).toBeVisible();
      await expect(toast.locator('[data-toast-icon="error"]')).toBeVisible();
      await expectNoContrastViolations(page, `${label} error toast`, { include: '[role="alert"]' });
    });

    test('job wizard, delete dialog and run queued toast', async ({ page }) => {
      await page.goto('/#/jobs');
      await page.getByRole('button', { name: /run backup now/i }).first().click();
      await expect(page.getByText(/queued for execution/i).first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} job queued`);

      await page.getByRole('button', { name: /new job/i }).click();
      await expect(page.getByText('Select one or more backup types.')).toBeVisible();
      await page.getByRole('button', { name: /Containers/i }).first().click();
      await page.getByRole('button', { name: /plex/i }).first().click();
      await expectNoContrastViolations(page, `${label} job wizard items`);

      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${label} job wizard second step`);

      await page.reload();
      await page.getByRole('button', { name: /delete job/i }).first().click();
      await expect(page.getByRole('dialog').first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} delete job dialog`);
    });

    test('storage form and storage browser', async ({ page }) => {
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /add storage/i }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog.getByRole('heading', { name: /add storage/i })).toBeVisible();
      await expectNoContrastViolations(page, `${label} add storage`);
      await dialog.getByRole('button', { name: 'S3 / S3-Compatible' }).click();
      await expect(dialog.getByRole('button', { name: 'Wasabi' })).toBeVisible();
      await expectNoContrastViolations(page, `${label} add S3 storage`);

      await page.reload();
      await page.getByRole('button', { name: /browse destination files/i }).first().click();
      await expect(page.getByText('Browse Local Array Backup')).toBeVisible();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${label} storage browser`);
    });

    test('restore wizard steps', async ({ page }) => {
      await page.goto('/#/restore');
      await page.getByRole('button', { name: /plex/i }).click();
      await page.getByRole('button', { name: /home-assistant/i }).click();
      await expectNoContrastViolations(page, `${label} restore items`);

      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await expect(page.getByText(/Your selection comes from 2 backup jobs/i)).toBeVisible();
      await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
      await page.getByRole('button', { name: /VM Backups/i }).first().click();
      await expectNoContrastViolations(page, `${label} restore points`);
    });

    test('command palette and changelog modal', async ({ page }) => {
      await page.route('**/api/v1/release/changelog', (route) =>
        route.fulfill(json({ releases: [{ version: 'v2026.09.01', date: '2026-09-01', sections: { Fixed: ['Readable text'] } }] })),
      );
      await page.goto('/#/settings');
      await page.keyboard.press('Control+k');
      await expect(page.locator('kbd').first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} command palette`);
      await page.keyboard.press('Escape');

      await page.getByRole('button', { name: /view changelog/i }).click();
      await expect(page.getByText('v2026.09.01').first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} changelog modal`);
    });

    test('dashboard customisation', async ({ page }) => {
      await page.goto('/#/');
      await page.getByRole('button', { name: /customi[sz]e|edit/i }).first().click();
      await page.waitForTimeout(100);
      await expectNoContrastViolations(page, `${label} dashboard edit mode`);
    });

    test('mobile navigation at 375 px', async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 812 });
      await page.goto('/#/jobs');
      const nav = page.locator('nav[aria-label="Mobile navigation"]');
      await expect(nav).toBeVisible();
      await expectNoContrastViolations(page, `${label} mobile jobs`);

      await page.locator('header button[aria-expanded], button[aria-label*="menu" i]').first().click();
      await expect(page.getByRole('button', { name: 'Logs', exact: true }).first()).toBeVisible();
      await expectNoContrastViolations(page, `${label} mobile menu`);
    });

    test('skip link when focused', async ({ page }) => {
      await page.goto('/#/jobs');
      await page.keyboard.press('Tab');
      await expect(page.getByRole('link', { name: /skip/i })).toBeFocused();
      await expectNoContrastViolations(page, `${label} skip link`, { include: 'a[href="#main-content"]' });
    });

    test.describe('replica mode', () => {
      test.beforeEach(async ({ page }) => {
        // Second setup wins: Playwright runs the most recently added route first.
        await setupVaultMockApi(page, { replica: true, replicationSources: REPLICATION_SOURCES });
      });

      for (const { name, hash, ready } of REPLICA_PAGES) {
        test(`${name} page`, async ({ page }) => {
          await open(page, hash, ready);
          await expect(page.getByText(/^Read-only replica/).first()).toBeVisible();
          await expectNoContrastViolations(page, `${label} replica ${name}`);
        });
      }

      for (const tab of SETTINGS_TABS) {
        test(`read-only Settings ${tab} tab`, async ({ page }) => {
          await page.goto('/#/settings');
          await expect(page.getByText(/^Read-only replica/).first()).toBeVisible();
          await page.getByRole('button', { name: tab, exact: true }).click();
          await page.waitForTimeout(100);
          await expectNoContrastViolations(page, `${label} replica Settings ${tab}`);
        });
      }
    });

    test.describe('active runs', () => {
      test('restore running in the restore wizard', async ({ page }) => {
        await setupVaultMockApi(page, { runnerStatus: RUNNING_RESTORE });
        await page.goto('/#/restore');
        await page.getByRole('button', { name: /plex/i }).click();
        await page.getByRole('button', { name: /home-assistant/i }).click();
        await page.getByRole('button', { name: 'Next', exact: true }).click();
        await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
        await page.getByRole('button', { name: /VM Backups/i }).first().click();
        await page.getByRole('button', { name: /Continue/i }).click();
        await expect(page.getByText('Restore in progress')).toBeVisible();
        await expect(page.getByRole('progressbar').first()).toBeVisible();
        await expectNoContrastViolations(page, `${label} restore running`);
      });

      test('backup running on the dashboard and Jobs page', async ({ page }) => {
        await setupVaultMockApi(page, { runnerStatus: { ...RUNNING_RESTORE, run_type: 'backup', current_item_message: 'Uploading' } });
        await page.goto('/#/');
        await expect(page.getByRole('progressbar').first()).toBeVisible();
        await expectNoContrastViolations(page, `${label} backup running on dashboard`);
        await page.goto('/#/jobs');
        await expect(page.getByRole('heading', { name: 'Backup Jobs' })).toBeVisible();
        await expectNoContrastViolations(page, `${label} backup running on Jobs`);
      });
    });
  });
}

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

// axe's color-contrast rule does not measure placeholders, so assert the token instead.
for (const { style, mode, label } of THEMES) {
  test(`placeholders use the readable muted token: ${label}`, async ({ page }) => {
    await seedTheme(page, style, mode);
    await settleMotion(page);
    await setupVaultMockApi(page);
    await page.goto('/#/storage');
    await page.getByRole('button', { name: /add storage/i }).click();
    const color = await page.locator('dialog input[placeholder]').first().evaluate((el) => getComputedStyle(el, '::placeholder').color);
    expect(color).toBe(await cssColor(page, '--color-text-muted'));
  });
}

// A top-layer modal makes the page inert, so prove the audit still measures dialog content.
test('the audit measures text inside a native dialog', async ({ page }) => {
  await seedTheme(page, 'default', 'light');
  await setupVaultMockApi(page);
  await page.goto('/#/jobs');
  await page.getByRole('button', { name: /delete job/i }).first().click();
  await expect(page.getByRole('dialog', { name: 'Delete Job' })).toBeVisible();
  await page.locator('dialog p').first().evaluate((el) => {
    el.id = 'contrast-probe';
    el.style.cssText = 'color:#f59e0b;background:#ffffff;font-size:14px';
  });
  const { violations } = await auditContrast(page, { include: '#contrast-probe' });
  expect(violations).toHaveLength(1);
});

for (const { style, mode, label } of THEMES) {
  test(`toast above an open dialog: ${label}`, async ({ page }) => {
    await seedTheme(page, style, mode);
    await settleMotion(page);
    await setupVaultMockApi(page);
    await page.route(/\/api\/v1\/storage$/, (route) => route.request().method() === 'POST'
      ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'backend down' }) })
      : route.fallback());
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Add Storage' });
    await dialog.locator('#sname').fill('Rejected');
    await dialog.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups/vault');
    await dialog.getByRole('button', { name: 'Add Storage', exact: true }).click();
    await expect(page.getByRole('alert')).toBeVisible();
    await expectNoContrastViolations(page, `${label} toast over dialog`, { include: '[role="alert"]' });
  });
}

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
