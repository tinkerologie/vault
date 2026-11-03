import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { settleMotion } from './helpers/contrast';
import { WIDGETS, tabWalk } from './helpers/keyboard-walk';
import { PRESENTATIONS, ROUTES as EVERY_PAGE, VIEWPORTS, openPresentation } from './helpers/presentation';

// Page headings and titles for every dispatched route (web/src/lib/routes.js).
const ROUTES = [
  { hash: '#/', heading: 'Dashboard' },
  { hash: '#/jobs', heading: 'Backup Jobs' },
  { hash: '#/storage', heading: 'Storage Destinations' },
  { hash: '#/history', heading: 'Backup & Restore History' },
  { hash: '#/restore', heading: 'Restore' },
  { hash: '#/logs', heading: 'Logs' },
  { hash: '#/replication', heading: 'Replication' },
  { hash: '#/recovery', heading: 'Disaster Recovery' },
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
  ['Disaster Recovery', '#/recovery'],
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

// The automated half of the #529 screen-reader pass: every routed page, in both
// presentations, at desktop and 375 px. Tab from the top of the document to the
// end and check what each stop would announce.
test.describe('keyboard walkthrough on every page (#529)', () => {
  test('the walk flags a control Tab skips, but not a roving group member or hidden content', async ({ page }) => {
    await page.setContent(`<a href="#top">Skip</a>
      <div role="tablist" aria-label="Sections">
        <button role="tab" aria-selected="true" tabindex="0">One</button>
        <button role="tab" aria-selected="false" tabindex="-1">Two</button>
      </div>
      <label><input type="radio" name="mode" checked> Light</label>
      <label><input type="radio" name="mode"> Dark</label>
      <button tabindex="-1">Stranded</button>
      <div aria-hidden="true"><button tabindex="-1">Decorative</button></div>`);
    const { unreached } = await tabWalk(page, page.mainFrame());
    expect(unreached).toEqual(['<button tabindex="-1">Stranded</button>']);
  });

  for (const presentation of PRESENTATIONS) {
    for (const viewport of VIEWPORTS) {
      test.describe(`${presentation} at ${viewport.name}`, () => {
        test.use({ viewport: { width: viewport.width, height: viewport.height } });

        for (const route of EVERY_PAGE) {
          test(`${route.name}: every control is reachable, named, visibly focused and not nested`, async ({ page }) => {
            await settleMotion(page);
            await setupVaultMockApi(page);
            const frame = await openPresentation(page, presentation, route.hash);
            await expect(frame.locator(route.ready).first()).toBeVisible();
            await expect(frame.getByRole('heading', { level: 1, name: route.heading, exact: true })).toBeVisible();
            expect(await frame.title()).toBe(`${route.heading} – Vault`);

            const { stops, unreached } = await tabWalk(page, frame);
            expect(stops.length, 'Tab reaches controls').toBeGreaterThan(1);
            expect(stops[0].announced, 'the skip link is the first stop').toBe('link "Skip to main content"');
            expect(stops.filter((s) => !s.named).map((s) => s.announced), 'unnamed stops').toEqual([]);
            expect(stops.filter((s) => !s.visibleFocus).map((s) => s.announced), 'stops without a visible focus indicator').toEqual([]);
            expect(stops.filter((s) => s.nestedIn).map((s) => `${s.announced} in ${s.nestedIn}`), 'nested controls').toEqual([]);
            expect(unreached, 'controls Tab never reaches').toEqual([]);
          });
        }
      });
    }
  }
});

// Defects the walkthrough found (#529): state a screen reader couldn't hear,
// names that changed at 375 px, a control nested in a role="button" row, and
// navigation done with buttons.
test.describe('screen-reader state and names (#529)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('Jobs: the status filter says which choice is on', async ({ page }) => {
    await page.goto('/#/jobs');
    const group = page.getByRole('group', { name: 'Filter by status' });
    await expect(group.getByRole('button', { name: 'All', pressed: true })).toBeVisible();
    await group.getByRole('button', { name: 'Disabled' }).click();
    await expect(group.getByRole('button', { name: 'Disabled', pressed: true })).toBeVisible();
    await expect(group.getByRole('button', { pressed: true })).toHaveCount(1);
  });

  test('Jobs: each enable switch names its job, so "on" means enabled', async ({ page }) => {
    await page.goto('/#/jobs');
    await expect(page.getByRole('switch', { name: 'Enable Docker & Appdata', checked: true })).toBeVisible();
    await expect(page.getByRole('switch', { name: 'Enable VM Backups', checked: true })).toBeVisible();
    await expect(page.getByRole('switch', { name: /Disable/ })).toHaveCount(0);
    // No hover title or description that contradicts the state, on or off.
    const docker = page.getByRole('switch', { name: 'Enable Docker & Appdata' });
    await expect(docker).not.toHaveAttribute('title');
    await expect(docker).toHaveAccessibleDescription('');
    await docker.click();
    await expect(docker).toHaveAttribute('aria-checked', 'false');
    await expect(docker).not.toHaveAttribute('title');
    await expect(docker).toHaveAccessibleDescription('');
  });

  for (const viewport of VIEWPORTS) {
    test(`Restore at ${viewport.name}: steps keep their names and mark the current one`, async ({ page }) => {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await page.goto('/#/restore');
      const step1 = page.getByRole('button', { name: '1 Select Items' });
      await expect(step1).toHaveAttribute('aria-current', 'step');
      await expect(page.getByRole('button', { name: '2 Choose Version' })).not.toHaveAttribute('aria-current');
      await expect(page.getByRole('button', { name: '3 Restore' })).toBeAttached();
    });
  }

  test('Restore: the type filter and the item cards say what is selected', async ({ page }) => {
    await page.goto('/#/restore');
    const types = page.getByRole('group', { name: 'Filter by type' });
    await expect(types.getByRole('button', { name: 'All', pressed: true })).toBeVisible();
    await types.getByRole('button', { name: 'VMs' }).click();
    await expect(types.getByRole('button', { name: 'VMs', pressed: true })).toBeVisible();
    await types.getByRole('button', { name: 'All' }).click();

    const plex = page.getByRole('button', { name: /^plex / });
    await expect(plex).toHaveAttribute('aria-pressed', 'false');
    await plex.focus();
    await page.keyboard.press('Space');
    await expect(plex).toHaveAttribute('aria-pressed', 'true');
    await page.keyboard.press('Enter');
    await expect(plex).toHaveAttribute('aria-pressed', 'false');
  });

  test('History: a run with details is a disclosure button, and its anomaly badge is a sibling', async ({ page }) => {
    await page.route('**/api/v1/anomalies*', (route) => route.fulfill({ json: { anomalies: [{
      id: 7, scope_kind: 'job', scope_id: 1, job_run_id: 901, summary: 'Backup size spike above baseline',
      severity: 'warning', state: 'open', detected_at: '2026-10-01T02:20:00Z',
    }] } }));
    await page.goto('/#/history');
    const run = page.getByRole('button', { name: 'Docker & Appdata completed', exact: true });
    await expect(run).toHaveAttribute('aria-expanded', 'false');
    await expect(page.locator('main [role="button"]')).toHaveCount(0);
    expect(await run.locator(WIDGETS).count()).toBe(0);

    await run.focus();
    await page.keyboard.press('Enter');
    await expect(run).toHaveAttribute('aria-expanded', 'true');
    const details = page.locator(`#${await run.getAttribute('aria-controls')}`);
    await expect(details).toContainText('Deduplication savings 53%');
    await page.keyboard.press('Space');
    await expect(run).toHaveAttribute('aria-expanded', 'false');

    // The badge's tooltip button opens its tooltip without toggling the run.
    const card = page.locator('main .rounded-xl', { has: run });
    const badge = card.getByRole('button', { name: /warning/i });
    await badge.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('tooltip')).toBeVisible();
    await expect(run).toHaveAttribute('aria-expanded', 'false');

    // A click anywhere on the summary row still toggles it.
    const box = (await card.boundingBox())!;
    await page.mouse.click(box.x + box.width - 40, box.y + 30);
    await expect(run).toHaveAttribute('aria-expanded', 'true');
  });

  test('Disaster Recovery: the ways into the wizard and to Jobs are links', async ({ page, context }) => {
    await page.goto('/#/recovery');
    const recover = page.getByRole('link', { name: 'Recover Vault' });
    await expect(recover).toHaveAttribute('href', '#/recover');
    await expect(page.getByRole('link', { name: 'Configure backup jobs →' })).toHaveAttribute('href', '#/jobs');
    const [popup] = await Promise.all([context.waitForEvent('page'), recover.click({ modifiers: [NEW_TAB] })]);
    await expect(popup).toHaveURL(/#\/recover$/);
    await recover.click();
    await expect(h1(page, 'Recover Vault')).toBeFocused();
  });
});

test.describe('embedded presentation (#529)', () => {
  for (const viewport of VIEWPORTS) {
    test(`at ${viewport.name}: every route has its own title and focuses its heading`, async ({ page }) => {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await setupVaultMockApi(page);
      const frame = await openPresentation(page, 'embedded', '#/logs');
      const titles = new Set<string>();
      for (const { hash, heading } of EVERY_PAGE.filter((r) => r.hash !== '#/logs')) {
        await frame.evaluate((h) => { location.hash = h; }, hash);
        await expect(frame.getByRole('heading', { level: 1, name: heading, exact: true })).toBeFocused();
        expect(await frame.title()).toBe(`${heading} – Vault`);
        titles.add(await frame.title());
      }
      expect(titles.size).toBe(EVERY_PAGE.length - 1);
    });
  }
});
