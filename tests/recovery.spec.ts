import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Cold Disaster Recovery Wizard (#/recover)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('guides administrator through 5-step disaster recovery from remote storage', async ({ page }) => {
    await page.goto('/#/recover');

    // Step 1: Connect storage
    await expect(page.getByText('Connect storage')).toBeVisible();
    await expect(page.getByRole('heading', { name: /Connect the storage that holds your backups/i })).toBeVisible();

    // Fill storage destination connection form
    const snameInput = page.locator('#sname');
    await snameInput.fill('Disaster Recovery Storage');

    const pathInput = page.getByRole('textbox', { name: 'Path' });
    await expect(pathInput).toBeVisible();
    await pathInput.fill('/mnt/user/backups');

    // Submit step 1
    const connectBtn = page.getByRole('button', { name: 'Connect', exact: true });
    await expect(connectBtn).toBeEnabled();
    await connectBtn.click();

    // Step 3: Choose which backup to restore
    await expect(page.getByRole('heading', { name: /Choose which backup to restore/i })).toBeVisible();
    await expect(page.getByText('Most recent backup', { exact: true })).toBeVisible();
    await expect(page.getByText('vault.db.20261005.gz')).toBeVisible();

    // Start restore
    const startRestoreBtn = page.getByRole('button', { name: /Restore this backup/i });
    await expect(startRestoreBtn).toBeVisible();
    await startRestoreBtn.click();

    // Confirm dialog
    const confirmBtn = page.getByRole('button', { name: /Yes, restore my settings/i });
    await expect(confirmBtn).toBeVisible();
    await confirmBtn.click();

    // Step 4: Check folder paths
    await expect(page.getByRole('heading', { name: /Check your folder paths/i })).toBeVisible();
    const continueBtn = page.getByRole('button', { name: 'Continue', exact: true });
    await expect(continueBtn).toBeVisible();
    await continueBtn.click();

    // Step 5: Vault is back
    await expect(page.getByRole('heading', { name: /Vault is back/i })).toBeVisible();
    const dashboardBtn = page.getByRole('button', { name: /Go to Dashboard/i });
    await expect(dashboardBtn).toBeVisible();
    await dashboardBtn.click();

    // Navigates back to dashboard
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
  });
});
