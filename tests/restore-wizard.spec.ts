import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import { setupVaultMockApi, type VaultMockOptions } from './mocks/vault-api';

// Clearing the destination is on by default, so Start Restore asks for a
// typed RESTORE that lists the affected locations (#700).
async function confirmClearingRestore(page: Page) {
  const dialog = page.getByRole('dialog', { name: 'Restore and Delete Extra Files' });
  await dialog.getByRole('textbox').fill('RESTORE');
  await dialog.getByRole('button', { name: 'Start Restore', exact: true }).click();
}

test.describe('Restore Run outcomes (#654)', () => {
  const idle = { active: false };
  const active = { active: true, job_id: 1, run_id: 7, run_type: 'restore', items_total: 1 };
  const run = { id: 7, job_id: 1, run_type: 'restore', status: 'completed', items_done: 1, items_failed: 0, items_total: 1 };

  async function submitRestore(page: Page) {
    await page.goto('/#/restore');
    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await page.getByRole('button', { name: 'Start Restore', exact: true }).click();
    await confirmClearingRestore(page);
  }

  async function startRestore(page: Page, runResult: NonNullable<VaultMockOptions['runResult']>) {
    let status: Record<string, unknown> = idle;
    await setupVaultMockApi(page, { runnerStatus: () => status, runResult });
    await page.addInitScript(() => {
      Object.assign(window, { __VAULT_RUNTIME_CONFIG__: { mode: 'direct', liveMode: 'poll' } });
    });
    await submitRestore(page);
    status = active;
    await expect(page.getByText('Restore Log', { exact: true })).toBeVisible();
    return (next: Record<string, unknown>) => { status = next; };
  }

  for (const outcome of [
    { status: 'completed', done: 1, failed: 0, total: 1, banner: 'Restore completed successfully', toast: /1\/1 items restored/ },
    { status: 'failed', done: 0, failed: 1, total: 1, banner: 'Restore failed', toast: /1 items failed/ },
    { status: 'partial', done: 1, failed: 1, total: 2, banner: 'Restore completed with errors', toast: /1\/2 restored, 1 failed/ },
  ]) {
    test(`uses the recorded ${outcome.status} outcome in the wizard and toast`, async ({ page }) => {
      const setStatus = await startRestore(page, () => ({
        ...run, status: outcome.status, items_done: outcome.done, items_failed: outcome.failed, items_total: outcome.total,
      }));
      setStatus(idle);
      await expect(page.getByText(outcome.banner, { exact: true })).toBeVisible();
      await expect(page.getByRole('alert').filter({ hasText: outcome.toast })).toBeVisible();
    });
  }

  test('waits for a temporarily unavailable outcome and retries', async ({ page }) => {
    let available = false;
    const setStatus = await startRestore(page, () => available ? run : 404);
    const unavailable = page.waitForResponse(response => response.url().endsWith('/runs/7') && response.status() === 404);
    setStatus(idle);
    await unavailable;
    await expect(page.getByRole('button', { name: 'Restoring...' })).toBeVisible();
    await expect(page.getByText(/Restore (failed|completed successfully)/)).toHaveCount(0);
    available = true;
    await expect(page.getByText('Restore completed successfully', { exact: true })).toBeVisible();
    await expect(page.getByRole('alert').filter({ hasText: /1\/1 items restored/ })).toBeVisible();
  });

  test('ignores a late outcome after a newer run starts for the same job', async ({ page }) => {
    let releasePrevious: () => void = () => {};
    const held = new Promise<void>(resolve => { releasePrevious = resolve; });
    const setStatus = await startRestore(page, async id => {
      if (id === 7) {
        await held;
        return run;
      }
      return { ...run, id: 8, status: 'failed', items_done: 0, items_failed: 1 };
    });
    const nextRun = page.waitForRequest(req => req.url().includes('/runs/8/logs'));
    const previousResult = page.waitForResponse(res => res.url().endsWith('/runs/7'));
    setStatus({ ...active, run_id: 8 });
    await nextRun;
    releasePrevious();
    await (await previousResult).finished();
    await expect(page.getByRole('button', { name: 'Restoring...' })).toBeVisible();
    await expect(page.getByText(/Restore completed successfully/)).toHaveCount(0);
    setStatus(idle);
    await expect(page.getByText('Restore failed', { exact: true })).toBeVisible();
    await expect(page.getByRole('alert').filter({ hasText: /1 items failed/ })).toBeVisible();
  });

  test('uses a complete WebSocket result directly', async ({ page }) => {
    let lookups = 0;
    let socket: WebSocketRoute | undefined;
    await setupVaultMockApi(page, { runResult: () => { lookups += 1; return 404; } });
    await page.routeWebSocket('**/api/v1/ws', ws => { socket = ws; });
    await submitRestore(page);
    await expect.poll(() => socket).toBeDefined();
    socket!.send(JSON.stringify({ ...active, type: 'job_run_started' }));
    await expect(page.getByText('Restore Log', { exact: true })).toBeVisible();
    socket!.send(JSON.stringify({ ...run, type: 'job_run_completed', run_id: 7 }));
    await expect(page.getByText('Restore completed successfully', { exact: true })).toBeVisible();
    await expect(page.getByRole('alert').filter({ hasText: /1\/1 items restored/ })).toBeVisible();
    expect(lookups).toBe(0);
  });

  test('retries the outcome after reconnect without another snapshot', async ({ page }) => {
    const sockets: WebSocketRoute[] = [];
    let available = false;
    let attempts = 0;
    await setupVaultMockApi(page, { runResult: () => {
      attempts += 1;
      return available ? run : 503;
    } });
    await page.routeWebSocket('**/api/v1/ws', ws => { sockets.push(ws); });
    await submitRestore(page);
    await expect.poll(() => sockets.length).toBe(1);
    sockets[0].send(JSON.stringify({ ...active, type: 'job_run_started' }));
    await expect(page.getByText('Restore Log', { exact: true })).toBeVisible();

    const unavailable = page.waitForResponse(res => res.url().endsWith('/runs/7') && res.status() === 503);
    await sockets[0].close();
    await expect.poll(() => sockets.length).toBe(2);
    await unavailable;
    await expect.poll(() => attempts).toBe(2);
    await expect(page.getByRole('button', { name: 'Restoring...' })).toBeVisible();

    available = true;
    await expect(page.getByText('Restore completed successfully', { exact: true })).toBeVisible();
    await expect(page.getByRole('alert').filter({ hasText: /1\/1 items restored/ })).toBeVisible();
    expect(sockets).toHaveLength(2);
  });
});

test.describe('Scoped restore preflight (#655)', () => {
  const job1Preflight = '**/jobs/1/restore-points/*/preflight';
  const job2Preflight = '**/jobs/2/restore-points/*/preflight';

  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/restore');
  });

  test('checks the selected VM and displays the advisory staging warning', async ({ page }) => {
    const detail = '13.0 GiB free at /mnt/cache (cache). Estimated space for the selected items is 20.0 GiB.';
    await page.route(job2Preflight, route => route.fulfill({
      json: { ok: true, checks: [{ id: 'space', label: 'Restore staging space', status: 'warn', detail }] },
    }));
    await page.getByRole('button', { name: /home-assistant/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /VM Backups/i }).first().click();

    const request = page.waitForRequest(job2Preflight);
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    expect((await request).postDataJSON()).toEqual({ items: ['home-assistant'] });
    await expect(page.getByText(detail, { exact: false })).toBeVisible();
    await expect(page.getByLabel('warning', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Start Restore', exact: true })).toBeEnabled();
  });

  test('requires new checks after the item selection changes', async ({ page }) => {
    const next = page.getByRole('button', { name: 'Next', exact: true });
    const point = page.getByRole('button', { name: /Docker & Appdata/i }).first();
    const check = page.getByRole('button', { name: 'Run checks', exact: true });
    const start = page.getByRole('button', { name: 'Start Restore', exact: true });
    await page.getByRole('button', { name: /plex/i }).click();
    await next.click();
    await point.click();
    await check.click();
    await expect(start).toBeEnabled();

    await page.getByRole('button', { name: 'Back to restore points', exact: true }).click();
    await page.getByRole('button', { name: 'Back to items', exact: true }).click();
    await page.getByRole('button', { name: /nextcloud/i }).click();
    await next.click();
    await point.click();
    await expect(start).toBeDisabled();

    const request = page.waitForRequest(job1Preflight);
    await check.click();
    expect((await request).postDataJSON().items).toEqual(['plex', 'nextcloud']);
    await expect(start).toBeEnabled();
  });

  test('keeps the newer check when an old destination response arrives late', async ({ page }) => {
    let releaseOld!: () => void;
    const holdOld = new Promise<void>(resolve => { releaseOld = resolve; });
    await page.route(job2Preflight, async route => {
      if (route.request().postDataJSON().destination) {
        await route.fulfill({ json: { ok: true, checks: [{ id: 'space', label: 'New staging result', status: 'ok' }] } });
      } else {
        await holdOld;
        await route.fulfill({ json: { ok: false, checks: [{ id: 'present', label: 'Old result', status: 'fail' }] } });
      }
    });
    await page.getByRole('button', { name: /home-assistant/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /VM Backups/i }).first().click();

    const first = page.waitForRequest(job2Preflight);
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await first;
    await page.getByLabel('Custom destination', { exact: true }).check();
    await page.getByLabel(/Custom restore destination/i).fill('/mnt/cache/vm-copy');
    const start = page.getByRole('button', { name: 'Start Restore', exact: true });
    await expect(start).toBeDisabled();
    await expect(page.getByText(/Inputs changed since the last check/)).toBeVisible();
    await page.getByRole('button', { name: 'Run checks', exact: true }).click();
    await expect(page.getByText('New staging result')).toBeVisible();
    await expect(start).toBeEnabled();

    const late = page.waitForResponse(res => res.url().endsWith('/preflight') && !res.request().postDataJSON().destination);
    releaseOld();
    await (await late).finished();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await expect(page.getByText('New staging result')).toBeVisible();
    await expect(page.getByText('Old result')).toHaveCount(0);
    await expect(start).toBeEnabled();
  });
});

test.describe('Restore Wizard Multi-Job Flow (#438)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('aggregates items across multiple jobs and guides user through restore plan', async ({ page }) => {
    await page.goto('/#/restore');

    // Step 1: Select items across jobs
    await expect(page.getByText('Choose what to restore').first()).toBeVisible();
    await expect(page.getByText(/Select items to restore/i)).toBeVisible();

    // Verify items from Job 1 and Job 2 are present
    const plexItem = page.getByRole('button', { name: /plex/i });
    const haItem = page.getByRole('button', { name: /home-assistant/i });

    await expect(plexItem).toBeVisible();
    await expect(haItem).toBeVisible();

    // Select items from both jobs
    await plexItem.click();
    await expect(page.getByText(/1 item selected/i)).toBeVisible();

    await haItem.click();
    await expect(page.getByText(/2 items selected/i)).toBeVisible();

    // Advance to Step 2
    const nextBtn = page.getByRole('button', { name: 'Next', exact: true });
    await expect(nextBtn).toBeEnabled();
    await nextBtn.click();

    // Step 2: Aggregated Restore Point Timeline
    await expect(page.getByText(/Your selection comes from 2 backup jobs/i)).toBeVisible();

    // Timeline should render restore points with source job badges
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('VM Backups').first()).toBeVisible();

    // Select the restore point cards for both jobs
    const dockerPoint = page.getByRole('button', { name: /Docker & Appdata/i }).first();
    const vmPoint = page.getByRole('button', { name: /VM Backups/i }).first();

    await expect(dockerPoint).toBeVisible();
    await expect(vmPoint).toBeVisible();

    await dockerPoint.click();
    await vmPoint.click();

    // Step 2 Continue button should now be enabled
    const continueBtn = page.getByRole('button', { name: /Continue/i });
    await expect(continueBtn).toBeEnabled();
    await continueBtn.click();

    // Step 3: Multi-Job Restore Plan & Options
    await expect(page.getByText(/Multi-Job Restore Plan/i)).toBeVisible();
    await expect(page.getByText(/Total Items/i)).toBeVisible();

    // Verify each job unit has its configuration section
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('VM Backups').first()).toBeVisible();

    // Verify destination controls exist (Original location selected by default)
    await expect(page.getByText(/Original location/i).first()).toBeVisible();

    // Set up request listener to capture restore submissions
    const restoreRequests: Array<{ url: string; postData: unknown }> = [];
    page.on('request', req => {
      if (req.url().endsWith('/restore') && req.method() === 'POST') {
        restoreRequests.push({ url: req.url(), postData: req.postDataJSON() });
      }
    });

    const preflights = [1, 2].map(id => page.waitForRequest(req =>
      req.method() === 'POST' && req.url().includes(`/jobs/${id}/restore-points/`) && req.url().endsWith('/preflight')));
    // Run pre-flight checks to enable restore
    await page.getByRole('button', { name: /Run all pre-flight checks/i }).click();
    const [job1Preflight, job2Preflight] = await Promise.all(preflights);
    expect(job1Preflight.postDataJSON().items).toEqual(['plex']);
    expect(job2Preflight.postDataJSON().items).toEqual(['home-assistant']);

    // Verify pre-flight checks passed
    await expect(page.getByLabel('passed').first()).toBeVisible();

    // Intercept Job 1 restore request to verify sequential execution:
    // Job 2 restore request must not start until Job 1 restore request completes.
    let releaseJob1Restore: () => void = () => {};
    const job1HoldPromise = new Promise<void>(resolve => {
      releaseJob1Restore = resolve;
    });

    await page.route('**/jobs/1/restore', async (route) => {
      await job1HoldPromise;
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'restore started', restore_point_id: 1001, items: 1 }),
      });
    });

    // Start restore action becomes enabled
    const startRestoreBtn = page.getByRole('button', { name: /^Start Restore/i });
    await expect(startRestoreBtn).toBeVisible();
    await expect(startRestoreBtn).toBeEnabled();
    await startRestoreBtn.click();
    await confirmClearingRestore(page);

    // Verify Job 1 was submitted first while Job 2 has not started yet
    await expect.poll(() => restoreRequests.length).toBe(1);
    expect(restoreRequests[0].url).toContain('/jobs/1/restore');
    expect(restoreRequests.some(r => r.url.endsWith('/jobs/2/restore'))).toBe(false);

    // A destination edited during the first request was never checked or submitted.
    await page.getByRole('radio', { name: 'Custom destination' }).nth(1).check();
    await page.getByLabel('Custom restore destination').fill('/mnt/cache/unchecked');

    // Release Job 1 response
    releaseJob1Restore();

    // Now Job 2 request should be submitted sequentially
    await expect.poll(() => restoreRequests.length).toBe(2);
    const job1Req = restoreRequests.find(r => r.url.endsWith('/jobs/1/restore'));
    const job2Req = restoreRequests.find(r => r.url.endsWith('/jobs/2/restore'));
    expect(job1Req).toBeDefined();
    expect(job2Req).toBeDefined();
    expect(job1Req?.postData).toMatchObject({
      restore_point_id: 1001,
      items: ['plex'],
    });
    expect(job2Req?.postData).toMatchObject({
      restore_point_id: 2001,
      items: ['home-assistant'],
    });
    expect(job2Req?.postData).not.toHaveProperty('destination');
  });

  test('supports custom destination path configuration on a restore unit', async ({ page }) => {
    await page.goto('/#/restore');

    // Select plex item
    const plexItem = page.getByRole('button', { name: /plex/i });
    await plexItem.click();

    // Advance to Step 2
    await page.getByRole('button', { name: 'Next', exact: true }).click();

    // Select restore point (in single-job flow, selecting the point covering all items auto-advances to Step 3)
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();

    // Switch to Custom destination
    await page.getByLabel(/Custom destination/i).first().click();

    // Custom restore destination input is now visible
    const destInput = page.getByLabel(/Custom restore destination/i);
    await expect(destInput).toBeVisible();

    // Type a custom destination path
    await destInput.fill('/mnt/user/custom-restore');
    await expect(destInput).toHaveValue('/mnt/user/custom-restore');

    // Acknowledge container recreation/remap warning
    await page.getByLabel(/I understand the live container will be recreated and remapped/i).check();

    // Set up request listener to capture restore submission
    const restoreRequests: Array<{ url: string; postData: unknown }> = [];
    page.on('request', req => {
      if (req.url().endsWith('/restore') && req.method() === 'POST') {
        restoreRequests.push({ url: req.url(), postData: req.postDataJSON() });
      }
    });

    // Run pre-flight check
    await page.getByRole('button', { name: /Run checks/i }).click();
    await expect(page.getByLabel('passed').first()).toBeVisible();

    // Start restore and verify custom destination is submitted in the payload
    const startRestoreBtn = page.getByRole('button', { name: /^Start Restore/i });
    await expect(startRestoreBtn).toBeEnabled();
    await startRestoreBtn.click();
    await confirmClearingRestore(page);

    await expect.poll(() => restoreRequests.length).toBe(1);
    expect(restoreRequests[0].url).toContain('/jobs/1/restore');
    expect(restoreRequests[0].postData).toMatchObject({
      destination: '/mnt/user/custom-restore',
      restore_point_id: 1001,
      items: ['plex'],
    });
  });

  test('allows navigating backward between wizard steps', async ({ page }) => {
    await page.goto('/#/restore');

    const plexItem = page.getByRole('button', { name: /plex/i });
    await plexItem.click();

    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await expect(page.getByText('Back to items')).toBeVisible();

    // Navigate back to Step 1
    await page.getByText('Back to items').click();
    await expect(page.getByText('Choose what to restore').first()).toBeVisible();
    await expect(page.getByText(/1 item selected/i)).toBeVisible();
  });
});

test.describe('Restore Wizard partial-restore file picker (#449)', () => {
  const contentsGlob = '**/jobs/1/restore-points/1001/contents*';

  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  // Select plex, pick its restore point (single-job flow auto-advances to the
  // plan step) and return the file-picker summary for plex.
  async function openPlexPlan(page: import('@playwright/test').Page) {
    await page.goto('/#/restore');
    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    const summary = page.locator('summary', { hasText: 'plex' });
    await expect(summary).toBeVisible();
    return summary;
  }

  test('names the search, sort and per-item path filter fields', async ({ page }) => {
    await page.goto('/#/restore');
    await page.getByRole('textbox', { name: 'Search items or jobs' }).fill('plex');
    await expect(page.getByRole('button', { name: /home-assistant/i })).toHaveCount(0);
    await page.getByRole('textbox', { name: 'Search items or jobs' }).clear();
    await expect(page.getByRole('combobox', { name: 'Sort:' })).toHaveValue('alpha-asc');
    await page.getByLabel('Sort:').selectOption('alpha-desc');
    await expect(page.getByLabel('Sort:')).toHaveValue('alpha-desc');

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByRole('textbox', { name: 'Filter paths in plex' })).toBeVisible();
  });

  test('shows loading until a slow listing arrives, then the file tree', async ({ page }) => {
    let release: () => void = () => {};
    const held = new Promise<void>(resolve => { release = resolve; });
    await page.route(contentsGlob, async route => {
      await held;
      await route.fallback();
    });

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('Loading file list…')).toBeVisible();

    release();
    await expect(page.getByLabel('Select config')).toBeVisible();
    await expect(page.getByText('All 2 files selected').first()).toBeVisible();
  });

  test('a timed-out listing offers Retry instead of a whole-archive fallback', async ({ page }) => {
    let calls = 0;
    await page.route(contentsGlob, async route => {
      calls += 1;
      if (calls === 1) {
        return route.fulfill({
          status: 504,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'vault daemon request timed out' }),
        });
      }
      return route.fallback();
    });

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('Could not load the file list: vault daemon request timed out')).toBeVisible();
    await expect(page.getByText(/whole-archive|whole item is restored/i)).toHaveCount(0);

    await page.getByRole('button', { name: 'Retry', exact: true }).click();
    await expect(page.getByLabel('Select config')).toBeVisible();
    expect(calls).toBe(2);

    // Deselect one file; the remaining one is submitted as a partial restore.
    await page.getByRole('button', { name: 'Expand config' }).click();
    await page.getByLabel('Select database.db').uncheck();
    await expect(page.getByText('Partial restore: restoring 1 of 2 selected files.')).toBeVisible();

    const restoreRequests: unknown[] = [];
    page.on('request', req => {
      if (req.url().endsWith('/jobs/1/restore') && req.method() === 'POST') {
        restoreRequests.push(req.postDataJSON());
      }
    });
    await page.getByRole('button', { name: /Run checks/i }).click();
    await expect(page.getByLabel('passed').first()).toBeVisible();
    await page.getByRole('button', { name: /^Start Restore/i }).click();

    await expect.poll(() => restoreRequests.length).toBe(1);
    expect(restoreRequests[0]).toMatchObject({
      items: ['plex'],
      file_paths: { plex: ['config/settings.xml'] },
    });
  });

  test('a failed listing blocks restore until the user retries or picks the whole item', async ({ page }) => {
    await page.route(contentsGlob, route => route.fulfill({
      status: 504,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'vault daemon request timed out' }),
    }));

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('Could not load the file list: vault daemon request timed out')).toBeVisible();
    await expect(page.getByText('Wait for the file list to load, retry it, or choose to restore the whole item.')).toBeVisible();

    await page.getByRole('button', { name: /Run checks/i }).click();
    await expect(page.getByLabel('passed').first()).toBeVisible();
    const startRestoreBtn = page.getByRole('button', { name: /^Start Restore/i });
    await expect(startRestoreBtn).toBeDisabled();

    await page.getByRole('button', { name: 'Restore whole item' }).click();
    await expect(startRestoreBtn).toBeEnabled();
    await expect(summary).toContainText('Whole item will be restored');

    const restoreRequests: Record<string, unknown>[] = [];
    page.on('request', req => {
      if (req.url().endsWith('/jobs/1/restore') && req.method() === 'POST') {
        restoreRequests.push(req.postDataJSON());
      }
    });
    await startRestoreBtn.click();
    await confirmClearingRestore(page);
    await expect.poll(() => restoreRequests.length).toBe(1);
    expect(restoreRequests[0]).toMatchObject({ items: ['plex'] });
    expect(restoreRequests[0]).not.toHaveProperty('file_paths');
  });

  test('a listing that lands after the restore point changed is discarded', async ({ page }) => {
    let release: () => void = () => {};
    const held = new Promise<void>(resolve => { release = resolve; });
    let calls = 0;
    await page.route(contentsGlob, async route => {
      calls += 1;
      if (calls === 1) await held;
      await route.fallback();
    });
    // An older second point, so the user can switch points (#702: re-picking
    // the same point keeps its file list).
    await page.route('**/jobs/1/restore-points', route => route.fulfill({ json: [
      { id: 1001, job_id: 1, backup_type: 'full', created_at: '2026-10-01T02:00:00Z', size_bytes: 10, chain_depth: 1, metadata: JSON.stringify({ item_sizes: { plex: 5, nextcloud: 5 } }) },
      { id: 1000, job_id: 1, backup_type: 'full', created_at: '2026-09-30T02:00:00Z', size_bytes: 10, chain_depth: 1, metadata: JSON.stringify({ item_sizes: { plex: 5, nextcloud: 5 } }) },
    ] }));

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('Loading file list…')).toBeVisible();

    // Go back and pick another point while the first listing is in flight;
    // this resets the picker, so the late response must not repopulate it.
    await page.getByRole('button', { name: /Choose a restore point/ }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).nth(2).click();
    await expect(summary).toContainText('Click to browse contents');

    release();
    await expect.poll(() => calls).toBe(1);
    await page.waitForTimeout(250);
    await expect(summary).toContainText('Click to browse contents');
    await expect(page.getByLabel('Select config')).toHaveCount(0);
  });

  test('an unbrowsable restore point explains the whole-item fallback', async ({ page }) => {
    await page.route(contentsGlob, route => route.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'no readable tar index sidecar found for this item' }),
    }));

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('If you continue without a file selection, the whole item is restored.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Retry', exact: true })).toHaveCount(0);
  });

  test('an empty listing reports no restorable files', async ({ page }) => {
    await page.route(contentsGlob, route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ version: 1, archive: 'plex.tar.zst', files: [] }),
    }));

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('This backup captured no restorable files for this item.')).toBeVisible();
    await expect(summary).toContainText('No restorable files');
  });
});

test.describe('Folder Restore Request review', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page, { restoreRequestFixtures: true });
  });

  test('opens a direct request link and follows query-only selection in an iframe', async ({ page }) => {
    await page.route('**/test-restore-review', route => route.fulfill({
      contentType: 'text/html',
      body: '<iframe title="Vault" src="/?embed=1#/restore?request=review-replace"></iframe>',
    }));
    await page.goto('/test-restore-review');
    const frame = page.frameLocator('iframe[title="Vault"]');
    const review = frame.getByRole('region', { name: 'Restore Request review' });
    await expect(review.getByText('/mnt/user/recovery/fixture')).toBeVisible();
    await expect(review.getByText(/Clear all existing target contents/i)).toBeVisible();
    await expect(review.getByText(/This backup has no recorded Verification/i)).toBeVisible();
    await expect(review.getByRole('button', { name: /approve|start restore/i })).toHaveCount(0);

    await frame.getByRole('link', { name: /merge fixture/i }).click();
    await expect(review.getByText(/Overwrite matching entries/i)).toBeVisible();
    await expect(review.getByText(/Backup Chain could not be confirmed/i)).toBeVisible();
    await expect(review.getByText('/mnt/user/recovery/fixture')).toHaveCount(0);
  });

  test('shows unavailable and denied review links', async ({ page }) => {
    await page.goto('/#/restore?request=missing');
    await expect(page.getByText('Restore Request not found.')).toBeVisible();
    await page.evaluate(() => { window.location.hash = '/restore?request=denied'; });
    await expect(page.getByText('Operator access required to review this Restore Request.')).toBeVisible();
  });

  test('paginates terminal request history separately from run history', async ({ page }) => {
    await page.goto('/#/history');
    const history = page.getByRole('region', { name: 'Request history' });
    await expect(history.getByText('withdrawn fixture', { exact: false })).toBeVisible();
    await history.getByRole('button', { name: 'Load more requests' }).click();
    await expect(history.getByText('expired fixture', { exact: false })).toBeVisible();
    await expect(history.getByRole('button', { name: 'Load more requests' })).toHaveCount(0);
  });
});

test('native folder approval keeps a busy request pending and follows the linked Restore Run', async ({ page }) => {
  await setupVaultMockApi(page, { restoreRequestFixtures: true });
  await page.addInitScript(() => {
    Object.assign(window, { __VAULT_RUNTIME_CONFIG__: {
      mode: 'unraid-proxy',
      csrfToken: 'native-csrf-fixture',
      restoreRequestActionPath: '/plugins/vault/include/restore-request.php',
    } });
  });
  let approvalAttempts = 0;
  let finished = false;
  await page.route('**/api/v1/restore-requests/review-replace', route => route.fulfill({ json: {
    id: 'review-replace', status: finished ? 'completed' : 'pending',
    requester_name: 'Recovery assistant', job_name: 'Folder backups',
    item: { item_name: 'archive', item_type: 'folder' },
    restore_point: { id: 11, created_at: '2026-11-10T02:00:00Z' },
    target_choice: 'alternate', configured_path: '/mnt/user/restore/review',
    resolved_path: '/mnt/cache/restore/review',
    expires_at: '2026-11-14T02:00:00Z', membership: 'confirmed', verification: { status: 'none' },
    chain: { status: 'confirmed', steps: [] }, effective_write_mode: 'replace',
    effects: ['clear_target_contents', 'write_backed_up_entries'], warnings: ['unverified_backup'],
    eligible_for_approval: true, requires_passphrase: true, review_digest: 'reviewed-fixture',
    ...(finished ? {
      run_id: 77, approval: { operator_name: 'root', approved_at: '2026-11-12T02:00:00Z' },
      run: { status: 'completed', items_done: 1, items_total: 1, items_failed: 0, application_health: 'unchecked' },
    } : {}),
  } }));
  await page.route('**/plugins/vault/include/restore-request.php', route => {
    approvalAttempts += 1;
    const req = route.request();
    expect(req.url()).not.toContain('ephemeral-fixture');
    expect(Object.fromEntries(new URLSearchParams(req.postData() || ''))).toEqual({
      action: 'approve', id: 'review-replace', review_digest: 'reviewed-fixture',
      passphrase: 'ephemeral-fixture', csrf_token: 'native-csrf-fixture',
    });
    if (approvalAttempts === 1) {
      return route.fulfill({ status: 409, json: { error: 'Runner busy. Try again when it finishes.' } });
    }
    finished = true;
    return route.fulfill({ status: 202, json: { run_id: 77, status: 'running' } });
  });

  await page.route('**/api/v1/runs/77/logs?*', route => route.fulfill({ json: {
    run_id: 77, entries: [{ id: 1, message: 'Folder Restore completed' }],
  } }));
  await page.route(url => url.pathname === '/api/v1/restore-requests' &&
    url.searchParams.get('status')?.includes('completed') === true, route => route.fulfill({ json: {
    requests: [{ id: 'review-replace', status: 'completed', run_id: 77, job_name: 'Folder backups',
      item: { item_name: 'archive', item_type: 'folder' }, approval: { operator_name: 'root' } }],
  } }));

  await page.goto('/#/restore?request=review-replace');
  const review = page.getByRole('region', { name: 'Restore Request review' });
  await expect(review.getByText('/mnt/cache/restore/review')).toBeVisible();
  for (let attempt = 0; attempt < 2; attempt += 1) {
    await review.getByRole('button', { name: 'Approve and run' }).click();
    const dialog = page.getByRole('dialog', { name: 'Approve Restore Request' });
    await dialog.getByLabel('Backup Passphrase').fill('ephemeral-fixture');
    await dialog.getByLabel(/Type RESTORE to confirm/).fill('RESTORE');
    await dialog.getByRole('button', { name: 'Approve and run' }).click();
    if (attempt === 0) {
      await expect(review.getByRole('alert')).toContainText('Runner busy');
      await expect(review.getByText('pending', { exact: true })).toBeVisible();
    }
  }
  await expect(review.getByText('Restore Run #77')).toBeVisible();
  await expect(review.getByText('unchecked', { exact: true })).toBeVisible();
  await review.getByText('Restore Run log', { exact: true }).click();
  await expect(review.getByText('Folder Restore completed', { exact: true })).toBeVisible();
  await page.goto('/#/history');
  const history = page.getByRole('region', { name: 'Request history' });
  await expect(history.getByText(/Restore Run #77/)).toBeVisible();
  await expect(history.getByText(/root/)).toBeVisible();
  await history.getByRole('link', { name: 'archive · Folder backups' }).click();
  await page.reload();
  await expect(review.getByText('Restore Run #77')).toBeVisible();
});

for (const stopOutcome of ['cancelled', 'completed'] as const) {
  test(`native exact-run stop shows the ${stopOutcome} outcome`, async ({ page }) => {
    let status = 'running';
    await setupVaultMockApi(page, {
      restoreRequestFixtures: true,
      restoreRequestRun: () => ({
        status,
        run_id: 91,
        terminal_reason: status === 'cancelled' ? 'stopped_by_operator' : undefined,
        run: {
          status, active: status === 'running', items_total: 1, items_done: status === 'completed' ? 1 : 0,
          items_failed: 0, application_health: 'unchecked',
        },
      }),
    });
    await page.addInitScript(() => {
      Object.assign(window, { __VAULT_RUNTIME_CONFIG__: {
        mode: 'unraid-proxy',
        csrfToken: 'native-csrf-fixture',
        restoreRequestActionPath: '/plugins/vault/include/restore-request.php',
      } });
    });
    let acceptStop = () => {};
    const stopReply = new Promise<void>(resolve => { acceptStop = resolve; });
    let attempts = 0;
    await page.route('**/plugins/vault/include/restore-request.php', async route => {
      attempts += 1;
      expect(Object.fromEntries(new URLSearchParams(route.request().postData() || ''))).toEqual({
        action: 'stop', id: '91', csrf_token: 'native-csrf-fixture',
      });
      await stopReply;
      if (stopOutcome === 'completed') {
        status = 'completed';
        return route.fulfill({ status: 409, json: { run_id: 91, status } });
      }
      return route.fulfill({ status: 202, json: { run_id: 91, status: 'stopping' } });
    });

    await page.goto('/#/restore?request=review-replace');
    const review = page.getByRole('region', { name: 'Restore Request review' });
    await expect(review.getByRole('heading', { name: 'Restore Run #91' })).toBeVisible();
    await review.getByRole('button', { name: 'Stop Restore Run' }).click();
    const dialog = page.getByRole('dialog', { name: 'Stop Restore Run #91' });
    await expect(dialog.getByText('Stopping can leave the restore location partly restored.')).toBeVisible();
    const stop = dialog.getByRole('button', { name: 'Stop Restore Run' });
    await stop.click();
    await expect(stop).toBeDisabled();
    await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
    acceptStop();
    if (stopOutcome === 'cancelled') {
      await expect(review.getByRole('status').filter({ hasText: 'Stopping…' })).toBeVisible();
      status = 'cancelled';
      await expect(review.getByText('The restore location may be partly restored.', { exact: false })).toBeVisible();
    }
    await expect(review.getByRole('status').filter({ hasText: new RegExp(stopOutcome, 'i') })).toBeVisible();
    await expect(review.getByRole('button', { name: 'Stop Restore Run' })).toHaveCount(0);
    expect(attempts).toBe(1);
  });
}
