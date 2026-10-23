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

  test('shows the per-stream S3 upload buffer for the part size (#500)', async ({ page }) => {
    await page.goto('/#/storage');
    await page.getByRole('button', { name: /add storage/i }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'S3 / S3-Compatible' }).click();
    await dialog.locator('summary', { hasText: 'Advanced' }).click();

    const hint = dialog.getByTestId('s3-part-memory-hint');
    await expect(hint).toContainText('320 MiB'); // default 64 MiB × 5
    await dialog.locator('#s3_part_size').fill('128');
    await expect(hint).toContainText('640 MiB');
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

  test.describe('unsafe local roots (#514)', () => {
    // A small directory tree: the root listing holds shortcuts, then
    // /mnt -> /mnt/user -> /mnt/user/backups.
    const tree: Record<string, { path: string; entries: { name: string; path: string; is_dir: boolean }[] }> = {
      '': { path: '', entries: [{ name: 'mnt', path: '/mnt', is_dir: true }, { name: 'boot', path: '/boot', is_dir: true }] },
      '/mnt': { path: '/mnt', entries: [{ name: 'user', path: '/mnt/user', is_dir: true }] },
      '/mnt/user': { path: '/mnt/user', entries: [{ name: 'backups', path: '/mnt/user/backups', is_dir: true }] },
      '/mnt/user/backups': { path: '/mnt/user/backups', entries: [] },
      '/boot': { path: '/boot', entries: [] },
    };

    async function openLocalForm(page) {
      await page.route(/\/api\/v1\/browse(\?|$)/, (route) => {
        const p = new URL(route.request().url()).searchParams.get('path') || '';
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(tree[p] ?? tree['']) });
      });
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /add storage/i }).click();
      const dialog = page.getByRole('dialog');
      await dialog.locator('#sname').fill('Picker Safety');
      await dialog.getByRole('button', { name: /^Local/ }).click();
      return dialog;
    }

    test('the directory picker cannot select the root listing or shared parents', async ({ page }) => {
      const deletes: string[] = [];
      page.on('request', (req) => { if (req.method() === 'DELETE') deletes.push(req.url()); });
      await openLocalForm(page);

      await page.getByRole('button', { name: 'Browse directories' }).click();
      const select = page.getByRole('button', { name: 'Select This Path' });

      // Root listing: not selectable at all.
      await expect(select).toBeDisabled();
      await expect(page.getByTestId('path-select-blocked')).toContainText('Open a directory');

      // Shared parents and protected trees are blocked with a reason.
      await page.getByRole('button', { name: /^mnt/ }).click();
      await expect(page.getByTestId('path-select-blocked')).toContainText('shared parent');
      await expect(select).toBeDisabled();
      await page.getByRole('button', { name: /^user/ }).click();
      await expect(page.getByTestId('path-select-blocked')).toContainText('shared parent');
      await expect(select).toBeDisabled();

      // A dedicated folder is selectable.
      await page.getByRole('button', { name: /^backups/ }).click();
      await expect(select).toBeEnabled();
      await select.click();
      await expect(page.getByRole('textbox', { name: 'Path' })).toHaveValue('/mnt/user/backups');

      // Picking a directory never triggers a destructive request.
      expect(deletes).toEqual([]);
    });

    test('the picker cannot select while a directory listing is loading', async ({ page }) => {
      await openLocalForm(page);
      // Hold the next listing so the picker stays in its loading state.
      let release: () => void = () => {};
      const held = new Promise<void>((resolve) => { release = resolve; });
      await page.route(/\/api\/v1\/browse\?path=%2Fmnt$/, async (route) => {
        await held;
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(tree['/mnt']) });
      });

      await page.getByRole('button', { name: 'Browse directories' }).click();
      await page.getByRole('button', { name: /^mnt/ }).click();
      const select = page.getByRole('button', { name: 'Select This Path' });
      await expect(page.getByText('Loading...')).toBeVisible();
      await expect(select).toBeDisabled();
      await expect(page.getByTestId('path-select-blocked')).toHaveCount(0);

      release();
      await expect(page.getByTestId('path-select-blocked')).toContainText('shared parent');
      await expect(select).toBeDisabled();
    });

    test('an unsafe typed path shows a hint and is not submitted', async ({ page }) => {
      const posts: string[] = [];
      page.on('request', (req) => {
        if (req.method() === 'POST' && /\/api\/v1\/storage$/.test(new URL(req.url()).pathname)) posts.push(req.url());
      });
      const dialog = await openLocalForm(page);
      const pathInput = dialog.getByRole('textbox', { name: 'Path' });

      await pathInput.fill('/');
      await expect(dialog.getByTestId('local-path-hint')).toContainText('protected system location');
      await pathInput.fill('relative/dir');
      await expect(dialog.getByTestId('local-path-hint')).toContainText('absolute');

      await dialog.getByRole('button', { name: 'Add Storage', exact: true }).click();
      await expect(page.getByText(/must be absolute/i).last()).toBeVisible();
      expect(posts).toEqual([]);
    });

    test('a server rejection of the root is shown to the user', async ({ page }) => {
      const dialog = await openLocalForm(page);
      await page.route(/\/api\/v1\/storage$/, (route) => {
        if (route.request().method() !== 'POST') return route.fallback();
        return route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'storage path "/mnt/user/link" is a protected system location; choose a dedicated backup directory such as /mnt/user/backups/vault' }),
        });
      });
      await dialog.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/link');
      await dialog.getByRole('button', { name: 'Add Storage', exact: true }).click();
      await expect(page.getByText(/is a protected system location/).first()).toBeVisible();
    });

    test('a partial file cleanup is reported as a warning, not success', async ({ page }) => {
      await page.route(/\/api\/v1\/storage\/\d+(\?.*)?$/, (route) => {
        if (route.request().method() !== 'DELETE') return route.fallback();
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ deleted: true, cleanup_error: 'refusing to delete a path outside Vault\'s managed backup layout: "plugins"' }),
        });
      });
      await page.goto('/#/storage');
      await page.getByRole('button', { name: /delete storage/i }).first().click();
      const dialog = page.getByRole('dialog');
      await expect(dialog.getByText(/Other files and folders in the destination are left untouched/)).toBeVisible();
      await dialog.getByText('Delete Vault backup files').click();
      await dialog.getByRole('button', { name: 'Delete Storage & Files' }).click();
      await expect(page.getByText(/some backup files were not removed/i)).toBeVisible();
      await expect(page.getByText('Storage and Vault backup files deleted')).toHaveCount(0);
    });
  });
});
