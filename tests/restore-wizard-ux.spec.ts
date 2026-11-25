import { test, expect, type Page } from '@playwright/test';
import { createInitialMockJobs, setupVaultMockApi } from './mocks/vault-api';

// Restore wizard UX fixes from the UI/UX audit (#707).

async function pickItems(page: Page, ...names: RegExp[]) {
  await page.goto('/#/restore');
  for (const name of names) await page.getByRole('button', { name }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
}

const dockerPoint = (page: Page) => page.getByRole('button', { name: /Docker & Appdata/i }).first();
const vmPoint = (page: Page) => page.getByRole('button', { name: /VM Backups/i }).first();

test.describe('Going back keeps chosen restore points and options (#702)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('keeps an unchanged job and clears only the job whose items changed', async ({ page }) => {
    await pickItems(page, /plex/i, /home-assistant/i);
    await dockerPoint(page).click();
    await vmPoint(page).click();
    await page.getByRole('button', { name: /Continue/ }).click();
    await page.getByRole('radio', { name: 'Custom destination' }).nth(1).check();
    await page.getByLabel('Custom restore destination').fill('/mnt/cache/vm-copy');

    // Step indicator back to step 1, then add another item from the Docker job.
    await page.getByRole('button', { name: 'Choose what to restore' }).click();
    await page.getByRole('button', { name: /nextcloud/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();

    await expect(page.getByTestId('restore-state-notice')).toHaveText(
      /restore point and options for Docker & Appdata were cleared because the items selected from that job changed/);
    await expect(vmPoint(page)).toHaveAttribute('aria-pressed', 'true');
    await expect(dockerPoint(page)).toHaveAttribute('aria-pressed', 'false');

    await dockerPoint(page).click();
    await page.getByRole('button', { name: /Continue/ }).click();
    await expect(page.getByLabel('Custom restore destination')).toHaveValue('/mnt/cache/vm-copy');
  });

  test('going back without changes keeps every choice', async ({ page }) => {
    await pickItems(page, /plex/i, /home-assistant/i);
    await dockerPoint(page).click();
    await vmPoint(page).click();
    await page.getByRole('button', { name: /Continue/ }).click();
    await page.getByRole('radio', { name: 'Custom destination' }).nth(1).check();
    await page.getByLabel('Custom restore destination').fill('/mnt/cache/vm-copy');

    await page.getByRole('button', { name: 'Choose what to restore' }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await expect(page.getByTestId('restore-state-notice')).toHaveCount(0);
    await expect(dockerPoint(page)).toHaveAttribute('aria-pressed', 'true');
    await page.getByRole('button', { name: /Continue/ }).click();
    await expect(page.getByLabel('Custom restore destination')).toHaveValue('/mnt/cache/vm-copy');
  });

  test('re-picking the same restore point keeps the file selection', async ({ page }) => {
    await pickItems(page, /plex/i);
    await dockerPoint(page).click();
    await page.locator('summary', { hasText: 'plex' }).click();
    await page.getByRole('button', { name: 'Expand all' }).click();
    await page.getByRole('treeitem', { name: /settings\.xml/ }).click();
    await expect(page.getByText('1 of 2 files selected')).toBeVisible();

    await page.getByRole('button', { name: 'Back to restore points' }).click();
    await dockerPoint(page).click();
    await expect(page.getByText('1 of 2 files selected')).toBeVisible();
    await expect(page.getByTestId('restore-state-notice')).toHaveCount(0);
  });

  test('choosing a different restore point resets only that job and says so', async ({ page }) => {
    await page.route('**/jobs/1/restore-points', route => route.fulfill({ json: [
      { id: 1001, job_id: 1, backup_type: 'full', created_at: '2026-10-01T02:00:00Z', size_bytes: 10, chain_depth: 1, metadata: JSON.stringify({ item_sizes: { plex: 5 } }) },
      { id: 1000, job_id: 1, backup_type: 'full', created_at: '2026-09-30T02:00:00Z', size_bytes: 10, chain_depth: 1, metadata: JSON.stringify({ item_sizes: { plex: 5 } }) },
    ] }));
    await pickItems(page, /plex/i);
    await dockerPoint(page).click();
    await page.locator('summary', { hasText: 'plex' }).click();
    await expect(page.getByText('All 2 files selected').first()).toBeVisible();
    await page.getByRole('button', { name: 'Back to restore points' }).click();
    await page.getByRole('button', { name: /^Restore point .*Docker & Appdata/ }).nth(1).click();
    await expect(page.getByTestId('restore-state-notice')).toHaveText(
      'Changing the restore point for Docker & Appdata reset its file selection and pre-flight checks.');
    await expect(page.locator('summary', { hasText: 'plex' })).toContainText('Click to browse contents');
  });

  test('a single job hides Continue until a restore point is chosen', async ({ page }) => {
    await pickItems(page, /plex/i);
    await expect(page.getByRole('button', { name: /Continue/ })).toHaveCount(0);
    await dockerPoint(page).click();
    await page.getByRole('button', { name: 'Back to restore points' }).click();
    await expect(page.getByRole('button', { name: /Continue/ })).toBeEnabled();
  });
});

test.describe('Restore point timeline (#704)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('deleting a point needs typed confirmation naming the date and job', async ({ page }) => {
    await pickItems(page, /plex/i);
    const deletes: string[] = [];
    page.on('request', req => { if (req.method() === 'DELETE') deletes.push(req.url()); });
    await page.getByRole('button', { name: /^Delete restore point/ }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Delete Restore Point' });
    await expect(dialog).toContainText('Docker & Appdata');
    await expect(dialog).toContainText('(full)');
    // The click must not select the point (the old nested button did).
    await expect(page.getByRole('heading', { name: /Step 2 of 3/ })).toBeVisible();
    const confirm = dialog.getByRole('button', { name: 'Delete Restore Point' });
    await expect(confirm).toBeDisabled();
    await dialog.getByRole('textbox').fill('DELETE');
    await page.route('**/jobs/1/restore-points/1001', route => route.fulfill({ status: 200, json: { message: 'deleted' } }));
    await confirm.click();
    await expect.poll(() => deletes.length).toBe(1);
    expect(deletes[0]).toContain('/jobs/1/restore-points/1001');
    await expect(page.getByText('No restore points found covering the selected items.')).toBeVisible();
  });

  test('a failed delete shows an error', async ({ page }) => {
    await page.route('**/jobs/1/restore-points/1001', route => route.request().method() === 'DELETE'
      ? route.fulfill({ status: 500, json: { error: 'storage offline' } })
      : route.fallback());
    await pickItems(page, /plex/i);
    await page.getByRole('button', { name: /^Delete restore point/ }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Delete Restore Point' });
    await dialog.getByRole('textbox').fill('DELETE');
    await dialog.getByRole('button', { name: 'Delete Restore Point' }).click();
    await expect(page.getByRole('alert').filter({ hasText: /Could not delete the restore point .*storage offline/ })).toBeVisible();
    await expect(dockerPoint(page)).toBeVisible();
  });
});

test.describe('Restore locations and clearing confirmation (#700)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('step 3 lists each location; clearing needs typed RESTORE, merging stays one click', async ({ page }) => {
    const restores: unknown[] = [];
    page.on('request', req => { if (req.method() === 'POST' && req.url().endsWith('/jobs/1/restore')) restores.push(req.postDataJSON()); });
    await pickItems(page, /plex/i);
    await dockerPoint(page).click();
    const targets = page.getByTestId('restore-targets');
    await expect(targets).toContainText("Each volume's original host path");
    await expect(targets).toContainText('Files here that are not in the backup will be deleted');
    await expect(page.getByTestId('restore-warning')).toContainText('This will delete files that are not in the backup');

    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Restore and Delete Extra Files' });
    await expect(dialog).toContainText('plex (Container), Docker & Appdata');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    expect(restores).toHaveLength(0);

    await page.getByRole('checkbox', { name: /Clear the destination first/ }).uncheck();
    await expect(targets).toContainText('Files here that are not in the backup are kept');
    await expect(page.getByTestId('restore-warning')).toContainText('Files that are not in the backup are kept');
    await page.getByRole('radio', { name: 'Custom destination' }).check();
    await page.getByLabel('Custom restore destination').fill('/mnt/user/restore-copy');
    await expect(targets).toContainText('/mnt/user/restore-copy');
    await page.getByLabel(/I understand the live container/).check();
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).click();
    await expect.poll(() => restores.length).toBe(1);
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect(restores[0]).toMatchObject({ clean_destination: false, destination: '/mnt/user/restore-copy' });
  });
});

test.describe('Stopping a restore and next steps (#701)', () => {
  test('stops the exact Restore Run, then offers View run and Retry failed items', async ({ page }) => {
    let status: Record<string, unknown> = { active: false };
    const failedRun = {
      id: 7, job_id: 1, run_type: 'restore', status: 'cancelled', items_done: 0, items_failed: 0, items_total: 2,
      log: JSON.stringify([{ name: 'plex', status: 'cancelled' }, { name: 'nextcloud', status: 'not_attempted' }]),
    };
    await setupVaultMockApi(page, { runnerStatus: () => status, runResult: () => failedRun });
    await page.addInitScript(() => {
      Object.assign(window, { __VAULT_RUNTIME_CONFIG__: {
        mode: 'unraid-proxy', liveMode: 'poll', csrfToken: 'csrf-fixture',
        restoreRequestActionPath: '/plugins/vault/include/restore-request.php',
      } });
    });
    const stops: Record<string, string>[] = [];
    await page.route('**/plugins/vault/include/restore-request.php', async route => {
      stops.push(Object.fromEntries(new URLSearchParams(route.request().postData() || '')));
      await route.fulfill({ status: 202, json: { run_id: 7, status: 'stopping' } });
    });

    await pickItems(page, /plex/i, /nextcloud/i);
    await dockerPoint(page).click();
    await page.getByRole('checkbox', { name: /Clear the destination first/ }).uncheck();
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('heading', { name: 'Restore in progress' })).toBeFocused();

    status = { active: true, job_id: 1, run_id: 7, run_type: 'restore', items_total: 2 };
    const stop = page.getByRole('button', { name: 'Stop restore' });
    await expect(stop).toBeVisible();
    await stop.click();
    const dialog = page.getByRole('dialog', { name: 'Stop Restore Run #7' });
    await expect(dialog).toContainText('Stopping can leave the restore location partly restored.');
    await dialog.getByRole('button', { name: 'Stop restore' }).click();
    await expect.poll(() => stops.length).toBe(1);
    expect(stops[0]).toEqual({ action: 'stop', id: '7', csrf_token: 'csrf-fixture' });
    await expect(page.getByRole('heading', { name: 'Stopping restore…' })).toBeVisible();

    status = { active: false };
    await expect(page.getByText('Restore cancelled', { exact: true })).toBeVisible();
    await expect(page.getByTestId('view-run')).toHaveAttribute('href', '#/history?run=7');
    await page.getByRole('button', { name: 'Retry failed items' }).click();
    await expect(page.getByTestId('restore-state-notice')).toHaveText(/Selected the 2 items that did not restore/);
    await expect(page.getByRole('button', { name: 'Start Restore', exact: true })).toBeDisabled();
  });

  test('without the signed-in Unraid page the wizard says how to stop', async ({ page }) => {
    let status: Record<string, unknown> = { active: false };
    await setupVaultMockApi(page, { runnerStatus: () => status, runResult: () => ({ id: 7, job_id: 1, run_type: 'restore', status: 'completed', items_done: 1, items_total: 1, items_failed: 0 }) });
    await page.addInitScript(() => {
      Object.assign(window, { __VAULT_RUNTIME_CONFIG__: { mode: 'direct', liveMode: 'poll' } });
    });
    await pickItems(page, /plex/i);
    await dockerPoint(page).click();
    await page.getByRole('checkbox', { name: /Clear the destination first/ }).uncheck();
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).click();
    status = { active: true, job_id: 1, run_id: 7, run_type: 'restore', items_total: 1 };
    await expect(page.getByText('To stop this restore, open Vault from the signed-in Unraid page.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Stop restore' })).toHaveCount(0);
    status = { active: false };
    await expect(page.getByText('Restore completed successfully', { exact: true })).toBeVisible();
    await expect(page.getByTestId('view-run')).toHaveAttribute('href', '#/history?run=7');
    await expect(page.getByRole('button', { name: 'Retry failed items' })).toHaveCount(0);
  });
});

test.describe('Items only restore points still hold (#669)', () => {
  test('a container removed from its job stays selectable and restorable', async ({ page }) => {
    const jobs = createInitialMockJobs();
    jobs[0].historical_items = [{ item_name: 'sonarr', item_type: 'container', last_backup_at: '2026-10-01T02:00:00Z', restore_points: 1 }];
    await setupVaultMockApi(page, { jobs });
    await page.route('**/jobs/1/restore-points', route => route.fulfill({ json: [{
      id: 1001, job_id: 1, backup_type: 'full', created_at: '2026-10-01T02:00:00Z', size_bytes: 10, chain_depth: 1,
      metadata: JSON.stringify({ item_sizes: { plex: 4, sonarr: 6 }, item_types: { plex: 'container', sonarr: 'container' } }),
    }] }));
    const listing = page.waitForRequest(req => req.url().includes('/jobs?details=true&history=true'));
    await page.goto('/#/restore');
    await listing;
    const card = page.getByRole('button', { name: /sonarr/i });
    await expect(card).toContainText('No longer in a job');
    await expect(card).toContainText('Container');
    await card.click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await dockerPoint(page).click();

    const preflight = page.waitForRequest(req => req.url().endsWith('/preflight'));
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    expect((await preflight).postDataJSON().items).toEqual(['sonarr']);
    const restore = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/jobs/1/restore'));
    await page.getByRole('checkbox', { name: /Clear the destination first/ }).uncheck();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).click();
    expect((await restore).postDataJSON()).toMatchObject({ restore_point_id: 1001, items: ['sonarr'] });
  });
});
