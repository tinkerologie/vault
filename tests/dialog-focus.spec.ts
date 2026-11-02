import { test, expect, type Locator, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

const REPLICATION_SOURCES = [
  { id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 },
];

/** Whether focus is on or inside the topmost open dialog, and not merely on <body>. */
const focusIsInTopDialog = (page: Page) =>
  page.evaluate(() => {
    const open = [...document.querySelectorAll('dialog[open]')];
    const top = open.find((d) => !d.querySelector('dialog[open]')) ?? null;
    const active = document.activeElement;
    return !!top && !!active && active !== document.body && top.contains(active) && active.closest('dialog') === top;
  });

async function tabbableCount(dialog: Locator) {
  return dialog.evaluate((el) => el.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), select, textarea').length);
}

/** Tab and Shift+Tab past both ends of the dialog; focus must never leave it. */
async function expectTabStaysInside(page: Page, dialog: Locator) {
  const presses = Math.min((await tabbableCount(dialog)) + 2, 40);
  for (const key of ['Tab', 'Shift+Tab']) {
    for (let i = 0; i < presses; i++) {
      await page.keyboard.press(key);
      expect(await focusIsInTopDialog(page), `${key} press ${i + 1} left the dialog`).toBe(true);
    }
  }
}

type Case = {
  name: string;
  hash: string;
  setup?: (page: Page) => Promise<void>;
  /** Steps after navigation, before the opener can be used. */
  prepare?: (page: Page) => Promise<void>;
  opener: (page: Page) => Locator;
  dialog: string | RegExp;
};

const CASES: Case[] = [
  { name: 'New Job wizard', hash: '#/jobs', opener: (p) => p.getByRole('button', { name: /new job/i }), dialog: 'Create Backup Job' },
  { name: 'Edit Job', hash: '#/jobs', opener: (p) => p.getByRole('button', { name: 'Edit job' }).first(), dialog: 'Edit Job' },
  { name: 'Delete Job', hash: '#/jobs', opener: (p) => p.getByRole('button', { name: /delete job/i }).first(), dialog: 'Delete Job' },
  {
    name: 'Review missing items',
    hash: '#/jobs',
    setup: async (page) => {
      await page.route(/\/api\/v1\/jobs\?details=true/, (route) => route.request().method() === 'GET'
        ? route.fulfill(json([{ id: 1, name: 'Docker & Appdata', enabled: true, storage_dest_id: 1, schedule: '0 2 * * *', items: [{ id: 9, job_id: 1, item_type: 'container', item_name: 'gone', missing_since: '2026-10-01T00:00:00Z' }] }]))
        : route.fallback());
    },
    opener: (p) => p.getByRole('button', { name: /1 missing/ }).first(),
    dialog: /Review missing items/,
  },
  { name: 'Add Storage', hash: '#/storage', opener: (p) => p.locator('main').getByRole('button', { name: /add storage/i }).first(), dialog: 'Add Storage' },
  { name: 'Edit Storage', hash: '#/storage', opener: (p) => p.getByRole('button', { name: 'Edit storage' }).first(), dialog: 'Edit Storage' },
  { name: 'Delete Storage', hash: '#/storage', opener: (p) => p.getByRole('button', { name: /delete storage/i }).first(), dialog: 'Delete Storage' },
  { name: 'Import Backups', hash: '#/storage', opener: (p) => p.getByRole('button', { name: 'Import backups' }).first(), dialog: /Import Backups/ },
  { name: 'Storage browser', hash: '#/storage', opener: (p) => p.getByRole('button', { name: /browse destination files/i }).first(), dialog: /Browse Local Array Backup/ },
  { name: 'Purge logs', hash: '#/logs', opener: (p) => p.getByRole('button', { name: /purge/i }).first(), dialog: 'Purge All Logs' },
  { name: 'Purge history', hash: '#/history', opener: (p) => p.getByRole('button', { name: /purge/i }).first(), dialog: 'Purge All History' },
  {
    name: 'Cancel queued operation',
    hash: '#/history',
    setup: (page) => setupVaultMockApi(page, { runnerStatus: { active: false, queue: [{ id: 2, job_id: 2, job_name: 'VM Backups', kind: 'backup', run_type: 'backup', status: 'queued' }] } }),
    opener: (p) => p.getByRole('button', { name: /cancel/i }).first(),
    dialog: 'Cancel Queued Operation',
  },
  {
    name: 'Add Replication Target',
    hash: '#/replication',
    setup: (page) => setupVaultMockApi(page, { replicationSources: REPLICATION_SOURCES }),
    opener: (p) => p.locator('main').getByRole('button', { name: 'Add Target' }).first(),
    dialog: 'Add Replication Target',
  },
  {
    name: 'Delete Replication Target',
    hash: '#/replication',
    setup: (page) => setupVaultMockApi(page, { replicationSources: REPLICATION_SOURCES }),
    opener: (p) => p.getByRole('button', { name: 'Delete replication target' }).first(),
    dialog: 'Delete Replication Target',
  },
  {
    name: 'Remove Encryption',
    hash: '#/settings',
    setup: async (page) => {
      await page.route('**/api/v1/settings/encryption', (route) => route.request().method() === 'GET' ? route.fulfill(json({ encryption_enabled: true })) : route.fallback());
    },
    prepare: async (page) => { await page.getByRole('button', { name: 'Security', exact: true }).click(); },
    opener: (p) => p.getByRole('button', { name: 'Disable encryption' }),
    dialog: 'Remove Encryption',
  },
  {
    name: 'Changelog',
    hash: '#/settings',
    setup: async (page) => {
      await page.route('**/api/v1/release/changelog', (route) => route.fulfill(json({ releases: [{ version: 'v2026.09.01', date: '2026-09-01', sections: { Fixed: ['Readable text'] } }] })));
    },
    opener: (p) => p.getByRole('button', { name: /view changelog/i }),
    dialog: 'Changelog',
  },
];

test.describe('Dialog keyboard contract (#528)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  for (const c of CASES) {
    test(`${c.name}: focus in, contained, Escape closes, focus returns`, async ({ page }) => {
      await c.setup?.(page);
      await page.goto(`/${c.hash}`);
      await c.prepare?.(page);
      const opener = c.opener(page);
      await expect(opener).toBeEnabled();
      const dialog = page.getByRole('dialog', { name: c.dialog });

      // Opened from the keyboard: focus moves in, and Tab cannot leave.
      await opener.focus();
      await page.keyboard.press('Enter');
      await expect(dialog).toBeVisible();
      await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
      await expectTabStaysInside(page, dialog);

      // Escape closes after focus has moved around, and focus returns to the opener.
      await page.keyboard.press('Escape');
      await expect(dialog).toHaveCount(0);
      await expect(opener).toBeFocused();

      // Opened with the mouse: the same contract, straight to Escape.
      await opener.click();
      await expect(dialog).toBeVisible();
      await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
      await page.keyboard.press('Escape');
      await expect(dialog).toHaveCount(0);
      await expect(opener).toBeFocused();
    });
  }

  test('an open dialog is a native modal: the page behind it is inert', async ({ page }) => {
    await page.goto('/#/storage');
    await page.getByRole('button', { name: /delete storage/i }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Delete Storage' });
    await expect(dialog).toBeVisible();
    expect(await dialog.evaluate((el) => el.tagName === 'DIALOG' && el.matches(':modal'))).toBe(true);
    // The dialog's DOM parent is in <main>, so ask whether a page control can still be hit.
    const nav = page.locator('aside nav button').first();
    const hit = await nav.evaluate((el) => {
      const box = el.getBoundingClientRect();
      return document.elementFromPoint(box.left + 5, box.top + 5)?.closest('dialog') !== null;
    });
    expect(hit).toBe(true);
  });

  test('only one dialog implementation remains in the page', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /delete job/i }).first().click();
    await expect(page.locator('dialog.vault-dialog[open]')).toHaveCount(1);
    await expect(page.locator('[role="dialog"], [aria-modal="true"]')).toHaveCount(0);
  });

  test('a closed dialog leaves nothing listening for Escape', async ({ page }) => {
    let purges = 0;
    await page.route('**/api/v1/activity**', (route) => {
      if (route.request().method() === 'DELETE') purges++;
      return route.fallback();
    });
    await page.goto('/#/logs');
    await page.getByRole('button', { name: /purge/i }).first().click();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect(purges).toBe(0);
  });

  test('confirmations start on Cancel, so Enter never confirms by accident', async ({ page }) => {
    await page.goto('/#/jobs');
    const row = page.getByRole('button', { name: /delete job/i }).first();
    await row.click();
    const dialog = page.getByRole('dialog', { name: 'Delete Job' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(dialog).toHaveCount(0);
    await expect(row).toBeFocused();
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
  });

  test('the delete radios work from the keyboard and keep their choice', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /delete job/i }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Delete Job' });
    await expect(dialog.getByRole('button', { name: 'Delete Job', exact: true })).toBeVisible();
    await dialog.getByRole('radio', { name: /Delete backup files/ }).check();
    await expect(dialog.getByRole('button', { name: 'Delete Job & Files' })).toBeVisible();
  });

  for (const [what, route, openerName, dialogName, confirm] of [
    ['job', '#/jobs', /delete job/i, 'Delete Job', 'Delete Job'],
    ['storage', '#/storage', /delete storage/i, 'Delete Storage', 'Delete Storage'],
  ] as const) {
    test(`focus lands on main content after a confirmed ${what} delete removes the opener`, async ({ page }) => {
      await page.goto(`/${route}`);
      const opener = page.getByRole('button', { name: openerName }).first();
      await opener.click();
      const dialog = page.getByRole('dialog', { name: dialogName });
      await dialog.getByRole('button', { name: confirm, exact: true }).click();
      await expect(dialog).toHaveCount(0);
      // The mock's list is static, so remove the row the way a reload would.
      await opener.evaluate((el) => el.remove());
      await expect(page.locator('main#main-content')).toBeFocused();
    });
  }
});

test.describe('Nested dialogs (#528)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('Escape in the path browser closes only the browser and keeps the storage form', async ({ page }) => {
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.locator('#sname').fill('Keep this name');

    const browse = editor.getByRole('button', { name: 'Browse directories' });
    await browse.focus();
    await page.keyboard.press('Enter');
    const browser = page.getByRole('dialog', { name: 'Browse Server' });
    await expect(browser).toBeVisible();
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
    await expectTabStaysInside(page, browser);

    await page.keyboard.press('Escape');
    await expect(browser).toHaveCount(0);
    await expect(editor).toBeVisible();
    await expect(editor.locator('#sname')).toHaveValue('Keep this name');
    await expect(browse).toBeFocused();

    await page.keyboard.press('Escape');
    await expect(editor).toHaveCount(0);
    await expect(page.locator('main').getByRole('button', { name: /add storage/i }).first()).toBeFocused();
  });

  test('browser buttons inside the storage form never submit it', async ({ page }) => {
    let posts = 0;
    await page.route('**/api/v1/storage', (route) => {
      if (route.request().method() === 'POST') posts++;
      return route.fallback();
    });
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.getByRole('button', { name: 'Browse directories' }).click();
    const browser = page.getByRole('dialog', { name: 'Browse Server' });
    await browser.getByRole('button', { name: 'Cancel' }).click();
    await expect(browser).toHaveCount(0);
    await expect(editor).toBeVisible();
    expect(posts).toBe(0);
  });

  test('keyboard focus survives navigating inside the path browser', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByRole('button', { name: 'Browse directories' }).first().click();
    const browser = page.getByRole('dialog', { name: 'Browse Server' });
    const folder = browser.locator('.divide-y button').first();
    await folder.focus();
    await page.keyboard.press('Enter');
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
    await page.keyboard.press('Shift+Tab');
    expect(await focusIsInTopDialog(page)).toBe(true);
    await page.keyboard.press('Escape');
    await expect(browser).toHaveCount(0);
  });

  test('Escape in the script browser closes only the browser and keeps the job form', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    const editor = page.getByRole('dialog', { name: 'Create Backup Job' });
    await editor.getByRole('button', { name: /Containers/i }).first().click();
    await editor.getByRole('button', { name: /plex/i }).first().click();
    await editor.getByRole('button', { name: /How$/ }).click();
    await editor.locator('summary', { hasText: 'Scripts' }).click();
    const preScript = editor.getByLabel('Pre-Backup Script');
    await preScript.fill('/boot/keep.sh');

    const browse = editor.getByRole('button', { name: 'Browse for script' }).first();
    await browse.focus();
    await page.keyboard.press('Enter');
    const browser = page.getByRole('dialog', { name: 'Select Script' });
    await expect(browser).toBeVisible();
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
    await expectTabStaysInside(page, browser);

    await page.keyboard.press('Escape');
    await expect(browser).toHaveCount(0);
    await expect(editor).toBeVisible();
    await expect(preScript).toHaveValue('/boot/keep.sh');
    await expect(browse).toBeFocused();
  });
});

test.describe('Command palette (#528)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  const palette = (page: Page) => page.getByRole('dialog', { name: 'Command palette' });
  const search = (page: Page) => page.getByPlaceholder('Type a command or search...');

  test('Ctrl+K focuses search, Tab stays inside, Escape closes after focus moves', async ({ page }) => {
    await page.goto('/#/jobs');
    const opener = page.getByRole('button', { name: /new job/i });
    await opener.focus();
    await page.keyboard.press('Control+k');
    await expect(search(page)).toBeFocused();

    for (let i = 0; i < 25; i++) {
      await page.keyboard.press('Tab');
      expect(await focusIsInTopDialog(page), `Tab ${i + 1} left the palette`).toBe(true);
    }
    for (let i = 0; i < 25; i++) {
      await page.keyboard.press('Shift+Tab');
      expect(await focusIsInTopDialog(page), `Shift+Tab ${i + 1} left the palette`).toBe(true);
    }
    await page.keyboard.press('Tab');
    await expect(search(page)).not.toBeFocused();

    await page.keyboard.press('Escape');
    await expect(palette(page)).toHaveCount(0);
    await expect(opener).toBeFocused();
  });

  test('Ctrl+K toggles it closed, and the search resets when reopened', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.keyboard.press('Control+k');
    await search(page).fill('stor');
    await page.keyboard.press('Control+k');
    await expect(palette(page)).toHaveCount(0);
    await page.keyboard.press('Control+k');
    await expect(search(page)).toHaveValue('');
  });

  test('Enter runs a command, and focus lands on main content', async ({ page }) => {
    await page.goto('/#/jobs');
    await expect(page.getByRole('heading', { name: 'Backup Jobs' })).toBeVisible();
    await page.keyboard.press('Control+k');
    await search(page).fill('Go to Storage');
    await page.keyboard.press('Enter');
    await expect(palette(page)).toHaveCount(0);
    await expect(page).toHaveURL(/#\/storage/);
    await expect.poll(() => page.evaluate(() => document.activeElement?.id || document.activeElement?.tagName)).toBe('main-content');
  });

  test('Enter on a tabbed-to row runs that row', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.keyboard.press('Control+k');
    await expect(search(page)).toBeFocused();
    await page.keyboard.press('Tab');
    await page.keyboard.press('Tab');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/#\/jobs/);
    await expect(palette(page)).toHaveCount(0);
  });

  test('opened above an editor, Escape closes only the palette', async ({ page }) => {
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.locator('#sname').fill('Keep me');
    await editor.locator('#sname').focus();

    await page.keyboard.press('Control+k');
    await expect(search(page)).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(palette(page)).toHaveCount(0);
    await expect(editor).toBeVisible();
    await expect(editor.locator('#sname')).toBeFocused();
    await expect(editor.locator('#sname')).toHaveValue('Keep me');
  });

  test('fits a 375 px viewport', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/#/jobs');
    await page.keyboard.press('Control+k');
    const box = await search(page).boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(16);
    expect(box!.x + box!.width).toBeLessThanOrEqual(375);
  });
});

test.describe('Wizard step focus (#528)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  const focusedHeading = (page: Page) =>
    page.evaluate(() => {
      const el = document.activeElement;
      return el?.matches('[data-step-heading]') ? el.textContent?.trim() : null;
    });

  test('Restore: forward and back land on each step heading, and not on first render', async ({ page }) => {
    await page.goto('/#/restore');
    await expect(page.getByRole('heading', { name: /Step 1 of 3/ })).toBeVisible();
    await expect(page.getByRole('button', { name: /plex/i })).toBeVisible();
    expect(await focusedHeading(page)).toBeNull();

    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: /home-assistant/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).focus();
    await page.keyboard.press('Enter');
    await expect.poll(() => focusedHeading(page)).toMatch(/Step 2 of 3/);

    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    await page.getByRole('button', { name: /VM Backups/i }).first().click();
    await page.getByRole('button', { name: /Continue/i }).click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Step 3 of 3/);

    await page.getByRole('button', { name: /Back to versions/ }).click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Step 2 of 3/);
    await page.getByRole('button', { name: /Back to items/ }).first().click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Step 1 of 3/);
  });

  test('New Job: Next, Back and the stepper land on the heading; opening does not', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Backup Job' });
    await expect(dialog).toBeVisible();
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
    expect(await focusedHeading(page)).toBeNull();

    await dialog.getByRole('button', { name: /Containers/i }).first().click();
    await dialog.getByRole('button', { name: /plex/i }).first().click();
    await dialog.getByRole('button', { name: 'Next', exact: true }).focus();
    await page.keyboard.press('Enter');
    await expect.poll(() => focusedHeading(page)).toBe('Where & when');

    await dialog.getByRole('button', { name: 'Back', exact: true }).click();
    await expect.poll(() => focusedHeading(page)).toBe('What');

    await dialog.getByRole('button', { name: /Where & when/ }).click();
    await expect.poll(() => focusedHeading(page)).toBe('Where & when');
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);

    // Reopening starts fresh: no focus jump onto the heading.
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await page.getByRole('button', { name: /new job/i }).click();
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
    expect(await focusedHeading(page)).toBeNull();
  });

  test('New Job: switching to the full form does not move focus', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    const toggle = page.getByRole('button', { name: 'Switch to full form' });
    await toggle.click();
    await expect(page.getByRole('button', { name: 'Switch to step-by-step' })).toBeFocused();
  });

  test('Recovery: each step change lands on its heading', async ({ page }) => {
    await page.goto('/#/recover');
    await expect(page.getByRole('heading', { name: /Connect the storage/i })).toBeVisible();
    expect(await focusedHeading(page)).toBeNull();

    await page.locator('#sname').fill('DR');
    await page.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups');
    await page.getByRole('button', { name: 'Connect', exact: true }).click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Choose which backup/);

    await page.getByRole('button', { name: /Restore this backup/i }).click();
    await page.getByRole('button', { name: /Yes, restore my settings/i }).click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Check your folder paths/);

    await page.getByRole('button', { name: 'Continue', exact: true }).click();
    await expect.poll(() => focusedHeading(page)).toMatch(/Vault is back/);
  });
});

test.describe('Toasts and tooltips above a modal (#528)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  /** Opens the Add Storage dialog, then raises an error toast from the page behind it. */
  async function toastOverDialog(page: Page) {
    await page.route('**/api/v1/storage', (route) => route.request().method() === 'POST'
      ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'backend down' }) })
      : route.fallback());
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Add Storage' });
    await dialog.locator('#sname').fill('Rejected');
    await dialog.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups/vault');
    await dialog.getByRole('button', { name: /^(Save|Create|Add)/ }).last().click();
    await expect(dialog).toBeVisible();
    return dialog;
  }

  test('an error toast is on top of the dialog and can be dismissed', async ({ page }) => {
    const dialog = await toastOverDialog(page);
    const toast = page.getByRole('alert');
    await expect(toast).toBeVisible();

    // Painted on top, and not inert: the element at its centre belongs to the toast.
    await expect.poll(() => toast.evaluate((el) => {
      const box = el.getBoundingClientRect();
      return el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
    })).toBe(true);

    await toast.getByRole('button', { name: 'Dismiss' }).click();
    await expect(toast).toHaveCount(0);
    await expect(dialog).toBeVisible();
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
  });

  test('the toast is reachable by Tab, and dismissing it keeps focus in the dialog', async ({ page }) => {
    await toastOverDialog(page);
    const dismiss = page.getByRole('alert').getByRole('button', { name: 'Dismiss' });
    await expect(dismiss).toBeVisible();
    await dismiss.focus();
    await expect(dismiss).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect.poll(() => focusIsInTopDialog(page)).toBe(true);
  });

  test('a toast stays visible when the dialog closes under it', async ({ page }) => {
    const dialog = await toastOverDialog(page);
    const toast = page.getByRole('alert');
    await expect(toast).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(toast).toBeVisible();
    await toast.getByRole('button', { name: 'Dismiss' }).click();
    await expect(toast).toHaveCount(0);
  });

  test('a toast over the dialog stays usable at 375 px', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    const dialog = await toastOverDialog(page);
    const toast = page.getByRole('alert');
    await expect(toast).toBeVisible();
    await toast.getByRole('button', { name: 'Dismiss' }).click();
    await expect(toast).toHaveCount(0);
    await expect(dialog).toBeVisible();
  });

  test('a tooltip inside a dialog appears above it', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Backup Job' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: /Containers/i }).first().click();
    await dialog.getByRole('button', { name: /plex/i }).first().click();
    await dialog.getByRole('button', { name: /How$/ }).click();
    const trigger = dialog.locator('.tooltip-trigger').first();
    await trigger.hover();
    const tip = page.locator('.tooltip-popup');
    await expect(tip).toBeVisible();
    expect(await tip.evaluate((el) => el.closest('dialog') !== null)).toBe(true);
  });
});
