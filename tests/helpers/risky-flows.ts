import { expect, type Locator, type Page, type Request } from '@playwright/test';

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

export const DB_PATH = '_vault/2026-10-01/vault.db';

async function press(locator: Locator, mode: 'mouse' | 'keyboard') {
  await expect(locator).toBeVisible();
  if (mode === 'mouse') {
    await locator.click();
  } else {
    await locator.focus();
    await locator.press('Enter');
  }
}

export async function mockVaultDbScan(page: Page) {
  await page.route(/\/storage\/\d+\/scan/, (route) =>
    route.fulfill(json({
      backups: [{ job_name: 'Old Job', path: 'old/job', item_count: 1, encrypted: false, created_at: '2026-09-01T00:00:00Z' }],
      vault_db: { path: DB_PATH, modified_at: '2026-10-01T04:00:00Z' },
    })),
  );
}

/** Job 2's pre-flight fails with a named check; job 1's still passes. */
export async function mockFailedPreflight(page: Page) {
  await page.route(/\/jobs\/2\/restore-points\/\d+\/preflight/, (route) =>
    route.fulfill(json({
      ok: false,
      checks: [
        { id: 'storage', label: 'Storage reachable', status: 'ok', detail: 'online' },
        { id: 'space', label: 'Free space', status: 'fail', detail: 'needs 40 GB more' },
      ],
    })),
  );
}

/** Walks the wizard to its review step with one item from each of two jobs. */
export async function reachRestoreReview(page: Page) {
  await page.goto('/#/restore');
  await page.getByRole('button', { name: /plex/i }).click();
  await page.getByRole('button', { name: /home-assistant/i }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
  await page.getByRole('button', { name: /VM Backups/i }).first().click();
  await page.getByRole('button', { name: /Continue/i }).click();
  await page.getByRole('button', { name: /Run all pre-flight checks/i }).click();
}

export interface RiskyFlow {
  name: string;
  word: string;
  title: string;
  confirmLabel: string;
  /** Mocks and navigation up to the page holding the opener. */
  arrive: (page: Page) => Promise<void>;
  /** The control that opens the dialog, after any steps needed to expose it. */
  opener: (page: Page, mode: 'mouse' | 'keyboard') => Promise<Locator>;
  isAction: (request: Request) => boolean;
  /** How many requests one confirmation sends. */
  calls: number;
  source: (string | RegExp)[];
  loss: (string | RegExp)[];
  /** Route glob for holding or failing the action. */
  actionUrl: RegExp;
  afterConfirm: (page: Page) => Promise<void>;
}

export const RISKY_FLOWS: RiskyFlow[] = [
  {
    name: 'Storage database replacement',
    word: 'REPLACE',
    title: 'Replace Vault Database',
    confirmLabel: 'Replace Database',
    arrive: async (page) => {
      await mockVaultDbScan(page);
      await page.goto('/#/storage');
      await expect(page.getByRole('heading', { name: 'Local Array Backup' })).toBeVisible();
    },
    opener: async (page, mode) => {
      await press(page.getByRole('button', { name: 'Import backups' }).first(), mode);
      const importDialog = page.getByRole('dialog', { name: /Import Backups/ });
      await press(importDialog.getByText('Restore Full Database'), mode);
      return importDialog.getByRole('button', { name: /Vault Database/ });
    },
    isAction: (r) => r.method() === 'POST' && /\/storage\/\d+\/restore-db$/.test(r.url()),
    calls: 1,
    source: ['Local Array Backup', DB_PATH, /2026|Oct/],
    loss: [/current Vault database \(jobs, storage destinations, history and settings\) is replaced/, 'Changes made after this backup was taken are lost', 'cannot be undone'],
    actionUrl: /\/storage\/\d+\/restore-db$/,
    afterConfirm: async (page) => {
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('alert').filter({ hasText: /restored successfully/i })).toBeVisible();
    },
  },
  {
    name: 'RestoreWizard restore anyway',
    word: 'RESTORE',
    title: 'Restore Despite Failed Checks',
    confirmLabel: 'Restore Anyway',
    arrive: async (page) => {
      await mockFailedPreflight(page);
      await reachRestoreReview(page);
      await expect(page.getByRole('button', { name: 'Restore anyway' })).toBeVisible();
    },
    opener: async (page) => page.getByRole('button', { name: 'Restore anyway' }),
    isAction: (r) => r.method() === 'POST' && /\/jobs\/\d+\/restore$/.test(r.url()),
    calls: 2,
    source: ['VM Backups', 'Docker & Appdata', 'Free space: needs 40 GB more', 'Original location'],
    loss: ['All 2 selected jobs are submitted, including 1 that failed pre-flight checks', 'Existing data at the destination is overwritten', 'incomplete data'],
    actionUrl: /\/jobs\/\d+\/restore$/,
    afterConfirm: async (page) => {
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('button', { name: 'Restore anyway' })).toHaveCount(0);
    },
  },
  {
    name: 'History purge',
    word: 'PURGE',
    title: 'Purge All History',
    confirmLabel: 'Purge Everything',
    arrive: async (page) => {
      await page.goto('/#/history');
      await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
    },
    opener: async (page) => page.getByRole('button', { name: /purge/i }).first(),
    isAction: (r) => r.method() === 'DELETE' && /\/history$/.test(r.url()),
    calls: 1,
    source: ['All jobs: the entire history, not only the filtered rows'],
    loss: ['All job-run history is deleted', 'All restore points are deleted', 'orphans', 'cannot be undone'],
    actionUrl: /\/history$/,
    afterConfirm: async (page) => {
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('button', { name: /purge/i }).first()).toBeDisabled();
    },
  },
  {
    name: 'Recovery wizard over an existing Vault',
    word: 'REPLACE',
    title: 'Replace Vault Database',
    confirmLabel: 'Replace Database',
    arrive: async (page) => {
      await page.goto('/#/recover');
      await page.locator('#sname').fill('Disaster Recovery Storage');
      await page.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups');
      await page.getByRole('button', { name: 'Connect', exact: true }).click();
      await expect(page.getByRole('heading', { name: /Choose which backup to restore/i })).toBeVisible();
    },
    opener: async (page) => page.getByRole('button', { name: 'Restore this backup' }),
    isAction: (r) => r.method() === 'POST' && /\/storage\/\d+\/restore-db$/.test(r.url()),
    calls: 1,
    source: ['Disaster Recovery Storage', 'vault.db.20261005.gz'],
    loss: [/This Vault currently has \d+ jobs? and \d+ storage destinations?, and they are replaced/, 'cannot be undone', 'not touched'],
    actionUrl: /\/storage\/\d+\/restore-db$/,
    afterConfirm: async (page) => {
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('heading', { name: /Check your folder paths/i })).toBeVisible();
    },
  },
];
