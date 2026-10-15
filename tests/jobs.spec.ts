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

  test.describe('per-backup verification cost warning (#457)', () => {
    const warningTitle = 'Verification can read a large amount of data from this destination';

    // Opens the wizard, picks a container, selects the destination and
    // advances to step 3 where the verification toggle lives.
    async function openVerificationStep(page, storageLabel: string) {
      await page.goto('/#/jobs');
      await page.getByRole('button', { name: /new job/i }).click();
      await page.getByRole('button', { name: /Containers/i }).first().click();
      await page.getByRole('button', { name: /plex/i }).first().click();
      const nextBtn = page.getByRole('button', { name: 'Next', exact: true });
      await nextBtn.click();
      await page.locator('#storage').selectOption({ label: storageLabel });
      await nextBtn.click();
      const toggle = page.getByRole('checkbox', { name: 'Verify backups after writing' });
      await expect(toggle).toBeAttached();
      return toggle;
    }

    test('warns with provider cost note for S3 when verification is on', async ({ page }) => {
      const toggle = await openVerificationStep(page, 'Offsite S3 (s3)');
      await expect(toggle).toBeChecked();

      await expect(page.getByText(warningTitle)).toBeVisible();
      await expect(page.getByText(/charge for requests, retrieval, or egress/)).toBeVisible();
      await expect(page.getByText(/including chunks reused from earlier backups/)).toBeVisible();
      await expect(toggle).toHaveAccessibleDescription(new RegExp(warningTitle));
    });

    test('hides the warning when verification is turned off', async ({ page }) => {
      const toggle = await openVerificationStep(page, 'Offsite S3 (s3)');
      await expect(page.getByText(warningTitle)).toBeVisible();

      await toggle.uncheck({ force: true });
      await expect(toggle).not.toBeChecked();
      await expect(page.getByText(warningTitle)).toHaveCount(0);
      await expect(toggle).not.toHaveAccessibleDescription(new RegExp(warningTitle));
    });

    test('warns in the express full form, where there is no toggle', async ({ page }) => {
      await page.goto('/#/jobs');
      await page.getByRole('button', { name: /new job/i }).click();
      await page.getByRole('button', { name: 'Switch to full form' }).click();

      const storage = page.locator('#ex_storage');
      await storage.selectOption({ label: 'Local Array Backup (local)' });
      await expect(page.getByText(warningTitle)).toHaveCount(0);

      await storage.selectOption({ label: 'Offsite S3 (s3)' });
      await expect(page.getByText(warningTitle)).toBeVisible();
      await expect(page.getByText(/under Backup Verification in the step-by-step form/)).toBeVisible();
      await expect(storage).toHaveAccessibleDescription(new RegExp(warningTitle));
    });

    test('repeats the warning on the review step', async ({ page }) => {
      await openVerificationStep(page, 'Offsite S3 (s3)');
      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await expect(page.getByLabel(/job name/i)).toBeVisible();
      await expect(page.getByText(warningTitle)).toBeVisible();
      await expect(page.getByText(/under Backup Verification in step 3/)).toBeVisible();
    });

    test('does not warn for a local destination', async ({ page }) => {
      const toggle = await openVerificationStep(page, 'Local Array Backup (local)');
      await expect(toggle).toBeChecked();
      await expect(page.getByText(warningTitle)).toHaveCount(0);
      await expect(toggle).toHaveAccessibleDescription(/validates SHA-256 checksums/);
      await page.getByRole('button', { name: 'Next', exact: true }).click();
      await expect(page.getByLabel(/job name/i)).toBeVisible();
      await expect(page.getByText(warningTitle)).toHaveCount(0);
    });
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
