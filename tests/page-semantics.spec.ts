import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Page headings and titles for every dispatched route (web/src/lib/routes.js).
const ROUTES = [
  { hash: '#/', heading: 'Dashboard' },
  { hash: '#/jobs', heading: 'Backup Jobs' },
  { hash: '#/storage', heading: 'Storage Destinations' },
  { hash: '#/history', heading: 'Backup & Restore History' },
  { hash: '#/restore', heading: 'Restore' },
  { hash: '#/logs', heading: 'Logs' },
  { hash: '#/replication', heading: 'Replication' },
  { hash: '#/recovery', heading: 'Recovery Guide' },
  { hash: '#/recover', heading: 'Recover Vault' },
  { hash: '#/anomalies', heading: 'Anomalies' },
  { hash: '#/settings', heading: 'Settings' },
];

const SIDEBAR = [
  ['Dashboard', '#/'],
  ['Jobs', '#/jobs'],
  ['Storage', '#/storage'],
  ['Restore', '#/restore'],
  ['History', '#/history'],
  ['Recovery', '#/recovery'],
  ['Logs', '#/logs'],
  ['Anomalies', '#/anomalies'],
  ['Settings', '#/settings'],
] as const;

const NEW_TAB = process.platform === 'darwin' ? 'Meta' : 'Control';
const INTERACTIVE = 'a, button, input, select, textarea, summary, [role="button"], [tabindex]';

const h1 = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true });
const sidebar = (page: Page) => page.getByRole('navigation', { name: 'Main navigation' });

async function dashboard(page: Page) {
  await page.goto('/#/');
  await expect(page.locator('.dash-tile').first()).toBeVisible();
}

// Every catalog tile, so the checks below don't depend on the default layout.
async function addEveryTile(page: Page) {
  await page.getByRole('button', { name: 'Customize' }).click();
  const add = page.locator('.dash-addpanel button:not([disabled])');
  while (await add.count()) {
    const before = await add.count();
    await add.first().click();
    await expect(add).toHaveCount(before - 1);
  }
}

test.describe('Page semantics (#529)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test.describe('titles', () => {
    test('every route has a unique title naming its page', async ({ page }) => {
      const seen = new Set<string>();
      for (const { hash, heading } of ROUTES) {
        await page.goto(`/${hash}`);
        await expect(page).toHaveTitle(`${heading} – Vault`);
        await expect(h1(page, heading)).toBeVisible();
        seen.add(await page.title());
      }
      expect(seen.size).toBe(ROUTES.length);
    });

    test('an unknown route renders Dashboard and titles it so', async ({ page }) => {
      await page.goto('/#/nope');
      await expect(h1(page, 'Dashboard')).toBeVisible();
      await expect(page).toHaveTitle('Dashboard – Vault');
    });
  });

  test.describe('navigation links', () => {
    test('sidebar items are links with real hrefs and aria-current', async ({ page }) => {
      await page.goto('/#/jobs');
      for (const [name, href] of SIDEBAR) {
        await expect(sidebar(page).getByRole('link', { name, exact: true })).toHaveAttribute('href', href);
      }
      await expect(sidebar(page).getByRole('button')).toHaveCount(0);
      await expect(sidebar(page).locator('[aria-current="page"]')).toHaveText('Jobs');
    });

    test('a modifier-click opens a sidebar link in a new tab', async ({ page, context }) => {
      await page.goto('/#/jobs');
      const [popup] = await Promise.all([
        context.waitForEvent('page'),
        sidebar(page).getByRole('link', { name: 'Storage' }).click({ modifiers: [NEW_TAB] }),
      ]);
      await setupVaultMockApi(popup);
      await popup.reload();
      await expect(popup).toHaveURL(/#\/storage$/);
      await expect(popup).toHaveTitle('Storage Destinations – Vault');
      await expect(page).toHaveURL(/#\/jobs$/);
    });

    test('the update indicator is a link to Settings', async ({ page }) => {
      await page.route('**/*', async (route) => {
        if (/\/releases?\/latest|latest-release|release/.test(new URL(route.request().url()).pathname)) {
          return route.fulfill({ json: { tag: 'v2099.01.01' } });
        }
        return route.fallback();
      });
      await page.goto('/#/jobs');
      const update = page.getByRole('link', { name: /Update available/ });
      await expect(update).toHaveAttribute('href', '#/settings');
    });
  });

  test.describe('dashboard tiles', () => {
    const TILES: [RegExp, string][] = [
      [/^Health score/, '#/history'],
      [/^Next run/, '#/jobs'],
      [/^Last backup/, '#/history'],
      [/^Storage used/, '#/storage'],
      [/^Storage per target/, '#/storage'],
      [/^Recovery readiness/, '#/recovery'],
      [/^Needs attention/, '#/history'],
      [/^Success rate/, '#/history'],
      [/^Anomalies/, '#/anomalies'],
      [/^Backup size trend/, '#/history'],
      [/^Backup calendar/, '#/history'],
      [/^Retention & cleanup/, '#/jobs'],
    ];

    test('navigation tiles are links with the right destination', async ({ page }) => {
      await dashboard(page);
      await addEveryTile(page);
      await page.getByRole('button', { name: 'Done' }).click();
      const grid = page.locator('.dash-tile-grid');
      for (const [name, href] of TILES) {
        await expect(grid.getByRole('link', { name })).toHaveAttribute('href', href);
      }
      await expect(page.locator('main [role="button"]')).toHaveCount(0);
    });

    test('a tile link navigates and opens in a new tab', async ({ page, context }) => {
      await dashboard(page);
      const tile = page.locator('.dash-tile-grid').getByRole('link', { name: /^Last backup/ });
      const [popup] = await Promise.all([context.waitForEvent('page'), tile.click({ modifiers: [NEW_TAB] })]);
      await expect(popup).toHaveURL(/#\/history$/);
      await expect(popup).toHaveTitle('Backup & Restore History – Vault');
      await expect(page).toHaveURL(/#\/$/);
      await tile.focus();
      await page.keyboard.press('Enter');
      await expect(h1(page, 'Backup & Restore History')).toBeFocused();
    });

    test('no interactive element is nested in a tile link, normally or in Customize mode', async ({ page }) => {
      await dashboard(page);
      await addEveryTile(page);
      const nested = page.locator(`main a[href] :is(${INTERACTIVE})`);
      const tiles = page.locator('.dash-tile a[href]');
      await expect(nested).toHaveCount(0);
      // Customize mode: the contents are inert, so the check also proves the DOM shape.
      expect(await page.locator('.dash-tile > [inert]').count()).toBeGreaterThan(0);

      await page.getByRole('button', { name: 'Done' }).click();
      await expect(page.locator('.dash-tile > [inert]')).toHaveCount(0);
      expect(await tiles.count()).toBeGreaterThanOrEqual(12);
      await expect(nested).toHaveCount(0);
    });

    test('Customize mode keeps Tab out of tile contents and leaves the toolbar usable', async ({ page }) => {
      await dashboard(page);
      await page.getByRole('button', { name: 'Customize' }).click();
      await expect(page.getByRole('button', { name: 'Done' })).toBeVisible();
      await page.getByRole('button', { name: 'Done' }).focus();
      const reached = new Set<string>();
      for (let i = 0; i < 40; i++) {
        await page.keyboard.press('Tab');
        const stop = await page.evaluate(() => {
          const el = document.activeElement;
          return { inert: !!el?.closest('[inert]'), name: el?.getAttribute('aria-label') ?? '' };
        });
        expect(stop.inert).toBe(false);
        reached.add(stop.name);
      }
      expect(reached).toContain('Remove Health score');
      await page.locator('.dash-tile').first().click({ position: { x: 20, y: 60 } });
      await expect(page).toHaveURL(/#\/$/);
    });

    test('the 3-2-1 goal and dismiss buttons never toggle the panel', async ({ page }) => {
      await dashboard(page);
      const toggle = page.getByRole('button', { name: /^3-2-1 Backup Rule/ });
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      expect(await toggle.locator(INTERACTIVE).count()).toBe(0);

      const goal = page.getByRole('button', { name: 'Full 3-2-1' });
      await goal.focus();
      await page.keyboard.press('Enter');
      await expect(goal).toHaveAttribute('aria-pressed', 'true');
      await page.keyboard.press('Space');
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');

      await toggle.focus();
      await page.keyboard.press('Enter');
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
      await page.keyboard.press('Space');
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');

      // The whole header row still toggles with the mouse, away from the controls.
      const box = (await toggle.boundingBox())!;
      await page.mouse.click(box.x + box.width + 40, box.y + box.height / 2);
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');

      await page.getByRole('button', { name: 'Hide 3-2-1 Backup Rule' }).click();
      await expect(toggle).toHaveCount(0);
    });
  });

  test.describe('focus after navigation', () => {
    test('every route focuses its heading after a client-side change', async ({ page }) => {
      await page.goto('/#/logs');
      await expect(h1(page, 'Logs')).toBeVisible();
      for (const { hash, heading } of ROUTES.filter((r) => r.hash !== '#/logs')) {
        await page.evaluate((h) => { location.hash = h; }, hash);
        await expect(h1(page, heading)).toBeFocused();
        await expect(page).toHaveTitle(`${heading} – Vault`);
      }
    });

    test('mouse and keyboard activation both focus the heading, on the page scrolled to the top', async ({ page }) => {
      await page.setViewportSize({ width: 1280, height: 400 });
      await page.goto('/#/settings');
      await expect(page.getByRole('tab', { name: 'General' })).toBeVisible();
      await page.locator('#main-content').evaluate((el) => { el.scrollTop = 300; });

      await sidebar(page).getByRole('link', { name: 'Jobs' }).click();
      await expect(h1(page, 'Backup Jobs')).toBeFocused();
      await expect(h1(page, 'Backup Jobs')).toBeInViewport();
      expect(await page.locator('#main-content').evaluate((el) => el.scrollTop)).toBe(0);

      await sidebar(page).getByRole('link', { name: 'Storage' }).focus();
      await page.keyboard.press('Enter');
      await expect(h1(page, 'Storage Destinations')).toBeFocused();
    });

    test('a deep link keeps focus at the start of the page, then navigation moves it', async ({ page }) => {
      await page.goto('/#/settings');
      await expect(h1(page, 'Settings')).toBeVisible();
      await expect(page).toHaveTitle('Settings – Vault');
      await expect(h1(page, 'Settings')).not.toBeFocused();
      await page.keyboard.press('Tab');
      await expect(page.getByRole('link', { name: /skip/i })).toBeFocused();

      await sidebar(page).getByRole('link', { name: 'Logs' }).click();
      await expect(h1(page, 'Logs')).toBeFocused();
    });

    test('a deep link with a query reaches the page, and a query-only change keeps focus', async ({ page }) => {
      await page.goto('/#/restore?job=1');
      await expect(h1(page, 'Restore')).toBeVisible();
      await expect(page).toHaveTitle('Restore – Vault');
      await page.evaluate(() => { location.hash = '#/restore?job=2'; });
      await expect(page).toHaveURL(/job=2/);
      await expect(h1(page, 'Restore')).not.toBeFocused();
    });

    test('back and forward focus the heading and restore the title', async ({ page }) => {
      await page.goto('/#/jobs');
      await sidebar(page).getByRole('link', { name: 'Storage' }).click();
      await expect(h1(page, 'Storage Destinations')).toBeFocused();
      await sidebar(page).getByRole('link', { name: 'Logs' }).click();
      await expect(h1(page, 'Logs')).toBeFocused();

      await page.goBack();
      await expect(h1(page, 'Storage Destinations')).toBeFocused();
      await expect(page).toHaveTitle('Storage Destinations – Vault');
      await page.goBack();
      await expect(h1(page, 'Backup Jobs')).toBeFocused();
      await page.goForward();
      await expect(h1(page, 'Storage Destinations')).toBeFocused();
    });

    test('a guard redirect lands on Dashboard with its title', async ({ page }) => {
      await setupVaultMockApi(page, { replica: true });
      await page.goto('/#/jobs');
      await expect(h1(page, 'Dashboard')).toBeVisible();
      await expect(page).toHaveURL(/#\/$/);
      await expect(page).toHaveTitle('Dashboard – Vault');
    });

    test('the skip link focuses the heading without changing the route', async ({ page }) => {
      await page.goto('/#/jobs');
      await expect(h1(page, 'Backup Jobs')).toBeVisible();
      await page.keyboard.press('Tab');
      await page.keyboard.press('Enter');
      await expect(h1(page, 'Backup Jobs')).toBeFocused();
      await expect(page).toHaveURL(/#\/jobs$/);
      await expect(page).toHaveTitle('Backup Jobs – Vault');
    });

    test('the restore wizard still moves focus to its step heading', async ({ page }) => {
      await page.goto('/#/jobs');
      await sidebar(page).getByRole('link', { name: 'Restore' }).click();
      await expect(h1(page, 'Restore')).toBeFocused();
      await page.getByRole('button', { name: /plex/i }).click();
      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await expect(page.locator('[data-step-heading]:focus')).toBeVisible();
    });
  });

  test.describe('Settings tabs', () => {
    const tab = (page: Page, name: string) => page.getByRole('tab', { name, exact: true });

    test('expose tablist, tab and tabpanel roles with roving tabindex', async ({ page }) => {
      await page.goto('/#/settings');
      await expect(page.getByRole('tablist', { name: 'Settings sections' })).toBeVisible();
      await expect(page.getByRole('tab')).toHaveCount(4);
      await expect(tab(page, 'General')).toHaveAttribute('aria-selected', 'true');
      await expect(tab(page, 'General')).toHaveAttribute('tabindex', '0');
      await expect(tab(page, 'General')).toHaveAttribute('aria-controls', 'settings-panel-general');
      await expect(tab(page, 'Security')).toHaveAttribute('aria-selected', 'false');
      await expect(tab(page, 'Security')).toHaveAttribute('tabindex', '-1');
      await expect(tab(page, 'Security')).not.toHaveAttribute('aria-controls');
      await expect(page.getByRole('tabpanel', { name: 'General' })).toHaveAttribute('id', 'settings-panel-general');
    });

    test('arrows, Home and End select and focus, and wrap at both ends', async ({ page }) => {
      await page.goto('/#/settings');
      await tab(page, 'General').focus();
      const names = await page.getByRole('tab').allInnerTexts();
      const expectOn = async (name: string) => {
        await expect(tab(page, name)).toBeFocused();
        await expect(tab(page, name)).toHaveAttribute('aria-selected', 'true');
        await expect(tab(page, name)).toHaveAttribute('tabindex', '0');
        await expect(page.getByRole('tabpanel', { name })).toBeVisible();
      };
      for (const name of names.slice(1)) {
        await page.keyboard.press('ArrowRight');
        await expectOn(name);
      }
      await page.keyboard.press('ArrowRight');
      await expectOn(names[0]);
      await page.keyboard.press('ArrowLeft');
      await expectOn(names[names.length - 1]);
      await page.keyboard.press('Home');
      await expectOn(names[0]);
      await page.keyboard.press('End');
      await expectOn(names[names.length - 1]);
      await expect(page.getByRole('tab', { selected: true })).toHaveCount(1);
    });

    test('Tab moves from the selected tab into the panel', async ({ page }) => {
      await page.goto('/#/settings');
      await tab(page, 'General').focus();
      await page.keyboard.press('ArrowRight');
      await page.keyboard.press('Tab');
      await expect(page.getByRole('tabpanel')).toBeFocused();
    });

    test('a click selects, and the section jump still works', async ({ page }) => {
      await page.goto('/#/settings');
      await tab(page, 'Reference').click();
      await expect(tab(page, 'Reference')).toHaveAttribute('aria-selected', 'true');
      await tab(page, 'General').click();
      await page.getByRole('button', { name: 'History Retention' }).click();
      await expect(page.locator('#set-history')).toBeFocused();
    });
  });

  test.describe('landmarks', () => {
    test('desktop navigation is named and main content is a landmark', async ({ page }) => {
      await page.goto('/#/jobs');
      await expect(page.getByRole('navigation', { name: 'Main navigation' })).toHaveCount(1);
      await expect(page.locator('nav:not([aria-label])')).toHaveCount(0);
      await expect(page.getByRole('main')).toBeVisible();
    });
  });

  test.describe('mobile at 375 px', () => {
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 812 });
    });

    test('bottom navigation items are links', async ({ page }) => {
      await page.goto('/#/jobs');
      const bottom = page.getByRole('navigation', { name: 'Mobile navigation' });
      for (const [name, href] of [['Home', '#/'], ['Jobs', '#/jobs'], ['History', '#/history'], ['Restore', '#/restore']]) {
        await expect(bottom.getByRole('link', { name, exact: true })).toHaveAttribute('href', href);
      }
      await expect(bottom.locator('[aria-current="page"]')).toHaveAccessibleName('Jobs');
      await bottom.getByRole('link', { name: 'History' }).click();
      await expect(h1(page, 'Backup & Restore History')).toBeFocused();
    });

    test('tile links stay usable', async ({ page }) => {
      await dashboard(page);
      await page.locator('.dash-tile-grid').getByRole('link', { name: /^Last backup/ }).click();
      await expect(h1(page, 'Backup & Restore History')).toBeFocused();
    });
  });
});
