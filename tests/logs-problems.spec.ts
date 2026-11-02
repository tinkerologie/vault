import { readFile } from 'node:fs/promises';
import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Locator, type Page, type WebSocketRoute } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { expectNoContrastViolations, seedTheme, settleMotion, type ThemeMode, type ThemeStyle } from './helpers/contrast';

// Warnings that show their cause, inline row details, and the Problems filter (#534).

type Row = { id: number; level: string; category: string; message: string; details: string | null; created_at: string };

const at = (sec: number) => new Date(Date.UTC(2026, 9, 20, 12, 0, sec)).toISOString();
const row = (id: number, level: string, category: string, message: string, details: unknown = null): Row => ({
  id, level, category, message, details: details ? JSON.stringify(details) : null, created_at: at(id),
});

const PLEX_ERROR = 'Timed out waiting, retrying, code=7';
// Oldest first. Rows 6 and 7 fold, as do 1 and 2.
const ROWS: Row[] = [
  row(1, 'info', 'backup', 'Uploaded recyclarr, file=a.tar'),
  row(2, 'info', 'backup', 'Uploaded recyclarr, file=b.tar'),
  row(3, 'warn', 'health', `Health check, container=plex, status=unhealthy, error=${PLEX_ERROR}`, { container_name: 'plex', status: 'unhealthy', job_id: 1, job_name: 'Nightly' }),
  row(4, 'warn', 'health', 'Health check, container=sonarr, status=unhealthy', { container_name: 'sonarr', status: 'unhealthy', job_id: 2, job_name: 'Weekly' }),
  row(5, 'warn', 'health', 'Health check, container=sonarr, status=failed, error=Container is exited (exit code 1)', { container_name: 'sonarr', status: 'failed', job_id: 2, job_name: 'Weekly' }),
  row(6, 'warn', 'health', 'Health check, container=radarr, status=unhealthy, error=first', { container_name: 'radarr', status: 'unhealthy', job_id: 2, job_name: 'Weekly' }),
  row(7, 'warn', 'health', 'Health check, container=radarr, status=unhealthy, error=second', { container_name: 'radarr', status: 'unhealthy', job_id: 2, job_name: 'Weekly' }),
  row(8, 'warning', 'health', 'Storage health check, destination=NAS, status=failed, error=dial tcp, refused', { storage_dest_id: 2, status: 'failed', error: 'dial tcp, refused' }),
  row(9, 'error', 'backup', 'Backup finished, job="Nightly", status=failed, items=0/1', { job_id: 1, job_name: 'Nightly' }),
  row(10, 'info', 'system', 'Routine tick'),
  row(11, 'error', 'system', 'Post-script failed: Nightly', { script: '/SENTINEL/script.sh', error: 'SENTINEL error text' }),
];

const COPIED = [
  '2026-10-20 12:00:01  info  backup  Uploaded recyclarr | file=a.tar',
  '2026-10-20 12:00:02  info  backup  Uploaded recyclarr | file=b.tar',
  `2026-10-20 12:00:03  warn  health  Health check | container=plex, status=unhealthy, error=${PLEX_ERROR}, job=Nightly`,
  '2026-10-20 12:00:04  warn  health  Health check | container=sonarr, status=unhealthy, job=Weekly',
  '2026-10-20 12:00:05  warn  health  Health check | container=sonarr, status=failed, error=Container is exited (exit code 1), job=Weekly',
  '2026-10-20 12:00:06  warn  health  Health check | container=radarr, status=unhealthy, error=first, job=Weekly',
  '2026-10-20 12:00:07  warn  health  Health check | container=radarr, status=unhealthy, error=second, job=Weekly',
  '2026-10-20 12:00:08  warn  health  Storage health check | destination=NAS, status=failed, error=dial tcp, refused',
  '2026-10-20 12:00:09  error  backup  Backup finished | job="Nightly", status=failed, items=0/1',
  '2026-10-20 12:00:10  info  system  Routine tick',
  '2026-10-20 12:00:11  error  system  Post-script failed: Nightly',
];

async function serveRows(page: Page, rows: Row[]) {
  await page.route('**/api/v1/activity*', async (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    const url = new URL(route.request().url());
    const beforeId = parseInt(url.searchParams.get('before_id') || '0', 10);
    const limit = parseInt(url.searchParams.get('limit') || '30', 10);
    const category = url.searchParams.get('category') || '';
    const body = [...rows].reverse()
      .filter((r) => (!beforeId || r.id < beforeId) && (!category || r.category === category))
      .slice(0, limit);
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
}

async function openLogs(page: Page, rows: Row[] = ROWS) {
  await setupVaultMockApi(page);
  let socket: WebSocketRoute | null = null;
  await page.routeWebSocket('**/api/v1/ws', (ws) => { socket = ws; });
  await serveRows(page, rows);
  await page.goto('/#/logs');
  const box = page.getByRole('log');
  await expect(box.locator(`[data-entry-id="${rows[rows.length - 1].id}"]`)).toBeVisible();
  await expect.poll(() => socket !== null).toBe(true);
  // Let the background full-history load finish, so nothing but the test moves rows.
  await expect(box.locator('[data-end-marker]')).toBeAttached();
  return {
    box,
    send: (r: Row) => socket!.send(JSON.stringify({ type: 'activity', entry: r })),
  };
}

const entry = (box: Locator, id: number) => box.locator(`[data-entry-id="${id}"]`);
const toggleOf = (box: Locator, id: number) => entry(box, id).locator('[aria-expanded]');
const copyOf = (box: Locator, id: number) => entry(box, id).getByRole('button', { name: /^Copy/ });
const offset = (box: Locator, id: number) => box.evaluate((el, i) => {
  const r = el.querySelector(`[data-entry-id="${i}"]`);
  return r ? r.getBoundingClientRect().top - el.getBoundingClientRect().top : NaN;
}, id);
// A real mouse click where the control already is. Locator.click() may first
// scroll its target to wherever Playwright prefers, which is the very movement
// the scroll tests below measure.
async function mouseClick(page: Page, target: Locator) {
  const at = (await target.boundingBox())!;
  await page.mouse.click(at.x + at.width / 2, at.y + at.height / 2);
}
const clipboard = (page: Page) => page.evaluate(() => navigator.clipboard.readText());

// The job name in COPIED is the canonical line's structured extra (logline.js).
test.use({ timezoneId: 'UTC', permissions: ['clipboard-read', 'clipboard-write'] });

test.describe('warning and error rows (#534)', () => {
  test('show the item, status and error with Details off', async ({ page }) => {
    const { box } = await openLogs(page);
    await expect(page.getByRole('button', { name: 'Details: off' })).toBeVisible();
    await expect(entry(box, 3)).toContainText(`Health check · plex · unhealthy · ${PLEX_ERROR}`);
    await expect(entry(box, 4)).toContainText('Health check · sonarr · unhealthy');
    await expect(entry(box, 8)).toContainText('Storage health check · NAS · failed · dial tcp, refused');
    await expect(entry(box, 9)).toContainText('Backup finished · Nightly · failed');
    // Routine rows keep their short form.
    await expect(entry(box, 10)).toHaveText(/Routine tick/);
    await expect(entry(box, 2)).not.toContainText('file=');
  });

  test('never show structured details that Details does not show', async ({ page }) => {
    const { box } = await openLogs(page);
    await toggleOf(box, 9).click();
    await page.getByRole('button', { name: /^Details:/ }).click();
    await expect(box).not.toContainText('SENTINEL');
    await page.getByRole('button', { name: /^Details:/ }).click();
    await expect(box).not.toContainText('SENTINEL');
  });

  test('about different containers or statuses never fold; the same ones do; info rows still fold', async ({ page }) => {
    const { box } = await openLogs(page);
    for (const id of [3, 4, 5]) await expect(entry(box, id)).toBeVisible();
    await expect(entry(box, 5)).not.toContainText('×');
    await expect(entry(box, 7)).toContainText('×2');
    await expect(entry(box, 6)).toHaveCount(0);
    await expect(entry(box, 2)).toContainText('×2');
    await expect(entry(box, 1)).toHaveCount(0);
  });

  test('show the cause at 375 px', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    const { box } = await openLogs(page);
    const text = entry(box, 3).getByText(`Health check · plex · unhealthy · ${PLEX_ERROR}`);
    await expect(text).toBeVisible();
    const clipped = await text.evaluate((el) => el.scrollWidth > el.clientWidth + 1);
    expect(clipped).toBe(false);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});

test.describe('row details (#534)', () => {
  test('mouse: one row opens and closes, others do not move state', async ({ page }) => {
    const { box } = await openLogs(page);
    const toggle = toggleOf(box, 3);
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const panelId = await toggle.getAttribute('aria-controls');
    const panel = page.locator(`[id="${panelId}"]`);
    await expect(panel).toContainText(`container=plex, status=unhealthy, error=${PLEX_ERROR}`);
    await expect(box.locator('[data-log-details]')).toHaveCount(1);
    await expect(toggleOf(box, 4)).toHaveAttribute('aria-expanded', 'false');
    await expect(entry(box, 4)).not.toContainText('container=');

    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await expect(box.locator('[data-log-details]')).toHaveCount(0);
  });

  test('keyboard: Enter and Space work, and focus stays on the toggle', async ({ page }) => {
    const { box } = await openLogs(page);
    const toggle = toggleOf(box, 4);
    await toggle.focus();
    await page.keyboard.press('Enter');
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(toggle).toBeFocused();
    await page.keyboard.press('Space');
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await expect(toggle).toBeFocused();
    await expect(toggleOf(box, 3)).toHaveAttribute('aria-expanded', 'false');
  });

  test('a folded row lists each entry, oldest details included', async ({ page }) => {
    const { box } = await openLogs(page);
    await toggleOf(box, 7).click();
    const items = box.locator('[data-log-details] li');
    await expect(items).toHaveCount(2);
    await expect(items.nth(0)).toContainText('error=second');
    await expect(items.nth(1)).toContainText('error=first');
  });

  test('a bare row has no toggle', async ({ page }) => {
    const { box } = await openLogs(page);
    await expect(entry(box, 10)).toBeVisible();
    await expect(toggleOf(box, 10)).toHaveCount(0);
  });

  test('stays open when a new line folds into it', async ({ page }) => {
    const rows = [...ROWS, row(12, 'info', 'system', 'Heartbeat, n=1'), row(13, 'info', 'system', 'Heartbeat, n=2')];
    const { box, send } = await openLogs(page, rows);
    const toggle = toggleOf(box, 13);
    await toggle.focus();
    await page.keyboard.press('Enter');
    const before = await offset(box, 13);
    send(row(14, 'info', 'system', 'Heartbeat, n=3'));
    await expect(box.locator('[data-log-details] li')).toHaveCount(3);
    await expect(entry(box, 14).locator('[aria-expanded]')).toHaveAttribute('aria-expanded', 'true');
    await expect(entry(box, 14).locator('[aria-expanded]')).toBeFocused();
    expect(Math.abs(await offset(box, 14) - before)).toBeLessThan(2);
  });
});

test.describe('row details on a phone (#534)', () => {
  test.use({ viewport: { width: 375, height: 812 }, hasTouch: true, isMobile: true });

  test('touch: tap opens and closes one row; row copy is there before any tap', async ({ page }) => {
    const { box } = await openLogs(page);
    const toggle = toggleOf(box, 3);
    await toggle.tap();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(box.locator('[data-log-details]')).toContainText('container=plex');
    await expect(toggleOf(box, 4)).toHaveAttribute('aria-expanded', 'false');
    await toggle.tap();
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');

    const copy = copyOf(box, 4);
    await expect.poll(() => copy.evaluate((el) => getComputedStyle(el).opacity)).toBe('1');
    await copy.tap();
    await expect.poll(() => clipboard(page)).toBe(COPIED[3]);
  });

  test('touch: the newest row can be copied, the jump button does not cover it', async ({ page }) => {
    const { box } = await openLogs(page);
    await copyOf(box, 11).tap();
    await expect.poll(() => clipboard(page)).toBe(COPIED[10]);
  });

  test('controls measure at least 24 px', async ({ page }) => {
    const { box } = await openLogs(page);
    for (const control of [toggleOf(box, 3), copyOf(box, 3), page.getByRole('button', { name: 'Problems' })]) {
      const size = await control.boundingBox();
      expect(size!.height).toBeGreaterThanOrEqual(24);
      expect(size!.width).toBeGreaterThanOrEqual(24);
    }
  });
});

test.describe('no scroll jump on opening a row (#534)', () => {
  const MANY = Array.from({ length: 150 }, (_, i) => row(i + 1, 'warn', 'health', `Health check ${i + 1}, container=c${i + 1}, status=unhealthy`));
  const incoming = (n: number) => row(1000 + n, 'warn', 'health', `Health check new ${n}, container=n${n}, status=unhealthy`);

  // The first row whose top is at least `past` px into the console.
  const rowInView = (box: Locator, past: number) => box.evaluate((el, p) => {
    const top = el.getBoundingClientRect().top;
    const found = [...el.querySelectorAll<HTMLElement>('[data-entry-id]')].find((r) => r.getBoundingClientRect().top - top >= p);
    return Number(found!.dataset.entryId);
  }, past);

  test('auto-follow on: opening pauses follow, and new lines do not move the row', async ({ page }) => {
    const { box, send } = await openLogs(page, MANY);
    await expect(page.getByRole('button', { name: 'Jump to present' })).toHaveCount(0);
    const id = await rowInView(box, 120);
    const before = await offset(box, id);

    await mouseClick(page, toggleOf(box, id));
    expect(Math.abs(await offset(box, id) - before)).toBeLessThan(2);
    await expect(page.getByRole('button', { name: 'Jump to present' })).toBeVisible();

    for (let n = 1; n <= 3; n++) {
      send(incoming(n));
      await expect(page.getByRole('button', { name: `${n} new ${n === 1 ? 'entry' : 'entries'}` })).toBeVisible();
      expect(Math.abs(await offset(box, id) - before)).toBeLessThan(2);
    }

    // Returning to the present is one click and shows what arrived.
    await page.getByRole('button', { name: '3 new entries' }).click();
    await expect(entry(box, 1003)).toBeVisible();
    await expect.poll(() => box.evaluate((el) => el.scrollTop)).toBe(0);
    await expect(toggleOf(box, id)).toHaveAttribute('aria-expanded', 'true');
  });

  test('auto-follow off: scrolled down, new lines do not move the row', async ({ page }) => {
    const { box, send } = await openLogs(page, MANY);
    await box.evaluate((el) => { el.scrollTop = 1200; });
    await expect(page.getByRole('button', { name: 'Jump to present' })).toBeVisible();
    const id = await rowInView(box, 100);
    const scrollBefore = await box.evaluate((el) => el.scrollTop);
    const before = await offset(box, id);

    await mouseClick(page, toggleOf(box, id));
    await expect(toggleOf(box, id)).toHaveAttribute('aria-expanded', 'true');
    expect(Math.abs(await offset(box, id) - before)).toBeLessThan(2);
    expect(Math.abs(await box.evaluate((el) => el.scrollTop) - scrollBefore)).toBeLessThan(2);

    for (let n = 1; n <= 3; n++) {
      send(incoming(n));
      await expect(page.getByRole('button', { name: `${n} new ${n === 1 ? 'entry' : 'entries'}` })).toBeVisible();
      expect(Math.abs(await offset(box, id) - before)).toBeLessThan(2);
    }

    await mouseClick(page, toggleOf(box, id));
    await expect(toggleOf(box, id)).toHaveAttribute('aria-expanded', 'false');
    expect(Math.abs(await offset(box, id) - before)).toBeLessThan(2);
  });

  test('closing a row at the very bottom keeps it in place', async ({ page }) => {
    const { box } = await openLogs(page, MANY);
    await box.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await expect(page.getByText('— End of logs —')).toBeVisible();
    await toggleOf(box, 1).click();
    await box.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    const before = await offset(box, 2);
    await toggleOf(box, 1).click();
    await expect(toggleOf(box, 1)).toHaveAttribute('aria-expanded', 'false');
    // The console shrank under the reader, but row 2 keeps its screen position
    // as far as the content allows; it must not jump more than the panel's height.
    expect(Math.abs(await offset(box, 2) - before)).toBeLessThan(40);
  });
});

test.describe('copy and export stay verbatim (#534)', () => {
  test('Copy and Export hold every line, folded ones included', async ({ page }) => {
    await openLogs(page);
    await page.getByRole('button', { name: 'Copy', exact: true }).click();
    await expect.poll(() => clipboard(page)).toBe(COPIED.join('\n'));

    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export' }).click()]);
    expect(await readFile(await download.path(), 'utf8')).toBe(COPIED.join('\n'));
  });

  test('an open row changes nothing that is copied', async ({ page }) => {
    const { box } = await openLogs(page);
    await toggleOf(box, 7).click();
    await page.getByRole('button', { name: 'Copy', exact: true }).click();
    await expect.poll(() => clipboard(page)).toBe(COPIED.join('\n'));
  });

  test('Export under Problems holds exactly the problem lines', async ({ page }) => {
    await openLogs(page);
    await page.getByRole('button', { name: 'Problems' }).click();
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export' }).click()]);
    expect(await readFile(await download.path(), 'utf8')).toBe(COPIED.filter((l) => /  (warn|error)  /.test(l)).join('\n'));
  });

  test('row copy by mouse gives every line the row folded, newest first', async ({ page }) => {
    const { box } = await openLogs(page);
    await entry(box, 7).hover();
    await copyOf(box, 7).click();
    await expect.poll(() => clipboard(page)).toBe([COPIED[6], COPIED[5]].join('\n'));
  });

  test('row copy by keyboard: Tab reaches it without hover, visible, and Enter copies', async ({ page }) => {
    const { box } = await openLogs(page);
    await toggleOf(box, 4).focus();
    await page.keyboard.press('Tab');
    const copy = copyOf(box, 4);
    await expect(copy).toBeFocused();
    await expect(copy).toHaveAccessibleName('Copy log line');
    await expect.poll(() => copy.evaluate((el) => getComputedStyle(el).opacity)).toBe('1');
    await page.keyboard.press('Enter');
    await expect.poll(() => clipboard(page)).toBe(COPIED[3]);
    await expect(copyOf(box, 7)).toHaveAccessibleName('Copy 2 log lines');
  });
});

test.describe('Problems filter (#534)', () => {
  test('shows only warnings and errors, and clears with one click on the toggle', async ({ page }) => {
    const { box } = await openLogs(page);
    const toggle = page.getByRole('button', { name: 'Problems' });
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByRole('combobox', { name: 'Level' })).toHaveValue('problems');
    await expect(entry(box, 10)).toHaveCount(0);
    await expect(entry(box, 2)).toHaveCount(0);
    await expect(entry(box, 8)).toBeVisible();
    await expect(entry(box, 9)).toBeVisible();
    const pills = await box.locator('[data-entry-id]').evaluateAll((rows) => rows.map((r) => r.querySelector('.uppercase')!.textContent!.trim().toLowerCase()));
    expect(new Set(pills)).toEqual(new Set(['warn', 'error']));

    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await expect(entry(box, 10)).toBeVisible();
  });

  test('the select and Reset filters drive the same state', async ({ page }) => {
    const { box } = await openLogs(page);
    const select = page.getByRole('combobox', { name: 'Level' });
    const toggle = page.getByRole('button', { name: 'Problems' });
    await select.selectOption({ label: 'Problems' });
    await expect(toggle).toHaveAttribute('aria-pressed', 'true');
    await page.getByRole('button', { name: 'Reset filters' }).click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await expect(select).toHaveValue('');
    await expect(entry(box, 10)).toBeVisible();

    await select.selectOption({ label: 'Error' });
    await toggle.click();
    await expect(select).toHaveValue('problems');
    await expect(entry(box, 8)).toBeVisible();
  });

  test('composes with Category, Job and Search', async ({ page }) => {
    const { box } = await openLogs(page);
    await page.getByRole('button', { name: 'Problems' }).click();

    await page.getByRole('combobox', { name: 'Job' }).selectOption('Nightly');
    await expect(entry(box, 3)).toBeVisible();
    await expect(entry(box, 5)).toHaveCount(0);
    await page.getByRole('combobox', { name: 'Job' }).selectOption({ index: 0 });

    await page.getByPlaceholder(/filter logs/i).fill('refused');
    await expect(box.locator('[data-entry-id]')).toHaveCount(1);
    await expect(entry(box, 8)).toBeVisible();
    await page.getByPlaceholder(/filter logs/i).fill('');

    await page.getByRole('combobox', { name: 'Category' }).selectOption('system');
    await expect(entry(box, 11)).toBeVisible();
    await expect(entry(box, 10)).toHaveCount(0);
    await expect(entry(box, 9)).toHaveCount(0);

    // One action clears everything at once.
    await page.getByRole('button', { name: 'Reset filters' }).click();
    await expect(page.getByRole('button', { name: 'Problems' })).toHaveAttribute('aria-pressed', 'false');
    await expect(entry(box, 2)).toBeVisible();
  });

  test('a console with no problems says so', async ({ page }) => {
    await openLogs(page, [row(1, 'info', 'backup', 'Routine tick')]);
    await page.getByRole('button', { name: 'Problems' }).click();
    await expect(page.getByText('No problems', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Reset filters' }).click();
    await expect(page.getByText('Routine tick')).toBeVisible();
  });
});

test.describe('labels (#534)', () => {
  test('the jump button says Oldest, and sits clear of the rows', async ({ page }) => {
    const rows = Array.from({ length: 150 }, (_, i) => row(i + 1, 'info', 'backup', `Line ${i + 1}`));
    const { box } = await openLogs(page, rows);
    const jump = page.getByRole('button', { name: 'Jump to oldest log' });
    await expect(jump).toBeVisible();
    await expect(jump).toHaveText('Oldest');
    const jumpBox = await jump.boundingBox();
    const first = await entry(box, 150).boundingBox();
    expect(jumpBox!.y + jumpBox!.height).toBeLessThanOrEqual(first!.y + 1);
  });

  test('level labels, counts and day labels are at least 11 px', async ({ page }) => {
    const { box } = await openLogs(page);
    const size = (l: Locator) => l.first().evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
    expect(await size(entry(box, 3).locator('.uppercase'))).toBeGreaterThanOrEqual(11);
    expect(await size(entry(box, 7).getByText('×2'))).toBeGreaterThanOrEqual(11);
    expect(await size(box.locator('.sticky span').first())).toBeGreaterThanOrEqual(11);
  });
});

const THEMES = (['default', '1bit', 'retro'] as ThemeStyle[]).flatMap((style) =>
  (['light', 'dark'] as ThemeMode[]).map((mode) => ({ style, mode, label: `${style} ${mode}` })));
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];

async function structural(page: Page, label: string) {
  const { violations } = await new AxeBuilder({ page }).withTags(TAGS).disableRules(['color-contrast']).analyze();
  expect(violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')}`)), `${label}: axe`).toEqual([]);
}

for (const { style, mode, label } of THEMES) {
  test(`axe, opened row and Problems: ${label}`, async ({ page }) => {
    await seedTheme(page, style, mode);
    await settleMotion(page);
    const { box } = await openLogs(page);
    await toggleOf(box, 7).click();
    await toggleOf(box, 3).click();
    await expect(box.locator('[data-log-details]')).toHaveCount(2);
    await structural(page, `${label} opened`);
    await expectNoContrastViolations(page, `${label} opened`);

    await page.getByRole('button', { name: 'Problems' }).click();
    await expect(page.getByRole('button', { name: 'Problems' })).toHaveAttribute('aria-pressed', 'true');
    await structural(page, `${label} Problems`);
    await expectNoContrastViolations(page, `${label} Problems`);

    await entry(box, 9).hover();
    await expectNoContrastViolations(page, `${label} hovered row`);
  });
}

test('axe at 375 px with a row opened and Problems on', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await settleMotion(page);
  const { box } = await openLogs(page);
  await page.getByRole('button', { name: 'Problems' }).click();
  await toggleOf(box, 7).click();
  await expect(box.locator('[data-log-details]')).toHaveCount(1);
  await structural(page, 'phone');
  await expectNoContrastViolations(page, 'phone');
});
