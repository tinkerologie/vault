import path from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { test, expect, type FrameLocator, type Locator, type Page, type WebSocketRoute } from '@playwright/test';
import { setupVaultMockApi, mockJob1Points } from './mocks/vault-api';

// The embedded Backups view (#526), through a real iframe. The harness stands in
// for Unraid's page: a tall header above the frame, a floating scroll arrow, and
// the real embed-host.js that Backups.page loads. Only the config app.php would
// inject is faked; plugin tests in scripts/ cover that side.
const EMBED_HOST = path.join(process.cwd(), 'plugin/pages/include/embed-host.js');
const HARNESS = '/__unraid_page__.html';
const HEADER_HEIGHT = 300;
const ARROW_CLEARANCE = 72;

const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 800 },
  { name: '375 px', width: 375, height: 667 },
];

const REPLICATION_SOURCES = [{ id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 }];

const PAGES = [
  { label: 'Jobs', hash: '#/jobs', heading: 'Backup Jobs', daemonOnly: true },
  { label: 'Storage', hash: '#/storage', heading: 'Storage Destinations' },
  { label: 'Restore', hash: '#/restore', heading: 'Restore', daemonOnly: true },
  { label: 'History', hash: '#/history', heading: 'Backup & Restore History' },
  { label: 'Recovery', hash: '#/recovery', heading: 'Recovery Guide', daemonOnly: true },
  { label: 'Replication', hash: '#/replication', heading: 'Replication' },
  { label: 'Logs', hash: '#/logs', heading: 'Logs' },
  { label: 'Anomalies', hash: '#/anomalies', heading: 'Anomalies' },
  { label: 'Settings', hash: '#/settings', heading: 'Settings' },
  { label: 'Dashboard', hash: '#/', heading: 'Dashboard' },
];

const harnessHtml = (frameSrc: string) => `<!doctype html><html><head><title>Unraid</title>
<style>body{margin:0}#header{height:${HEADER_HEIGHT}px;background:#333}#arrow{position:fixed;right:16px;bottom:16px;width:40px;height:40px;background:#c00}</style>
</head><body>
<div id="header"></div>
<div id="arrow"></div>
<iframe id="vault-backups-frame" src="${frameSrc}" style="width:100%;border:0;display:block" title="Vault Backups"></iframe>
<script src="/plugins/vault/include/embed-host.js"></script>
<script>VaultEmbedHost.attach(document.getElementById('vault-backups-frame'));</script>
</body></html>`;

type EmbedOptions = {
  /** Adds routes that must win over the mock API, before the first request. */
  beforeLoad?: () => Promise<unknown>;
  hostTheme?: string | null;
  stored?: Record<string, string>;
  hash?: string;
  replica?: boolean;
  /** Serves the frame from another origin than the parent. */
  crossOrigin?: boolean;
};

async function openEmbedded(page: Page, options: EmbedOptions = {}): Promise<FrameLocator> {
  const { hostTheme = null, stored = {}, hash = '#/', replica = false, crossOrigin = false } = options;
  await setupVaultMockApi(page, { replica, replicationSources: REPLICATION_SOURCES });
  await options.beforeLoad?.();
  await page.addInitScript(({ hostTheme, stored }) => {
    if (window.self !== window.top) {
      (window as any).__VAULT_RUNTIME_CONFIG__ = { presentation: 'embedded', hostTheme };
    }
    if (!sessionStorage.getItem('seeded')) {
      sessionStorage.setItem('seeded', '1');
      for (const [key, value] of Object.entries(stored)) localStorage.setItem(key, value);
    }
  }, { hostTheme, stored });
  const frameOrigin = 'http://vault.example.test';
  if (crossOrigin) {
    // The other origin's documents and assets come from the real preview server.
    await page.route(`${frameOrigin}/**`, async (route) => {
      const { pathname, search } = new URL(route.request().url());
      if (pathname.startsWith('/api/')) return route.fallback();
      await route.fulfill({ response: await route.fetch({ url: `http://127.0.0.1:4173${pathname}${search}` }) });
    });
  }
  const frameSrc = `${crossOrigin ? frameOrigin : ''}/?embed=1${hash}`;
  await page.route(`**${HARNESS}`, (route) => route.fulfill({ contentType: 'text/html', body: harnessHtml(frameSrc) }));
  await page.route('**/plugins/vault/include/embed-host.js', (route) => route.fulfill({ contentType: 'text/javascript', path: EMBED_HOST }));
  await page.goto(HARNESS);
  return page.frameLocator('#vault-backups-frame');
}

const h1 = (frame: FrameLocator, name: string) => frame.getByRole('heading', { level: 1, name, exact: true });
const linkRow = (frame: FrameLocator) => frame.getByRole('navigation', { name: 'Main navigation' });
const childWindow = (page: Page) => page.frames().find((f) => f !== page.mainFrame())!;
const frameHeight = (page: Page) => page.locator('#vault-backups-frame').evaluate((el) => el.getBoundingClientRect().height);
const appHeight = (page: Page) => childWindow(page).evaluate(() => document.getElementById('app')!.offsetHeight);
const floorHeight = (page: Page) => page.evaluate((top) => Math.max(480, window.innerHeight - top - 24), HEADER_HEIGHT);

/** Waits for the frame to match its content, and returns that height. */
async function settledFrameHeight(page: Page) {
  await expect.poll(async () => Math.abs((await frameHeight(page)) - Math.max(await floorHeight(page), await appHeight(page)))).toBeLessThanOrEqual(1);
  return frameHeight(page);
}

/** Once the frame has caught up, nothing inside it scrolls at page level. */
async function expectNoNestedScroll(page: Page) {
  const spare = () => childWindow(page).evaluate(() => {
    const root = document.documentElement;
    const main = document.getElementById('main-content')!;
    return Math.max(root.scrollHeight - window.innerHeight, main.scrollHeight - main.clientHeight);
  });
  await expect.poll(spare).toBeLessThanOrEqual(1);
  const state = await childWindow(page).evaluate(() => ({
    sideways: document.documentElement.scrollWidth - window.innerWidth,
    mainOverflow: getComputedStyle(document.getElementById('main-content')!).overflowY,
  }));
  expect(state.sideways).toBeLessThanOrEqual(0);
  expect(state.mainOverflow).toBe('visible');
}

/** The box is on the parent's screen and clear of the arrow's band. */
async function expectInParentViewport(page: Page, box: { y: number; height: number } | null) {
  expect(box).not.toBeNull();
  const viewport = page.viewportSize()!;
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport.height - ARROW_CLEARANCE + 1);
}

// A history long enough that the render window (#507) hides older rows, each
// line with a key=value tail so it has a details toggle.
const LOG_COUNT = 400;
const logRow = (id: number) => ({
  id, level: 'info', category: 'backup', message: `Embedded entry ${id}, seq=${id}`, details: null,
  created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + id * 1000).toISOString(),
});

/** Embedded Logs over LOG_COUNT lines, with a socket that can push new ones. */
async function openEmbeddedLogs(page: Page) {
  const all = Array.from({ length: LOG_COUNT }, (_, i) => logRow(LOG_COUNT - i));
  let socket: WebSocketRoute | null = null;
  const frame = await openEmbedded(page, {
    hash: '#/logs',
    beforeLoad: async () => {
      await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
      await page.route('**/api/v1/activity*', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const url = new URL(route.request().url());
        const beforeId = parseInt(url.searchParams.get('before_id') || '0', 10);
        const limit = parseInt(url.searchParams.get('limit') || '30', 10);
        await route.fulfill({ json: all.filter((r) => !beforeId || r.id < beforeId).slice(0, limit) });
      });
    },
  });
  const log = frame.getByRole('log');
  await expect(log.locator(`[data-entry-id="${LOG_COUNT}"]`)).toBeVisible();
  // One date separator and 199 lines: the window is full and older rows wait below it.
  await expect.poll(() => log.locator('[data-entry-id]').count(), { timeout: 15000 }).toBe(199);
  await expect.poll(() => socket !== null).toBe(true);
  await settledFrameHeight(page);
  return {
    frame,
    log,
    send: (id: number) => socket!.send(JSON.stringify({ type: 'activity', entry: logRow(id) })),
  };
}

const parentScrollY = (page: Page) => page.evaluate(() => window.scrollY);
const scrollParentBy = (page: Page, dy: number) => page.evaluate((d) => window.scrollBy(0, d), dy);
/** A real click where the control is: Locator.click() may scroll first. */
async function clickInPlace(page: Page, target: Locator) {
  const at = (await target.boundingBox())!;
  await page.mouse.click(at.x + at.width / 2, at.y + at.height / 2);
}
/** Elements in the frame that scroll their own overflow. */
const ownScrollers = (page: Page) => childWindow(page).evaluate(() => [...document.querySelectorAll('#app *')]
  .filter((el) => /auto|scroll/.test(getComputedStyle(el).overflowY) && el.scrollHeight > el.clientHeight + 1)
  .map((el) => el.getAttribute('role') || el.className));
/** The first log line at or below the top of the parent's screen. */
const firstLineOnScreen = (page: Page) => childWindow(page).evaluate(() => {
  const top = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--vault-visible-top')) || 0;
  const row = [...document.querySelectorAll<HTMLElement>('[role="log"] [data-entry-id]')].find((r) => r.getBoundingClientRect().top >= top + 40)!;
  return { id: row.dataset.entryId!, top: row.getBoundingClientRect().top };
});
const lineTop = (page: Page, id: string) => childWindow(page).evaluate((i) => document.querySelector(`[data-entry-id="${i}"]`)?.getBoundingClientRect().top ?? NaN, id);

for (const viewport of VIEWPORTS) {
  test.describe(`embedded at ${viewport.name}`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('replaces all fixed chrome with the link row', async ({ page }) => {
      const frame = await openEmbedded(page);
      await expect(h1(frame, 'Dashboard')).toBeVisible();
      await expect(linkRow(frame)).toBeVisible();
      await expect(frame.getByRole('banner')).toHaveCount(0);
      await expect(frame.getByText('VAULT', { exact: true })).toHaveCount(0);
      await expect(frame.getByRole('complementary', { name: 'Sidebar' })).toHaveCount(0);
      await expect(frame.getByRole('navigation', { name: 'Mobile navigation' })).toHaveCount(0);
      await expect(frame.getByRole('button', { name: 'More', exact: true })).toHaveCount(0);
      const fixed = await childWindow(page).evaluate(() => [...document.querySelectorAll('#app *')].filter((el) => getComputedStyle(el).position === 'fixed').length);
      expect(fixed).toBe(0);
    });

    for (const replica of [false, true]) {
      test(`reaches every page from the link row in ${replica ? 'replica' : 'daemon'} mode`, async ({ page }) => {
        const frame = await openEmbedded(page, { replica });
        await expect(linkRow(frame)).toBeVisible();
        const row = linkRow(frame);
        for (const entry of PAGES.filter((p) => !(replica && p.daemonOnly))) {
          const link = row.getByRole('link', { name: entry.label, exact: true });
          await link.click();
          await expect(h1(frame, entry.heading)).toBeVisible();
          await expect(link).toHaveAttribute('aria-current', 'page');
          expect(await childWindow(page).evaluate(() => location.hash)).toBe(entry.hash);
          await expectNoNestedScroll(page);
        }
        for (const hidden of PAGES.filter((p) => replica && p.daemonOnly)) {
          await expect(row.getByRole('link', { name: hidden.label, exact: true })).toHaveCount(0);
        }
        await expect(row.getByRole('link', { name: 'Documentation (opens in a new tab)' })).toHaveAttribute('target', '_blank');
      });
    }

    test('the link row wraps and is reachable by keyboard', async ({ page }) => {
      const frame = await openEmbedded(page);
      const row = linkRow(frame);
      await expect(row).toBeVisible();
      const sideways = await row.evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(sideways).toBeLessThanOrEqual(0);
      await row.getByRole('link', { name: 'Dashboard', exact: true }).focus();
      await page.keyboard.press('Tab');
      await page.keyboard.press('Enter');
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
    });

    test('the status banner works in the frame (#527)', async ({ page }) => {
      const frame = await openEmbedded(page);
      const banner = frame.getByRole('region', { name: 'Backup status' });
      await expect(banner.getByRole('heading', { name: 'Needs attention' })).toBeVisible();
      await settledFrameHeight(page);
      await expectNoNestedScroll(page);
      expect(await banner.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0);

      await banner.getByRole('link', { name: 'Add to a backup job' }).click();
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
      expect(await childWindow(page).evaluate(() => location.hash)).toBe('#/jobs');
      expect(new URL(page.url()).pathname).toBe(HARNESS);
    });

    test('the replica banner offers no link to a hidden page in the frame (#527)', async ({ page }) => {
      const frame = await openEmbedded(page, { replica: true });
      const banner = frame.getByRole('region', { name: 'Backup status' });
      await expect(banner.getByRole('heading', { name: 'Needs attention' })).toBeVisible();
      await expect(banner.getByRole('link', { name: 'Add to a backup job' })).toHaveCount(0);
      await banner.getByRole('link', { name: 'Review failures' }).click();
      await expect(h1(frame, 'Backup & Restore History')).toBeVisible();
    });

    test('the frame sizes to its content, growing and shrinking with it', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/anomalies' });
      await expect(h1(frame, 'Anomalies')).toBeVisible();
      const short = await settledFrameHeight(page);

      await linkRow(frame).getByRole('link', { name: 'Settings', exact: true }).click();
      await expect(h1(frame, 'Settings')).toBeVisible();
      await expect.poll(() => frameHeight(page)).toBeGreaterThan(short);
      await settledFrameHeight(page);
      await expectNoNestedScroll(page);

      await linkRow(frame).getByRole('link', { name: 'Anomalies', exact: true }).click();
      await expect(h1(frame, 'Anomalies')).toBeVisible();
      await expect.poll(() => frameHeight(page)).toBeLessThanOrEqual(short + 1);

      // A change inside one route, not just navigation.
      const before = await settledFrameHeight(page);
      await childWindow(page).evaluate(() => {
        const block = document.createElement('div');
        block.id = 'grown';
        block.style.height = '2000px';
        document.getElementById('main-content')!.append(block);
      });
      await expect.poll(() => frameHeight(page)).toBeGreaterThan(before + 1000);
      await expectNoNestedScroll(page);
      await childWindow(page).evaluate(() => document.getElementById('grown')!.remove());
      await expect.poll(() => frameHeight(page)).toBeLessThanOrEqual(before + 1);
    });

    test('the Logs console has no scroll box of its own (#526)', async ({ page }) => {
      const { log } = await openEmbeddedLogs(page);
      expect(await ownScrollers(page)).toEqual([]);
      expect(await log.evaluate((el) => getComputedStyle(el).overflowY)).toBe('visible');
      // The page scrolls past the whole rendered window, which is taller than the screen.
      expect((await log.boundingBox())!.height).toBeGreaterThan(viewport.height * 2);
      // Its height comes from its rows, not the frame, so nothing feeds back.
      const first = await settledFrameHeight(page);
      await page.waitForTimeout(500);
      expect(Math.abs((await frameHeight(page)) - first)).toBeLessThanOrEqual(1);
      await expectNoNestedScroll(page);
    });

    test('Logs follow, jump to oldest and jump to present scroll the page (#526)', async ({ page }) => {
      const { frame, log } = await openEmbeddedLogs(page);
      const present = frame.getByTitle('Jump to present');
      const oldest = frame.getByRole('button', { name: 'Jump to oldest log' });
      await expect(present).toHaveCount(0);
      await expect(oldest).toBeAttached();

      // Scrolling the page into the console leaves the live edge. Both buttons
      // stay on screen, clear of the arrow.
      await scrollParentBy(page, (await log.boundingBox())!.y + 1500);
      await expect(present).toBeVisible();
      await expectInParentViewport(page, await present.boundingBox());
      await expectInParentViewport(page, await oldest.boundingBox());

      // Oldest renders the oldest rows and scrolls the page to the end marker.
      const before = await parentScrollY(page);
      await clickInPlace(page, oldest);
      await expect(log.locator('[data-entry-id="1"]')).toBeAttached();
      const end = frame.getByText('— End of logs —');
      await expect.poll(async () => {
        const box = await end.boundingBox();
        return box !== null && box.y >= 0 && box.y + box.height <= viewport.height - ARROW_CLEARANCE + 1;
      }).toBe(true);
      expect(await parentScrollY(page)).toBeGreaterThan(before);
      await expect(oldest).toHaveCount(0);

      // Present returns to the newest line at the top of the screen, following again.
      await clickInPlace(page, present);
      const newest = log.locator(`[data-entry-id="${LOG_COUNT}"]`);
      await expect(newest).toBeVisible();
      await expect.poll(async () => (await newest.boundingBox())!.y).toBeGreaterThanOrEqual(0);
      expect((await newest.boundingBox())!.y).toBeLessThan(viewport.height);
      await expect(present).toHaveCount(0);
      await expectNoNestedScroll(page);
    });

    test('Logs keep the reading line still while paused, and follow at the live edge (#526)', async ({ page }) => {
      const { frame, log, send } = await openEmbeddedLogs(page);
      const present = frame.getByTitle('Jump to present');

      // At the live edge a new line lands on top and the page does not move.
      send(LOG_COUNT + 1);
      await expect(log.locator(`[data-entry-id="${LOG_COUNT + 1}"]`)).toBeVisible();
      await expect(present).toHaveCount(0);
      expect(await parentScrollY(page)).toBe(0);

      // Paused partway down, a new line above does not push the one being read.
      await scrollParentBy(page, (await log.boundingBox())!.y + 1200);
      await expect(present).toBeVisible();
      await page.waitForTimeout(100);
      const reading = await firstLineOnScreen(page);
      const screenY = async () => (await lineTop(page, reading.id)) - (await parentScrollY(page));
      const startY = await screenY();
      send(LOG_COUNT + 2);
      // It waits above the window, counted on the button.
      await expect(present).toHaveText(/1 new entry/);
      await expect.poll(async () => Math.abs((await screenY()) - startY)).toBeLessThan(2);
    });

    test('opening a Logs row pauses follow in the frame (#526)', async ({ page }) => {
      const { frame, log } = await openEmbeddedLogs(page);
      await log.locator(`[data-entry-id="${LOG_COUNT}"] [aria-expanded]`).click();
      await expect(log.locator(`[data-entry-id="${LOG_COUNT}"] [aria-expanded="true"]`)).toBeVisible();
      await expect(frame.getByTitle('Jump to present')).toBeVisible();
      expect(await ownScrollers(page)).toEqual([]);
    });

    test('Logs copy and export work in the frame (#526)', async ({ page, context }) => {
      await context.grantPermissions(['clipboard-read', 'clipboard-write']);
      const { frame } = await openEmbeddedLogs(page);
      await frame.getByRole('button', { name: 'Copy', exact: true }).click();
      await expect(frame.getByRole('button', { name: 'Copied', exact: true })).toBeVisible();
      const copied = await childWindow(page).evaluate(() => navigator.clipboard.readText());
      expect(copied.split('\n')).toHaveLength(LOG_COUNT);
      expect(copied).toContain(`Embedded entry ${LOG_COUNT} | seq=${LOG_COUNT}`);
      const [download] = await Promise.all([
        page.waitForEvent('download'),
        frame.getByRole('button', { name: 'Export', exact: true }).click(),
      ]);
      expect(download.suggestedFilename()).toMatch(/^vault-logs-\d{4}-\d{2}-\d{2}\.log$/);
    });

    test('the end of the content scrolls clear of the floating arrow', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/settings' });
      await expect(h1(frame, 'Settings')).toBeVisible();
      await settledFrameHeight(page);
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
      const content = await frame.locator('#main-content > div').boundingBox();
      const arrow = await page.locator('#arrow').boundingBox();
      expect(content!.y + content!.height).toBeLessThanOrEqual(arrow!.y);
    });

    test('a dialog opens on screen after the parent has scrolled, and still works', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/jobs' });
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
      await settledFrameHeight(page);
      await page.evaluate(() => window.scrollTo(0, 250));
      await frame.getByRole('button', { name: 'New Job' }).first().click();
      const dialog = frame.getByRole('dialog', { name: 'Create Backup Job' });
      await expect(dialog).toBeVisible();
      await expect.poll(async () => {
        const box = await dialog.locator('> div').boundingBox();
        const limit = viewport.height - ARROW_CLEARANCE + 1;
        return box !== null && box.y >= 0 && box.y + box.height <= limit;
      }).toBe(true);
      await expectInParentViewport(page, await dialog.locator('> div').boundingBox());
      await page.keyboard.press('Escape');
      await expect(dialog).toBeHidden();
    });

    test('a dialog follows the parent while it scrolls', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/settings' });
      await expect(h1(frame, 'Settings')).toBeVisible();
      await settledFrameHeight(page);
      await linkRow(frame).getByRole('link', { name: 'Settings', exact: true }).focus();
      await page.keyboard.press('ControlOrMeta+k');
      const dialog = frame.getByRole('dialog').first();
      await expect(dialog).toBeVisible();
      await page.evaluate(() => window.scrollTo(0, 700));
      await expect.poll(async () => {
        const box = await dialog.locator('> div').boundingBox();
        return box !== null && box.y >= 0 && box.y + box.height <= viewport.height;
      }).toBe(true);
    });

    test('the bulk action bar stays on screen', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/jobs' });
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
      await frame.getByRole('checkbox', { name: /^Select / }).first().check();
      const bar = frame.getByText(/\d+ selected/).locator('..').last();
      await expect(bar).toBeVisible();
      await expect.poll(async () => {
        const box = await bar.boundingBox();
        return box !== null && box.y + box.height <= viewport.height - ARROW_CLEARANCE;
      }).toBe(true);
    });

    test('every bulk control is on screen and works', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/jobs' });
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
      await frame.getByRole('checkbox', { name: 'Select Docker & Appdata' }).check();
      const bar = frame.getByRole('region', { name: 'Bulk actions' });
      await expect(bar).toBeVisible();
      for (const control of [bar.getByText('1 selected'), ...await bar.getByRole('button').all()]) {
        await expect.poll(async () => {
          const box = await control.boundingBox();
          return box !== null && box.x >= 0 && box.x + box.width <= viewport.width && box.y >= 0 && box.y + box.height <= viewport.height;
        }).toBe(true);
      }
      await bar.getByRole('button', { name: 'Clear' }).click();
      await expect(bar).toHaveCount(0);
    });

    test('a toast appears on screen after the parent has scrolled', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/jobs' });
      await expect(h1(frame, 'Backup Jobs')).toBeVisible();
      await settledFrameHeight(page);
      await page.evaluate(() => window.scrollTo(0, 250));
      await frame.getByRole('button', { name: /run backup now/i }).first().click();
      const toast = frame.getByRole('alert');
      await expect(toast).toBeVisible();
      await expectInParentViewport(page, await toast.boundingBox());
    });

    test('the link row passes an accessibility audit', async ({ page }) => {
      await page.emulateMedia({ reducedMotion: 'reduce' });
      const frame = await openEmbedded(page);
      await expect(linkRow(frame)).toBeVisible();
      const row = ['#vault-backups-frame', 'nav[aria-label="Main navigation"]'];
      const { violations } = await new AxeBuilder({ page }).include(row as never).analyze();
      expect(violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.html} ${n.failureSummary}`))).toEqual([]);
    });

    test('the skip link brings the focused heading on screen', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/settings' });
      await expect(h1(frame, 'Settings')).toBeVisible();
      await settledFrameHeight(page);
      await page.evaluate(() => window.scrollTo(0, 900));
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(HEADER_HEIGHT);
      await page.waitForTimeout(100);
      await frame.getByRole('link', { name: 'Skip to main content' }).focus();
      await page.keyboard.press('Enter');
      const heading = h1(frame, 'Settings');
      await expect(heading).toBeFocused();
      await expect.poll(async () => {
        const box = await heading.boundingBox();
        return box !== null && box.y >= 0 && box.y + box.height <= viewport.height;
      }).toBe(true);
    });

    test('a new route brings the frame top back on screen', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/settings' });
      await expect(h1(frame, 'Settings')).toBeVisible();
      await settledFrameHeight(page);
      await page.evaluate(() => window.scrollTo(0, 900));
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(HEADER_HEIGHT);
      await page.waitForTimeout(100);
      await childWindow(page).evaluate(() => { location.hash = '#/logs'; });
      await expect(h1(frame, 'Logs')).toBeVisible();
      await expect.poll(async () => (await page.locator('#vault-backups-frame').boundingBox())!.y).toBeGreaterThanOrEqual(-1);
    });

    test('restore timeline day jumps scroll the parent page', async ({ page }) => {
      // The mock's points share a day, so spread them out to draw the day strip.
      const spreadPointsAcrossDays = () => page.route(/\/jobs\/1\/restore-points(\?|$)/, (route) => route.fulfill({
        json: Array.from({ length: 12 }, (_, i) => ({
          ...mockJob1Points[0],
          id: 1001 + i,
          created_at: `2026-10-${String(i + 1).padStart(2, '0')}T02:00:00Z`,
        })),
      }));
      const frame = await openEmbedded(page, { hash: '#/restore', beforeLoad: spreadPointsAcrossDays });
      await frame.getByRole('button', { name: /plex/i }).click();
      await frame.getByRole('button', { name: 'Next', exact: true }).click();
      const days = frame.getByRole('group', { name: 'Backup activity by day' }).getByRole('button');
      await expect(days.first()).toBeVisible();
      await settledFrameHeight(page);
      const before = await page.evaluate(() => window.scrollY);
      // The strip runs oldest to newest and the list newest first, so the first
      // bar is the last, offscreen day.
      await days.first().click();
      const target = frame.locator('[id^="rpday-"]').last();
      await expect.poll(async () => {
        const box = await target.boundingBox();
        return box !== null && box.y >= 0 && box.y < page.viewportSize()!.height;
      }).toBe(true);
      expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(before);
    });

    test('Settings jump links scroll the parent page', async ({ page }) => {
      const frame = await openEmbedded(page, { hash: '#/settings' });
      await expect(h1(frame, 'Settings')).toBeVisible();
      await settledFrameHeight(page);
      await frame.getByRole('button', { name: 'Appearance', exact: true }).click();
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
      const target = await frame.locator('#set-appearance').boundingBox();
      expect(target!.y).toBeGreaterThanOrEqual(0);
      expect(target!.y).toBeLessThan(viewport.height);
      await expect(frame.locator('#set-appearance')).toBeFocused();
    });

    test('standalone is unchanged', async ({ page }) => {
      await setupVaultMockApi(page);
      await page.goto('/');
      await expect(page.getByRole('heading', { level: 1, name: 'Dashboard', exact: true })).toBeVisible();
      const desktop = viewport.width >= 1024;
      await expect(page.getByRole('complementary', { name: 'Sidebar' })).toBeVisible({ visible: desktop });
      await expect(page.getByRole('banner')).toBeVisible({ visible: !desktop });
      await expect(page.getByRole('navigation', { name: 'Mobile navigation' })).toBeVisible({ visible: !desktop });
      const state = await page.evaluate(() => ({
        embedded: document.documentElement.classList.contains('vault-embedded'),
        shell: document.querySelector('#app > div')!.className,
        main: getComputedStyle(document.getElementById('main-content')!).overflowY,
        html: getComputedStyle(document.documentElement).overflow,
      }));
      expect(state).toEqual({ embedded: false, shell: 'flex h-screen bg-surface', main: 'auto', html: 'hidden' });
    });

    test('standalone Logs keeps its own scroll box', async ({ page }) => {
      await setupVaultMockApi(page);
      await page.goto('/#/logs');
      const log = page.getByRole('log');
      await expect(log).toBeVisible();
      const state = await log.evaluate((el) => ({
        overflow: getComputedStyle(el).overflowY,
        height: Math.round(el.getBoundingClientRect().height),
        frame: el.parentElement!.className,
        // No sticky button rails: the buttons sit on the console's own corners.
        rails: el.parentElement!.querySelectorAll(':scope > .sticky').length,
      }));
      expect(state).toEqual({ overflow: 'auto', height: Math.round(viewport.height * 0.72), frame: 'relative vault-console-frame', rails: 0 });
    });
  });
}

test.describe('embedded theme', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  const isDark = (page: Page) => childWindow(page).evaluate(() => document.documentElement.classList.contains('dark'));

  for (const [hostTheme, system, dark] of [['light', 'dark', false], ['dark', 'light', true]] as const) {
    test(`follows a ${hostTheme} parent over a ${system} system preference, without persisting it`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: system });
      const frame = await openEmbedded(page, { hostTheme });
      await expect(h1(frame, 'Dashboard')).toBeVisible();
      expect(await isDark(page)).toBe(dark);
      expect(await childWindow(page).evaluate(() => localStorage.getItem('vault-mode'))).toBeNull();
    });
  }

  for (const hostTheme of [null, 'custom-theme']) {
    test(`falls back to the system preference when the parent theme is ${hostTheme}`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'light' });
      const frame = await openEmbedded(page, { hostTheme });
      await expect(h1(frame, 'Dashboard')).toBeVisible();
      expect(await isDark(page)).toBe(false);
    });
  }

  for (const [stored, dark] of [['dark', true], ['light', false], ['system', false]] as const) {
    test(`a stored ${stored} choice beats the parent`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'light' });
      const hostTheme = dark ? 'light' : 'dark';
      const frame = await openEmbedded(page, { hostTheme, stored: { 'vault-mode': stored } });
      await expect(h1(frame, 'Dashboard')).toBeVisible();
      expect(await isDark(page)).toBe(dark);
    });
  }

  test('choosing a mode in Settings sticks across a reload', async ({ page }) => {
    const frame = await openEmbedded(page, { hostTheme: 'light', hash: '#/settings' });
    await expect(h1(frame, 'Settings')).toBeVisible();
    expect(await isDark(page)).toBe(false);
    await frame.getByRole('button', { name: 'Dark', exact: true }).click();
    expect(await isDark(page)).toBe(true);
    await page.reload();
    await expect(h1(frame, 'Settings')).toBeVisible();
    expect(await isDark(page)).toBe(true);
  });
});

test.describe('embedded guards', () => {
  test.use({ viewport: { width: 375, height: 667 } });

  test('a cross-origin parent gets the standalone layout and no errors', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    const frame = await openEmbedded(page, { crossOrigin: true, hostTheme: 'dark' });
    await expect(h1(frame, 'Dashboard')).toBeVisible();
    await expect(frame.getByRole('navigation', { name: 'Mobile navigation' })).toBeVisible();
    await expect(frame.getByRole('navigation', { name: 'Main navigation' })).toHaveCount(0);
    expect(await childWindow(page).evaluate(() => document.documentElement.classList.contains('vault-embedded'))).toBe(false);
    expect(errors).toEqual([]);
  });

  test('a top-level embed=1 stays standalone', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.addInitScript(() => { (window as any).__VAULT_RUNTIME_CONFIG__ = { presentation: 'embedded', hostTheme: 'dark' }; });
    await page.goto('/?embed=1#/');
    await expect(page.getByRole('navigation', { name: 'Mobile navigation' })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.classList.contains('vault-embedded'))).toBe(false);
  });
});
