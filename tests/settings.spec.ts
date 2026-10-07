import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Settings Configuration & Management', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('switches across tabs and navigates settings sections', async ({ page }) => {
    await page.goto('/#/settings');

    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();

    // Verify main tabs are present
    const generalTab = page.getByRole('button', { name: 'General', exact: true });
    const securityTab = page.getByRole('button', { name: 'Security', exact: true });
    const notificationsTab = page.getByRole('button', { name: 'Notifications', exact: true });
    const referenceTab = page.getByRole('button', { name: 'Reference', exact: true });

    await expect(generalTab).toBeVisible();
    await expect(securityTab).toBeVisible();
    await expect(notificationsTab).toBeVisible();
    await expect(referenceTab).toBeVisible();

    // Switch to Security tab
    await securityTab.click();
    await expect(page.getByText('Encryption').first()).toBeVisible();
    await expect(page.getByText('API Key').first()).toBeVisible();

    // Switch to Reference tab
    await referenceTab.click();
    await expect(page.getByText('Documentation').first()).toBeVisible();

    // Switch back to General tab
    await generalTab.click();
    await expect(page.getByText('Appearance').first()).toBeVisible();
  });

  test('toggles dark and light appearance modes', async ({ page }) => {
    await page.goto('/#/settings');

    // Appearance mode options
    const lightBtn = page.getByRole('button', { name: 'Light', exact: true });
    const darkBtn = page.getByRole('button', { name: 'Dark', exact: true });

    await expect(lightBtn).toBeVisible();
    await expect(darkBtn).toBeVisible();

    await lightBtn.click();
    await expect(page.locator('html')).not.toHaveClass(/dark/);

    await darkBtn.click();
    await expect(page.locator('html')).toHaveClass(/dark/);
  });

  test('configures and tests Discord notification webhook', async ({ page }) => {
    await page.goto('/#/settings');

    // Go to Notifications tab
    await page.getByRole('button', { name: 'Notifications', exact: true }).click();
    await expect(page.getByText('Discord Notifications')).toBeVisible();

    // Enter Webhook URL
    const discordInput = page.locator('#discord-url');
    await expect(discordInput).toBeVisible();
    await discordInput.fill('https://discord.com/api/webhooks/123456/abcdef');

    // Click test button next to webhook input
    const testBtn = discordInput.locator('..').getByRole('button', { name: 'Test' });
    await expect(testBtn).toBeEnabled();
    await testBtn.click();

    // Verify toast confirms test notification dispatched
    await expect(page.getByText(/Test notification sent to Discord!/i)).toBeVisible();
  });

  test('triggers diagnostics bundle export download', async ({ page }) => {
    await page.goto('/#/settings');

    const diagDownloadBtn = page.getByRole('button', { name: /Download Diagnostics/i });
    await expect(diagDownloadBtn).toBeVisible();
    const downloadPromise = page.waitForEvent('download');
    await diagDownloadBtn.click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain('diagnostics');
  });

  test('downloads the server key for an off-server copy (#451)', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByRole('button', { name: 'Security', exact: true }).click();

    await expect(page.getByRole('heading', { name: /Server key/ })).toBeVisible();
    await expect(page.getByText(/Anyone with this file and access to your backup storage/)).toBeVisible();
    // The mock has no backup password, so the key is the only recovery path.
    await expect(page.getByText(/No backup password is set, so this file is the only way/)).toBeVisible();

    const downloadPromise = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Download vault.key' }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe('vault.key');
    const stream = await download.createReadStream();
    const chunks: Buffer[] = [];
    for await (const c of stream) chunks.push(c as Buffer);
    expect(Buffer.concat(chunks).length).toBe(32);
    await expect(page.getByText(/vault.key downloaded/)).toBeVisible();
  });
});
