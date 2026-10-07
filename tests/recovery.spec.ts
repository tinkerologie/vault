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

  test('unlocks deduplicated backups with the backup password after the restore (#451)', async ({ page }) => {
    const passphrases: string[] = [];
    await page.route('**/storage/dedup-keys/rewrap', async (route) => {
      const pass = route.request().postDataJSON()?.passphrase ?? '';
      passphrases.push(pass);
      const status = pass === 'correct horse' ? 'rewrapped' : pass ? 'locked_wrong_passphrase' : 'locked_needs_passphrase';
      const error = status === 'rewrapped' ? '' : status === 'locked_wrong_passphrase'
        ? 'the backup passphrase does not open this destination\'s escrow'
        : 'enter the backup passphrase to unlock this destination';
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ destinations: [{ storage_id: 2, name: 'Dedup Backups', status, error }] }),
      });
    });

    await page.goto('/#/recover');
    await page.locator('#sname').fill('Disaster Recovery Storage');
    await page.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups');
    await page.getByRole('button', { name: 'Connect', exact: true }).click();
    await page.getByRole('button', { name: /Restore this backup/i }).click();
    await page.getByRole('button', { name: /Yes, restore my settings/i }).click();

    // The restore asks which dedup destinations are still locked.
    await expect(page.getByRole('heading', { name: 'Deduplicated backups' })).toBeVisible();
    await expect(page.getByText('enter the backup passphrase to unlock this destination')).toBeVisible();
    expect(passphrases).toEqual(['']);

    const unlock = page.getByRole('button', { name: 'Unlock' });
    await expect(unlock).toBeDisabled();
    await page.getByLabel('Backup password').fill('wrong');
    await unlock.click();
    await expect(page.getByText(/does not open this destination/)).toBeVisible();

    await page.getByLabel('Backup password').fill('correct horse');
    await unlock.click();
    await expect(page.getByText('✓ Dedup Backups: unlocked with your backup password.')).toBeVisible();
    await expect(page.getByLabel('Backup password')).toHaveCount(0);
    expect(passphrases).toEqual(['', 'wrong', 'correct horse']);

    // The rest of the wizard still works.
    await page.getByRole('button', { name: 'Continue', exact: true }).click();
    await expect(page.getByRole('heading', { name: /Vault is back/i })).toBeVisible();
  });
});
