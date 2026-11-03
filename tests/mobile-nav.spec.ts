import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { seedTheme, settleMotion } from './helpers/contrast';

// One mobile navigation pattern (#530): the bottom bar plus the More sheet.
type Entry = { label: string; hash: string; heading: string };

const entry = (label: string, hash: string, heading: string): Entry => ({ label, hash, heading });
const HOME = entry('Home', '#/', 'Dashboard');
const JOBS = entry('Jobs', '#/jobs', 'Backup Jobs');
const HISTORY = entry('History', '#/history', 'Backup & Restore History');
const RESTORE = entry('Restore', '#/restore', 'Restore');
const REPLICATION = entry('Replication', '#/replication', 'Replication');
const LOGS = entry('Logs', '#/logs', 'Logs');
const STORAGE = entry('Storage', '#/storage', 'Storage Destinations');
const RECOVERY = entry('Recovery', '#/recovery', 'Recovery Guide');
const ANOMALIES = entry('Anomalies', '#/anomalies', 'Anomalies');
const SETTINGS = entry('Settings', '#/settings', 'Settings');

const MODES = [
  { mode: 'daemon', replica: false, bar: [HOME, JOBS, HISTORY, RESTORE], sheet: [STORAGE, RECOVERY, REPLICATION, LOGS, ANOMALIES, SETTINGS] },
  { mode: 'replica', replica: true, bar: [HOME, REPLICATION, HISTORY, LOGS], sheet: [STORAGE, ANOMALIES, SETTINGS] },
];

// Every page the app dispatches, with what proves its content has loaded.
const ALL_PAGES = [
  { name: 'Dashboard', hash: '#/', heading: 'Dashboard', replica: true },
  { name: 'Jobs', hash: '#/jobs', heading: 'Backup Jobs', replica: false },
  { name: 'Storage', hash: '#/storage', heading: 'Storage Destinations', replica: true },
  { name: 'History', hash: '#/history', heading: 'Backup & Restore History', replica: true },
  { name: 'Restore', hash: '#/restore', heading: 'Restore', replica: false },
  { name: 'Logs', hash: '#/logs', heading: 'Logs', replica: true },
  { name: 'Replication', hash: '#/replication', heading: 'Replication', replica: true },
  { name: 'Recovery', hash: '#/recovery', heading: 'Recovery Guide', replica: false },
  { name: 'Recover wizard', hash: '#/recover', heading: 'Recover Vault', replica: false },
  { name: 'Anomalies', hash: '#/anomalies', heading: 'Anomalies', replica: true },
  { name: 'Settings', hash: '#/settings', heading: 'Settings', replica: true },
];
const SETTINGS_TABS = ['General', 'Security', 'Notifications', 'Reference'];

// A daemon shows Replication once a source exists; a replica always does.
const REPLICATION_SOURCES = [{ id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 }];

const h1 = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true });
const bar = (page: Page) => page.getByRole('navigation', { name: 'Mobile navigation' });
const more = (page: Page) => bar(page).getByRole('button', { name: 'More', exact: true });
const sheet = (page: Page) => page.getByRole('dialog', { name: 'More' });
const sheetLinks = (page: Page) => sheet(page).getByRole('link');

/** The panel scales in, so its geometry is only final once that animation ends. */
async function openSheet(page: Page) {
  await more(page).click();
  await expect(sheet(page)).toBeVisible();
  await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))));
}

async function waitForPage(page: Page, hash: string, heading: string) {
  await page.goto(`/${hash}`);
  await expect(h1(page, heading)).toBeVisible();
  // Let data-driven content (and so its widest row) arrive before measuring.
  await page.waitForLoadState('networkidle');
}

async function expectNoOverflow(page: Page, label: string) {
  const sizes = await page.evaluate(() => {
    const root = document.documentElement;
    const main = document.getElementById('main-content') as HTMLElement;
    return { doc: root.scrollWidth - root.clientWidth, main: main.scrollWidth - main.clientWidth };
  });
  expect(sizes, `${label}: horizontal overflow in px`).toEqual({ doc: 0, main: 0 });
}

async function expectAxeTargetSize(page: Page, label: string) {
  const { violations } = await new AxeBuilder({ page }).withRules(['target-size']).analyze();
  const found = violations.flatMap((v) => v.nodes.map((n) => `${n.target.join(' ')} :: ${n.failureSummary}`));
  expect(found, `${label}: targets under 24 px`).toEqual([]);
}

test.describe('mobile navigation at 375 px (#530)', () => {
  test.use({ viewport: { width: 375, height: 812 }, hasTouch: true });

  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
  });

  for (const { mode, replica, bar: barPages, sheet: sheetPages } of MODES) {
    test.describe(`${mode} mode`, () => {
      test.beforeEach(async ({ page }) => {
        await setupVaultMockApi(page, { replica, replicationSources: REPLICATION_SOURCES });
      });

      test('the sheet lists exactly the pages the bar leaves out, plus Documentation', async ({ page }) => {
        await page.goto('/#/');
        await expect(bar(page).getByRole('link')).toHaveText(barPages.map((p) => p.label));
        await more(page).click();
        await expect(sheetLinks(page)).toHaveText([...sheetPages.map((p) => p.label), 'Documentation']);
        const docs = sheet(page).getByRole('link', { name: 'Documentation (opens in a new tab)' });
        await expect(docs).toHaveAttribute('href', 'https://tinkerologie.github.io/vault/');
        await expect(docs).toHaveAttribute('target', '_blank');
      });

      test('the walk reaches every page through the bar and More', async ({ page }) => {
        await page.goto('/#/settings');
        await expect(h1(page, 'Settings')).toBeVisible();

        for (const target of barPages) {
          await bar(page).getByRole('link', { name: target.label, exact: true }).click();
          await expect(page).toHaveURL(new RegExp(`${target.hash}$`));
          await expect(h1(page, target.heading)).toBeFocused();
          await expect(bar(page).locator('[aria-current="page"]')).toHaveAccessibleName(target.label);
          await expect(more(page)).not.toHaveAttribute('aria-current');
        }

        for (const target of sheetPages) {
          await more(page).click();
          const link = sheet(page).getByRole('link', { name: target.label, exact: true });
          await expect(link).toHaveAttribute('href', target.hash);
          await link.click();
          await expect(sheet(page)).toHaveCount(0);
          await expect(page).toHaveURL(new RegExp(`${target.hash}$`));
          await expect(h1(page, target.heading)).toBeFocused();
          // Active state for a page inside More: the bar shows More as current,
          // and reopening the sheet marks the page itself.
          await expect(bar(page).locator('[aria-current="page"]')).toHaveAccessibleName('More');
          await more(page).click();
          await expect(sheet(page).locator('[aria-current="page"]')).toHaveText(target.label);
          await page.keyboard.press('Escape');
          await expect(sheet(page)).toHaveCount(0);
        }
      });
    });
  }

  test('Replication and Anomalies leave the sheet when their features are off', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.route('**/api/v1/settings', (route) => (route.request().method() === 'GET'
      ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ replication_enabled: 'false', anomaly_detection_enabled: 'false' }) })
      : route.fallback()));
    await page.goto('/#/');
    await more(page).click();
    await expect(sheetLinks(page)).toHaveText(['Storage', 'Recovery', 'Logs', 'Settings', 'Documentation']);
  });

  test.describe('the sheet', () => {
    test.beforeEach(async ({ page }) => {
      await setupVaultMockApi(page);
      await page.goto('/#/jobs');
      await expect(h1(page, 'Backup Jobs')).toBeVisible();
    });

    test('no hamburger remains', async ({ page }) => {
      await expect(page.getByRole('button', { name: 'Toggle menu' })).toHaveCount(0);
      await expect(page.locator('#mobile-menu')).toHaveCount(0);
      await expect(page.locator('header button')).toHaveCount(0);
      await expect(page.getByRole('banner').getByText('VAULT')).toBeVisible();
      await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeHidden();
    });

    test('More opens a sheet, not Settings', async ({ page }) => {
      await expect(more(page)).toHaveAttribute('aria-haspopup', 'dialog');
      await expect(more(page)).toHaveAttribute('aria-expanded', 'false');
      await expect(more(page)).not.toHaveAttribute('aria-controls');

      await more(page).tap();

      await expect(sheet(page)).toBeVisible();
      await expect(page).toHaveURL(/#\/jobs$/);
      await expect(h1(page, 'Backup Jobs')).toBeAttached();
      await expect(sheet(page)).toHaveJSProperty('open', true);
      await expect(page.locator('#more-sheet')).toHaveCount(1);
    });

    test('the sheet sits on the bottom edge, at full width, above the bar', async ({ page }) => {
      await openSheet(page);
      const panel = sheet(page).locator('> div');
      await expect(panel).toBeVisible();
      const box = await panel.boundingBox();
      expect(box).toMatchObject({ x: 0, width: 375 });
      expect(box!.y + box!.height).toBeCloseTo(812, 0);
    });

    test('entries are real links at least 44 px tall, and tall content scrolls', async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 320 });
      await openSheet(page);
      const links = sheetLinks(page);
      for (let i = 0; i < await links.count(); i++) {
        expect((await links.nth(i).boundingBox())!.height).toBeGreaterThanOrEqual(44);
      }
      const last = links.last();
      await last.scrollIntoViewIfNeeded();
      await expect(last).toBeInViewport({ ratio: 1 });
      const panel = await sheet(page).locator('> div').boundingBox();
      expect(panel!.height).toBeLessThanOrEqual(320);
    });

    test('focus moves to the first entry and stays inside', async ({ page }) => {
      await more(page).focus();
      await page.keyboard.press('Enter');
      const links = sheetLinks(page);
      await expect(links.first()).toBeFocused();
      await expect(more(page)).toHaveAttribute('aria-expanded', 'true');
      await expect(more(page)).toHaveAttribute('aria-controls', 'more-sheet');

      const close = sheet(page).getByRole('button', { name: 'Close' });
      await page.keyboard.press('Shift+Tab');
      await expect(close).toBeFocused();
      await page.keyboard.press('Shift+Tab');
      await expect(links.last()).toBeFocused();
      await page.keyboard.press('Tab');
      await expect(close).toBeFocused();
    });

    test('Escape closes it and returns focus to More', async ({ page }) => {
      await more(page).focus();
      await page.keyboard.press('Enter');
      await expect(sheet(page)).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(sheet(page)).toHaveCount(0);
      await expect(more(page)).toBeFocused();
      await expect(more(page)).toHaveAttribute('aria-expanded', 'false');
      await expect(page).toHaveURL(/#\/jobs$/);
    });

    test('a tap on the backdrop closes it, a tap on the panel does not', async ({ page }) => {
      await more(page).click();
      await sheet(page).getByRole('heading', { name: 'More' }).click();
      await expect(sheet(page)).toBeVisible();
      await page.touchscreen.tap(187, 20);
      await expect(sheet(page)).toHaveCount(0);
      await expect(more(page)).toBeFocused();
      await expect(page).toHaveURL(/#\/jobs$/);
    });

    test('the Close button closes it', async ({ page }) => {
      await more(page).click();
      await sheet(page).getByRole('button', { name: 'Close' }).click();
      await expect(sheet(page)).toHaveCount(0);
      await expect(more(page)).toBeFocused();
    });

    test('the current page link closes it, and the route stays', async ({ page }) => {
      await page.goto('/#/logs');
      await expect(h1(page, 'Logs')).toBeVisible();
      await more(page).click();
      await sheet(page).getByRole('link', { name: 'Logs', exact: true }).click();
      await expect(sheet(page)).toHaveCount(0);
      await expect(page).toHaveURL(/#\/logs$/);
    });

    test('any route change closes it and focuses the new heading', async ({ page }) => {
      await more(page).click();
      await page.evaluate(() => { location.hash = '#/storage'; });
      await expect(sheet(page)).toHaveCount(0);
      await expect(h1(page, 'Storage Destinations')).toBeFocused();
    });

    test('browser back closes it', async ({ page }) => {
      await bar(page).getByRole('link', { name: 'History', exact: true }).click();
      await expect(h1(page, 'Backup & Restore History')).toBeFocused();
      await more(page).click();
      await page.goBack();
      await expect(sheet(page)).toHaveCount(0);
      await expect(h1(page, 'Backup Jobs')).toBeFocused();
    });

    test('the page behind cannot be used while it is open', async ({ page }) => {
      const history = (await bar(page).getByRole('link', { name: 'History', exact: true }).boundingBox())!;
      await page.goto('/#/settings');
      await expect(h1(page, 'Settings')).toBeVisible();
      await more(page).click();
      await expect(sheet(page)).toBeVisible();
      // The bar is under the modal's top layer, and the page behind doesn't scroll.
      const covering = await page.evaluate(({ x, y }) => !!document.elementFromPoint(x, y)?.closest('dialog'), { x: history.x + 5, y: history.y + 5 });
      expect(covering).toBe(true);
      const scrollTop = () => page.locator('#main-content').evaluate((el) => el.scrollTop);
      const before = await scrollTop();
      await page.mouse.move(187, 20);
      await page.mouse.wheel(0, 400);
      await page.waitForTimeout(150);
      expect(await scrollTop()).toBe(before);
    });

    test('Documentation opens in a new tab and the sheet closes', async ({ page, context }) => {
      await context.route('https://tinkerologie.github.io/**', (route) => route.fulfill({ status: 200, body: 'docs' }));
      await more(page).click();
      const [tab] = await Promise.all([context.waitForEvent('page'), sheetLinks(page).last().click()]);
      await tab.close();
      await expect(sheet(page)).toHaveCount(0);
      await expect(page).toHaveURL(/#\/jobs$/);
    });
  });

  test('the recover wizard counts as part of Recovery', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/recovery');
    await page.getByRole('link', { name: 'Recover Vault' }).click();
    await expect(h1(page, 'Recover Vault')).toBeFocused();
    await expect(more(page)).toHaveAttribute('aria-current', 'page');
    await more(page).click();
    await expect(sheet(page).locator('[aria-current="page"]')).toHaveText('Recovery');
  });

  for (const style of ['default', '1bit', 'retro'] as const) {
    for (const { mode, replica } of MODES) {
      test(`bar fits the viewport: ${style} ${mode}`, async ({ page }) => {
        await seedTheme(page, style, 'dark');
        await setupVaultMockApi(page, { replica });
        await waitForPage(page, '#/', 'Dashboard');
        const items = bar(page).locator('a, button');
        await expect(items).toHaveCount(5);
        for (let i = 0; i < 5; i++) {
          const box = (await items.nth(i).boundingBox())!;
          expect(box.x).toBeGreaterThanOrEqual(0);
          expect(box.x + box.width).toBeLessThanOrEqual(375);
          expect(box.height).toBeGreaterThanOrEqual(44);
        }
      });
    }
  }
});

test.describe('desktop navigation is unchanged (#530)', () => {
  test('the sidebar is the only navigation, in its usual order', async ({ page }) => {
    await settleMotion(page);
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/logs');
    const sidebar = page.getByRole('navigation', { name: 'Main navigation' });
    await expect(sidebar.getByRole('link')).toHaveText(['Dashboard', 'Jobs', 'Storage', 'Restore', 'History', 'Recovery', 'Replication', 'Logs', 'Anomalies', 'Settings', 'Documentation']);
    await expect(sidebar.locator('[aria-current="page"]')).toHaveText('Logs');
    for (const section of ['Protect', 'Recover', 'System']) {
      await expect(sidebar.getByText(section, { exact: true })).toBeVisible();
    }
    await expect(bar(page)).toBeHidden();
    await expect(more(page)).toBeHidden();
    await expect(page.getByRole('banner')).toBeHidden();
    await sidebar.getByRole('link', { name: 'Settings' }).click();
    await expect(h1(page, 'Settings')).toBeFocused();
    await expect(page).toHaveURL(/#\/settings$/);
    await expect(sheet(page)).toHaveCount(0);
  });

  test('Logs keeps its heading beside the toolbar', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/logs');
    const heading = (await h1(page, 'Logs').boundingBox())!;
    const group = (await page.getByRole('group', { name: 'Log actions' }).boundingBox())!;
    expect(group.x).toBeGreaterThan(heading.x + heading.width);
    expect(group.y).toBeLessThan(heading.y + heading.height);
  });

  test('info buttons have no enlarged hit area with a mouse', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/settings');
    const info = page.getByRole('button', { name: 'More info' }).first();
    await expect(info).toBeVisible();
    expect(await info.evaluate((el) => getComputedStyle(el, '::after').content)).toBe('none');
    const box = (await info.boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(24);
    expect(box.height).toBeGreaterThanOrEqual(24);
  });
});

test.describe('target sizes at 375 px on touch (#530)', () => {
  test.use({ viewport: { width: 375, height: 812 }, hasTouch: true });

  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
  });

  test('touch emulation reports a coarse pointer', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/');
    expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
  });

  for (const { mode, replica } of MODES) {
    for (const { name, hash, heading, replica: onReplica } of ALL_PAGES) {
      if (replica && !onReplica) continue;
      test(`axe target-size: ${name} (${mode})`, async ({ page }) => {
        await setupVaultMockApi(page, { replica });
        await waitForPage(page, hash, heading);
        await expectAxeTargetSize(page, name);
      });
    }

    for (const tab of SETTINGS_TABS.slice(1)) {
      test(`axe target-size: Settings ${tab} (${mode})`, async ({ page }) => {
        await setupVaultMockApi(page, { replica });
        await waitForPage(page, '#/settings', 'Settings');
        await page.getByRole('tab', { name: tab, exact: true }).click();
        await expect(page.getByRole('tabpanel', { name: tab })).toBeVisible();
        await expectAxeTargetSize(page, `Settings ${tab}`);
      });
    }

    test(`axe target-size: open More sheet (${mode})`, async ({ page }) => {
      await setupVaultMockApi(page, { replica });
      await waitForPage(page, '#/', 'Dashboard');
      await more(page).click();
      await expect(sheet(page)).toBeVisible();
      await expectAxeTargetSize(page, 'More sheet');
    });
  }

  test('axe target-size: Restore plan file tree', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/restore');
    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    await page.locator('summary', { hasText: 'plex' }).click();
    await expect(page.getByRole('textbox', { name: 'Filter paths in plex' })).toBeVisible();
    await expectAxeTargetSize(page, 'Restore plan');

    const expand = page.getByRole('button', { name: /^Expand / }).first();
    const box = (await expand.boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(24);
    expect(box.height).toBeGreaterThanOrEqual(24);
    // The checkbox is a 24 px target itself, inside a focusable row, and using it
    // doesn't expand the row.
    const checkbox = page.getByRole('checkbox', { name: /^Select / }).first();
    const checkBox = (await checkbox.boundingBox())!;
    expect(checkBox.width).toBeGreaterThanOrEqual(24);
    expect(checkBox.height).toBeGreaterThanOrEqual(24);
    const row = page.getByRole('treeitem').first();
    const expanded = await row.getAttribute('aria-expanded');
    const wasChecked = await checkbox.isChecked();
    await checkbox.click();
    await expect(checkbox).toBeChecked({ checked: !wasChecked });
    expect(await row.getAttribute('aria-expanded')).toBe(expanded);
  });

  test('a Jobs selection checkbox has a 24 px label that selects without opening the job', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/jobs', 'Backup Jobs');
    const checkbox = page.getByRole('checkbox', { name: 'Select Docker & Appdata' });
    const label = checkbox.locator('xpath=ancestor::label');
    const box = (await label.boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(24);
    expect(box.height).toBeGreaterThanOrEqual(24);
    await label.click({ position: { x: 1, y: 1 } });
    await expect(checkbox).toBeChecked();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  /** Asserts a real touch anywhere in the 44 px square lands on the button. */
  async function expectInfoButtonHitAreas(page: Page, label: string) {
    const buttons = page.getByRole('button', { name: 'More info' });
    const total = await buttons.count();
    const misses: string[] = [];
    for (let i = 0; i < total; i++) {
      const miss = await buttons.nth(i).evaluate((el) => {
        el.scrollIntoView({ block: 'center', inline: 'nearest' });
        const r = el.getBoundingClientRect();
        const cx = r.left + r.width / 2;
        const cy = r.top + r.height / 2;
        const corners = [[-21, -21], [21, -21], [-21, 21], [21, 21], [0, -21], [0, 21], [-21, 0], [21, 0]];
        const blocked = corners.filter(([dx, dy]) => {
          const hit = document.elementFromPoint(cx + dx, cy + dy);
          return !(hit && (hit === el || el.contains(hit)));
        });
        return r.width < 24 || r.height < 24 || blocked.length ? `${Math.round(r.width)}x${Math.round(r.height)} at ${Math.round(cx)},${Math.round(cy)} blocked ${JSON.stringify(blocked)}` : null;
      });
      if (miss) misses.push(miss);
    }
    expect(misses, `${label}: info buttons without a 44 px hit area`).toEqual([]);
    return total;
  }

  for (const tab of SETTINGS_TABS.slice(0, 3)) {
    test(`measured info buttons: Settings ${tab}`, async ({ page }) => {
      await setupVaultMockApi(page);
      await waitForPage(page, '#/settings', 'Settings');
      await page.getByRole('tab', { name: tab, exact: true }).click();
      await expect(page.getByRole('tabpanel', { name: tab })).toBeVisible();
      expect(await expectInfoButtonHitAreas(page, `Settings ${tab}`)).toBeGreaterThan(0);
    });
  }

  test('measured info button: Recover wizard', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/recover', 'Recover Vault');
    expect(await expectInfoButtonHitAreas(page, 'Recover wizard')).toBeGreaterThan(0);
  });

  test('fixed Settings actions are at least 24 px tall', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/settings', 'Settings');
    const general = [
      page.getByRole('button', { name: 'Download diagnostics bundle' }),
      page.getByRole('link', { name: 'GitHub Repository' }),
      page.getByRole('link', { name: 'Sponsor' }),
      page.getByRole('button', { name: 'Add retry' }),
    ];
    for (const control of general) {
      expect((await control.boundingBox())!.height).toBeGreaterThanOrEqual(24);
    }
    await page.getByRole('tab', { name: 'Security', exact: true }).click();
    const key = page.getByRole('button', { name: 'Download vault.key' });
    await expect(key).toBeVisible();
    expect((await key.boundingBox())!.height).toBeGreaterThanOrEqual(24);
  });

  test('a tap outside the visible info icon opens its tooltip', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/settings', 'Settings');
    const info = page.getByRole('button', { name: 'More info' }).first();
    await info.evaluate((el) => el.scrollIntoView({ block: 'center' }));
    const box = (await info.boundingBox())!;
    await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2 - 20);
    await expect(page.getByRole('tooltip')).toBeVisible();
  });

  test('info buttons do not grow the rows they sit in', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/settings', 'Settings');
    const info = page.getByRole('button', { name: 'More info' }).first();
    const parentHeight = await info.evaluate((el) => {
      const row = el.closest('label, h2, h3, p, div') as HTMLElement;
      return { row: row.getBoundingClientRect().height, line: parseFloat(getComputedStyle(row).lineHeight) };
    });
    expect(parentHeight.row).toBeLessThanOrEqual(Math.ceil(parentHeight.line) + 1);
  });
});

test.describe('no horizontal overflow at 375 px (#530)', () => {
  test.use({ viewport: { width: 375, height: 812 } });

  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
  });

  for (const { mode, replica } of MODES) {
    for (const { name, hash, heading, replica: onReplica } of ALL_PAGES) {
      if (replica && !onReplica) continue;
      test(`${name} (${mode})`, async ({ page }) => {
        await setupVaultMockApi(page, { replica });
        await waitForPage(page, hash, heading);
        await expectNoOverflow(page, name);
      });
    }

    for (const tab of SETTINGS_TABS.slice(1)) {
      test(`Settings ${tab} (${mode})`, async ({ page }) => {
        await setupVaultMockApi(page, { replica });
        await waitForPage(page, '#/settings', 'Settings');
        await page.getByRole('tab', { name: tab, exact: true }).click();
        await expect(page.getByRole('tabpanel', { name: tab })).toBeVisible();
        await expectNoOverflow(page, `Settings ${tab}`);
      });
    }
  }
});

test.describe('Logs at 375 px (#530)', () => {
  test.use({ viewport: { width: 375, height: 812 } });

  for (const { mode, replica } of MODES) {
    test(`heading and toolbar are fully visible (${mode})`, async ({ page }) => {
      await setupVaultMockApi(page, { replica });
      await waitForPage(page, '#/logs', 'Logs');

      const group = page.getByRole('group', { name: 'Log actions' });
      const heading = (await h1(page, 'Logs').boundingBox())!;
      const groupBox = (await group.boundingBox())!;
      expect(groupBox.y).toBeGreaterThanOrEqual(heading.y + heading.height);

      const buttons = group.getByRole('button');
      await expect(buttons).toHaveCount(replica ? 5 : 6);
      const boxes = [];
      for (let i = 0; i < await buttons.count(); i++) boxes.push((await buttons.nth(i).boundingBox())!);
      for (const box of [heading, groupBox, ...boxes]) {
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(375);
      }
      for (let a = 0; a < boxes.length; a++) {
        for (let b = a + 1; b < boxes.length; b++) {
          const apart = boxes[a].x + boxes[a].width <= boxes[b].x || boxes[b].x + boxes[b].width <= boxes[a].x
            || boxes[a].y + boxes[a].height <= boxes[b].y || boxes[b].y + boxes[b].height <= boxes[a].y;
          expect(apart, `buttons ${a} and ${b} overlap`).toBe(true);
        }
      }
      expect(await group.evaluate((el) => el.scrollWidth - el.clientWidth)).toBe(0);
      await expectNoOverflow(page, 'Logs');
    });
  }

  test('the toolbar still works after wrapping', async ({ page }) => {
    await setupVaultMockApi(page);
    await waitForPage(page, '#/logs', 'Logs');
    await page.getByRole('button', { name: /wrap:/i }).click();
    await expect(page.getByRole('button', { name: 'Wrap: on' })).toHaveAttribute('aria-pressed', 'true');
  });
});
