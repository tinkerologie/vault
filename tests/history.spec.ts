import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Backup History & Run Inspector', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('renders backup runs with status badges and metrics', async ({ page }) => {
    await page.goto('/#/history');

    await expect(page.getByRole('heading', { name: /Backup & Restore History/i })).toBeVisible();

    // Verify stats cards
    await expect(page.getByText('Total Runs')).toBeVisible();
    await expect(page.getByText('Successful')).toBeVisible();
    await expect(page.getByRole('paragraph').filter({ hasText: 'Failed' })).toBeVisible();

    // Verify run cards in timeline
    await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
    await expect(page.locator('main span', { hasText: 'VM Backups' }).first()).toBeVisible();
    await expect(page.getByText('completed').first()).toBeVisible();
    await expect(page.getByText('failed').first()).toBeVisible();
  });

  test('filters history by status pills and search query', async ({ page }) => {
    await page.goto('/#/history');

    // Filter by "Failed" status
    const failedPill = page.getByRole('button', { name: 'Failed', exact: true });
    await expect(failedPill).toBeVisible();
    await failedPill.click();

    await expect(page.locator('main span', { hasText: 'VM Backups' }).first()).toBeVisible();
    await expect(page.locator('main span', { hasText: 'Docker & Appdata' })).toHaveCount(0);

    // Filter by "Completed" status
    const completedPill = page.getByRole('button', { name: 'Completed', exact: true });
    await completedPill.click();

    await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
    await expect(page.locator('main span', { hasText: 'VM Backups' })).toHaveCount(0);

    // Reset status to "All"
    const allPill = page.getByRole('button', { name: 'All', exact: true }).first();
    await allPill.click();
    await expect(page.locator('main span', { hasText: 'VM Backups' }).first()).toBeVisible();

    // Search query
    const searchInput = page.getByPlaceholder(/search runs/i);
    await searchInput.fill('Docker');
    await expect(page.locator('main span', { hasText: 'Docker & Appdata' }).first()).toBeVisible();
    await expect(page.locator('main span', { hasText: 'VM Backups' })).toHaveCount(0);
  });

  test('expands run details and inspects execution logs', async ({ page }) => {
    await page.goto('/#/history');

    // Click run card for Docker & Appdata
    const dockerRunCard = page.getByRole('button', { name: /^Docker & Appdata completed/ }).first();
    await expect(dockerRunCard).toBeVisible();
    await dockerRunCard.click();

    // Verify logs inspector renders logs
    await expect(page.getByText('Deduplication savings 53%')).toBeVisible();
  });
});

test('can acknowledge one failed run without changing its outcome', async ({ page }) => {
  await setupVaultMockApi(page);
  await page.goto('/#/history?status=failed');
  const rowAction = page.getByRole('button', { name: 'Acknowledge', exact: true });
  await rowAction.click();
  await expect(page.getByText('Acknowledged 1 run. Skipped 0.')).toBeVisible();
  await expect(page.getByText('Acknowledged', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: /^VM Backups failed/ })).toBeVisible();
  await expect(rowAction).toHaveCount(0);
});

test('replicas can review History but have no acknowledgement actions', async ({ page }) => {
  await setupVaultMockApi(page, { replica: true });
  await page.goto('/#/history?status=failed&review=outstanding');
  await expect(page.getByText('Read-only replica', { exact: false }).first()).toBeVisible();
  await expect(page.locator('main span').getByText('VM Backups', { exact: true })).toBeVisible();
  await expect(page.getByText('failed', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: /^Acknowledge/ })).toHaveCount(0);
  await expect(page.getByRole('checkbox', { name: /Select run .* for acknowledgement/ })).toHaveCount(0);
});

test('updates review filters on same-page navigation and browser Back and Forward', async ({ page }) => {
  await setupVaultMockApi(page, {
    historyRuns: [
      { id: 1, job_id: 1, status: 'failed', started_at: '2026-10-01T12:00:00Z' },
      { id: 2, job_id: 2, status: 'cancelled', started_at: '2026-10-01T12:00:00Z' },
    ],
  });
  await page.goto('/#/history?status=failed&review=outstanding');
  const failed = page.getByRole('button', { name: 'Failed', exact: true });
  const cancelled = page.getByRole('button', { name: 'Cancelled', exact: true });
  const review = page.getByRole('checkbox', { name: 'Needs review' });
  await expect(failed).toHaveAttribute('aria-pressed', 'true');
  await expect(review).toBeChecked();
  await page.getByRole('checkbox', { name: 'Select run 1 for acknowledgement' }).check();

  await page.evaluate(() => { window.location.hash = '/history?status=cancelled'; });
  await expect(cancelled).toHaveAttribute('aria-pressed', 'true');
  await expect(review).not.toBeChecked();
  await expect(page.getByRole('checkbox', { name: 'Select run 2 for acknowledgement' })).toBeVisible();

  await page.goBack();
  await expect(failed).toHaveAttribute('aria-pressed', 'true');
  await expect(review).toBeChecked();
  await expect(page.getByRole('checkbox', { name: 'Select run 1 for acknowledgement' })).not.toBeChecked();

  await page.goForward();
  await expect(cancelled).toHaveAttribute('aria-pressed', 'true');
  await expect(review).not.toBeChecked();
});
