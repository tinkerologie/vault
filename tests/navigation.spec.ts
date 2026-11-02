import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Vault App Navigation & Shell', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('loads dashboard and renders navigation links', async ({ page }) => {
    await page.goto('/');

    // Expect brand/title in navigation or header
    await expect(page).toHaveTitle(/Vault/i);

    // Verify desktop sidebar nav exists
    const asideNav = page.locator('aside nav');
    await expect(asideNav).toBeVisible();

    await expect(asideNav.getByRole('link', { name: /Dashboard/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Jobs/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Storage/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Restore/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Logs/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Settings/i })).toBeVisible();
  });

  test('navigates across main views seamlessly', async ({ page }) => {
    await page.goto('/');

    const asideNav = page.locator('aside nav');
    await expect(asideNav).toBeVisible();

    // Navigate to Jobs
    await asideNav.getByRole('link', { name: /Jobs/i }).click();
    await expect(page).toHaveURL(/.*#\/jobs/);
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();

    // Navigate to Storage
    await asideNav.getByRole('link', { name: /Storage/i }).click();
    await expect(page).toHaveURL(/.*#\/storage/);
    await expect(page.getByText('Local Array Backup').first()).toBeVisible();

    // Navigate to Settings
    await asideNav.getByRole('link', { name: /Settings/i }).click();
    await expect(page).toHaveURL(/.*#\/settings/);

    // Navigate to Logs
    await asideNav.getByRole('link', { name: /Logs/i }).click();
    await expect(page).toHaveURL(/.*#\/logs/);

    // Navigate to Restore
    await asideNav.getByRole('link', { name: /Restore/i }).click();
    await expect(page).toHaveURL(/.*#\/restore/);
  });
});
