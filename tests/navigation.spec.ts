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

    await expect(asideNav.getByRole('button', { name: /Dashboard/i })).toBeVisible();
    await expect(asideNav.getByRole('button', { name: /Jobs/i })).toBeVisible();
    await expect(asideNav.getByRole('button', { name: /Storage/i })).toBeVisible();
    await expect(asideNav.getByRole('button', { name: /Restore/i })).toBeVisible();
    await expect(asideNav.getByRole('button', { name: /Logs/i })).toBeVisible();
    await expect(asideNav.getByRole('button', { name: /Settings/i })).toBeVisible();
  });

  test('navigates across main views seamlessly', async ({ page }) => {
    await page.goto('/');

    const asideNav = page.locator('aside nav');
    await expect(asideNav).toBeVisible();

    // Navigate to Jobs
    await asideNav.getByRole('button', { name: /Jobs/i }).click();
    await expect(page).toHaveURL(/.*#\/jobs/);
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();

    // Navigate to Storage
    await asideNav.getByRole('button', { name: /Storage/i }).click();
    await expect(page).toHaveURL(/.*#\/storage/);
    await expect(page.getByText('Local Array Backup').first()).toBeVisible();

    // Navigate to Settings
    await asideNav.getByRole('button', { name: /Settings/i }).click();
    await expect(page).toHaveURL(/.*#\/settings/);

    // Navigate to Logs
    await asideNav.getByRole('button', { name: /Logs/i }).click();
    await expect(page).toHaveURL(/.*#\/logs/);

    // Navigate to Restore
    await asideNav.getByRole('button', { name: /Restore/i }).click();
    await expect(page).toHaveURL(/.*#\/restore/);
  });
});
