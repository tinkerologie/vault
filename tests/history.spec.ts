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
    const dockerRunCard = page.locator('div[role="button"]', { hasText: 'Docker & Appdata' }).first();
    await expect(dockerRunCard).toBeVisible();
    await dockerRunCard.click();

    // Verify logs inspector renders logs
    await expect(page.getByText('Deduplication savings 53%')).toBeVisible();
  });
});
