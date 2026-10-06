import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test.describe('Jobs Management & Creation Wizard', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('renders jobs list with cards, schedules, and storage badges', async ({ page }) => {
    await page.goto('/#/jobs');

    await expect(page.getByRole('heading', { name: 'Backup Jobs' })).toBeVisible();
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('VM Backups').first()).toBeVisible();

    // Verify storage destination badge and schedule description
    await expect(page.locator('main span', { hasText: 'Local Array Backup' }).first()).toBeVisible();
  });

  test('filters jobs by search input and status', async ({ page }) => {
    await page.goto('/#/jobs');

    const searchInput = page.getByPlaceholder(/search jobs/i);
    await expect(searchInput).toBeVisible();

    // Search for "Docker"
    await searchInput.fill('Docker');
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('VM Backups')).not.toBeVisible();

    // Clear search
    await searchInput.fill('');
    await expect(page.getByText('VM Backups').first()).toBeVisible();

    // Filter by "Disabled" status
    await page.getByRole('button', { name: 'Disabled', exact: true }).click();
    await expect(page.getByText('No jobs match these filters.')).toBeVisible();
    await expect(page.getByText('Docker & Appdata')).not.toBeVisible();

    // Filter by "Enabled" status
    await page.getByRole('button', { name: 'Enabled', exact: true }).click();
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();
    await expect(page.getByText('VM Backups').first()).toBeVisible();
  });

  test('triggers Run Now and verifies queued execution state', async ({ page }) => {
    await page.goto('/#/jobs');

    // Find the Run Now button on the first job row
    const runBtn = page.getByRole('button', { name: /run backup now/i }).first();
    await expect(runBtn).toBeVisible();
    await runBtn.click();

    // Toast should report queued execution
    await expect(page.getByText(/queued for execution/i).first()).toBeVisible();
  });

  test('guides user through multi-step job creation modal with container picker', async ({ page }) => {
    await page.goto('/#/jobs');

    // Click "New Job" button
    const newJobBtn = page.getByRole('button', { name: /new job/i });
    await expect(newJobBtn).toBeVisible();
    await newJobBtn.click();

    // Modal opens: Step 1 What (TypePicker + ItemPicker)
    await expect(page.getByText('Select one or more backup types.')).toBeVisible();

    // Select "Containers" backup type card
    const containersCard = page.getByRole('button', { name: /Containers/i }).first();
    await expect(containersCard).toBeVisible();
    await containersCard.click();

    // Select container item 'plex'
    const plexItem = page.getByRole('button', { name: /plex/i }).first();
    await expect(plexItem).toBeVisible();
    await plexItem.click();

    // Advance to Step 2
    const nextBtn = page.getByRole('button', { name: 'Next', exact: true });
    await expect(nextBtn).toBeEnabled();
    await nextBtn.click();

    // Step 2: Where & when (Storage + Schedule)
    await expect(page.getByText('Storage Destination')).toBeVisible();
    const storageSelect = page.locator('#storage');
    await expect(storageSelect).toBeVisible();
    // Default or select Local Array Backup
    await storageSelect.selectOption({ label: 'Local Array Backup (local)' });

    await nextBtn.click();

    // Step 3: How (Backup Mode, Compression, Retention)
    await expect(page.getByText('Backup Mode', { exact: true })).toBeVisible();
    await expect(page.getByLabel(/backup type/i)).toBeVisible();

    // Adjust retention count
    const retentionCountInput = page.locator('#retention_count');
    await expect(retentionCountInput).toBeVisible();
    await retentionCountInput.fill('7');

    await nextBtn.click();

    // Step 4: Name & review
    await expect(page.getByLabel(/job name/i)).toBeVisible();
    const nameInput = page.locator('#name');
    await nameInput.fill('Daily Media Containers');

    const descInput = page.locator('#desc');
    await descInput.fill('Nightly backup of media services');

    // Verify summary card contains selected details
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Daily Media Containers')).toBeVisible();
    await expect(dialog.getByText('Local Array Backup')).toBeVisible();

    // Submit Job Creation
    const createBtn = page.getByRole('button', { name: 'Create Job', exact: true });
    await expect(createBtn).toBeEnabled();
    await createBtn.click();

    // Modal closes and new job appears in the list
    await expect(page.getByText('Job created successfully')).toBeVisible();
    await expect(page.locator('main').getByText('Daily Media Containers')).toBeVisible();
  });

  test('supports deleting a job with confirmation modal', async ({ page }) => {
    await page.goto('/#/jobs');

    // Click delete on the second job (VM Backups)
    const deleteBtn = page.getByRole('button', { name: /delete job/i }).nth(1);
    await expect(deleteBtn).toBeVisible();
    await deleteBtn.click();

    // Delete confirmation dialog appears
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(/Are you sure you want to delete/i)).toBeVisible();

    // Confirm deletion inside dialog
    const confirmBtn = dialog.getByRole('button', { name: /delete job/i });
    await confirmBtn.click();

    // Job deleted toast and removed from list
    await expect(page.getByText(/Job deleted/i)).toBeVisible();
    await expect(page.getByText('VM Backups')).toHaveCount(0);
  });
});
