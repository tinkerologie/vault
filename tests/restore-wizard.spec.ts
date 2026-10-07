import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Restore Wizard Multi-Job Flow (#438)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('aggregates items across multiple jobs and guides user through restore plan', async ({ page }) => {
    await page.goto('/#/restore');

    // Step 1: Select items across jobs
    await expect(page.getByText('Select Items').first()).toBeVisible();
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

    // Run pre-flight checks to enable restore
    await page.getByRole('button', { name: /Run all pre-flight checks/i }).click();

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

    // Verify Job 1 was submitted first while Job 2 has not started yet
    await expect.poll(() => restoreRequests.length).toBe(1);
    expect(restoreRequests[0].url).toContain('/jobs/1/restore');
    expect(restoreRequests.some(r => r.url.endsWith('/jobs/2/restore'))).toBe(false);

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
    await expect(page.getByText('Select Items').first()).toBeVisible();
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

    const summary = await openPlexPlan(page);
    await summary.click();
    await expect(page.getByText('Loading file list…')).toBeVisible();

    // Go back and re-pick the point while the first listing is in flight;
    // this resets the picker, so the late response must not repopulate it.
    await page.getByRole('button', { name: /Choose Version/ }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
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
