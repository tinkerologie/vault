import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Activity Logs & Run Log Viewer', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('renders log entries and applies level and search filters', async ({ page }) => {
    await page.goto('/#/logs');

    await expect(page.getByRole('heading', { name: 'Logs' })).toBeVisible();

    // Verify initial log messages from mock are rendered
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('Storage capacity above 75%').first()).toBeVisible();

    // Filter by Level: Error
    const levelSelect = page.locator('label', { hasText: 'Level' }).locator('select');
    await expect(levelSelect).toBeVisible();
    await levelSelect.selectOption({ label: 'Error' });

    // Error entry visible, info/warn hidden
    await expect(page.getByText('Failed to reach remote endpoint')).toBeVisible();
    await expect(page.getByText('Storage capacity above 75%')).toHaveCount(0);

    // Reset Level to All
    await levelSelect.selectOption({ label: 'All' });
    await expect(page.getByText('Storage capacity above 75%')).toBeVisible();

    // Search filter
    const searchInput = page.getByPlaceholder(/filter logs/i);
    await searchInput.fill('remote endpoint');
    await expect(page.getByText('Failed to reach remote endpoint')).toBeVisible();
    await expect(page.getByText('Docker & Appdata')).toHaveCount(0);

    await searchInput.fill('');
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
  });

  test('toggles line wrapping and metadata details', async ({ page }) => {
    await page.goto('/#/logs');

    // Toggle Wrap
    const wrapBtn = page.getByRole('button', { name: /wrap:/i });
    await expect(wrapBtn).toBeVisible();
    await wrapBtn.click();
    await expect(page.getByRole('button', { name: 'Wrap: on' })).toBeVisible();

    // Toggle Details
    const detailsBtn = page.getByRole('button', { name: /details:/i });
    await expect(detailsBtn).toBeVisible();
    await detailsBtn.click();
    await expect(page.getByRole('button', { name: 'Details: on' })).toBeVisible();
  });

  test('supports purging logs with confirmation dialog', async ({ page }) => {
    await page.goto('/#/logs');

    const purgeBtn = page.getByRole('button', { name: /purge/i });
    await expect(purgeBtn).toBeVisible();
    await purgeBtn.click();

    // Confirm dialog appears
    const confirmModal = page.getByRole('dialog');
    await expect(confirmModal).toBeVisible();

    // Confirm purge
    const confirmBtn = confirmModal.getByRole('button', { name: /purge all/i });
    await confirmBtn.click();

    // Logs are cleared
    await expect(page.getByText('No logs', { exact: true })).toBeVisible();
  });
});
