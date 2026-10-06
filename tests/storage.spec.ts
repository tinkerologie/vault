import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Storage Management & Target Setup Wizard', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('renders storage destinations with capacity and dedup indicators', async ({ page }) => {
    await page.goto('/#/storage');

    const localCard = page.locator('div.rounded-xl', { has: page.getByRole('heading', { name: 'Local Array Backup' }) });
    await expect(localCard).toBeVisible();

    // Verify type badge and path
    await expect(localCard.getByText('local', { exact: true })).toBeVisible();
    await expect(localCard.getByText('/mnt/user/backups')).toBeVisible();

    // Verify deduplication badge
    await expect(localCard.getByText(/Dedup/i)).toBeVisible();
  });

  test('guides user through Add Storage modal with connection test for local and S3', async ({ page }) => {
    await page.goto('/#/storage');

    // Open Add Storage modal
    const addBtn = page.getByRole('button', { name: /add storage/i });
    await expect(addBtn).toBeVisible();
    await addBtn.click();

    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('heading', { name: /add storage/i })).toBeVisible();

    // 1. Fill basic target name
    const nameInput = dialog.locator('#sname');
    await nameInput.fill('Cloud S3 Primary');

    // 2. Select S3 storage type
    const s3TypeBtn = dialog.getByRole('button', { name: 'S3 / S3-Compatible' });
    await s3TypeBtn.click();
    await expect(s3TypeBtn).toHaveAttribute('aria-pressed', 'true');

    // Quick-select preset (e.g. AWS S3 or MinIO)
    const wasabiPreset = dialog.getByRole('button', { name: 'Wasabi' });
    await expect(wasabiPreset).toBeVisible();
    await wasabiPreset.click();

    // Verify preset filled region
    const regionInput = dialog.locator('#s3_region');
    await expect(regionInput).toHaveValue('us-east-1');

    // Fill bucket and credentials
    await dialog.locator('#s3_bucket').fill('my-vault-backups');
    await dialog.locator('#s3_ak').fill('AKIATEST123456');
    await dialog.locator('#s3_sk').fill('SecretKey123456');

    // 3. Test unsaved connection
    const testConnBtn = dialog.getByRole('button', { name: /test connection/i });
    await expect(testConnBtn).toBeVisible();
    await testConnBtn.click();

    // Verify Connection OK badge/text
    await expect(dialog.getByText(/connection ok/i)).toBeVisible();

    // 4. Toggle deduplication
    const dedupCheckbox = dialog.getByLabel(/enable deduplication/i);
    await expect(dedupCheckbox).toBeVisible();
    await dedupCheckbox.check();
    await expect(dedupCheckbox).toBeChecked();

    // 5. Submit storage creation
    const submitBtn = dialog.getByRole('button', { name: 'Add Storage', exact: true });
    await expect(submitBtn).toBeEnabled();
    await submitBtn.click();

    // Verify toast & updated destinations list
    await expect(page.getByText(/storage created successfully/i)).toBeVisible();
    await expect(page.locator('main').getByText('Cloud S3 Primary')).toBeVisible();
  });

  test('opens remote storage browser drawer and lists backup archives', async ({ page }) => {
    await page.goto('/#/storage');

    // Click Browse destination files button on the card
    const browseBtn = page.getByRole('button', { name: /browse destination files/i }).first();
    await expect(browseBtn).toBeVisible();
    await browseBtn.click();

    // Storage browser dialog opens
    await expect(page.getByText('Browse Local Array Backup')).toBeVisible();

    // Verify file and directory entries
    await expect(page.getByText('vault-manifest.json')).toBeVisible();
    await expect(page.getByText('backups').first()).toBeVisible();

    // Close browser drawer
    const closeBtn = page.getByRole('button', { name: /close browser/i });
    await closeBtn.click();
    await expect(page.getByText('Browse Local Array Backup')).not.toBeVisible();
  });

  test('refreshes destination capacity on demand', async ({ page }) => {
    await page.goto('/#/storage');

    const refreshCapBtn = page.getByRole('button', { name: 'Refresh' }).first();
    await expect(refreshCapBtn).toBeVisible();
    await refreshCapBtn.click();
    await expect(page.getByText(/capacity refreshed/i)).toBeVisible();
  });

  test('deletes a storage destination with confirmation dialog', async ({ page }) => {
    await page.goto('/#/storage');

    // Click delete button
    const deleteBtn = page.getByRole('button', { name: /delete storage/i }).first();
    await expect(deleteBtn).toBeVisible();
    await deleteBtn.click();

    // Delete confirmation dialog appears
    await expect(page.getByText(/Are you sure you want to delete/i)).toBeVisible();

    // Confirm deletion
    const confirmBtn = page.getByRole('button', { name: 'Delete Storage', exact: true });
    await confirmBtn.click();

    // Success toast shown
    await expect(page.getByText(/storage deleted/i)).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Local Array Backup' })).not.toBeVisible();
  });
});
