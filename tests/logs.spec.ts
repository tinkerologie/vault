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

  test('renders initial rows and remains responsive while older page is pending (#454)', async ({ page }) => {
    // Generate 60 activity rows to exercise pagination (default batch is 30)
    const rows = Array.from({ length: 60 }, (_, i) => {
      const id = 60 - i;
      return {
        id,
        level: 'info',
        category: 'backup',
        message: `Activity entry #${id}`,
        details: null,
        created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + id * 1000).toISOString(),
      };
    });

    let releaseGate: () => void = () => {};
    const gateHeld = new Promise<void>(resolve => { releaseGate = resolve; });
    let olderCalls = 0;
    let gateReleased = false;

    // Route-specific override registered after shared mock
    await page.route('**/api/v1/activity*', async route => {
      if (route.request().method() !== 'GET') {
        return route.fallback();
      }
      const url = new URL(route.request().url());
      const beforeIdStr = url.searchParams.get('before_id');
      const beforeId = beforeIdStr ? parseInt(beforeIdStr, 10) : 0;
      const limit = parseInt(url.searchParams.get('limit') || '30', 10);
      const category = url.searchParams.get('category') || '';

      if (beforeId > 0) {
        olderCalls++;
        if (!gateReleased) {
          await gateHeld;
        }
      }

      let filtered = rows;
      if (category) filtered = filtered.filter(r => r.category === category);
      if (beforeId > 0) filtered = filtered.filter(r => r.id < beforeId);
      filtered.sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime() || b.id - a.id);
      const pageRows = filtered.slice(0, limit);

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(pageRows),
      });
    });

    await page.goto('/#/logs');

    // Initial rows render in the role="log" console
    const logConsole = page.getByRole('log');
    await expect(logConsole.locator('[data-entry-id="60"]')).toBeVisible();

    // "Loading logs..." spinner disappears
    await expect(page.getByText('Loading logs...')).toHaveCount(0);

    // Search input remains interactive while older-page request is held
    const searchInput = page.getByPlaceholder(/filter logs/i);
    await expect(searchInput).toBeVisible();
    await searchInput.fill('Entry #60');
    await expect(searchInput).toHaveValue('Entry #60');
    await searchInput.fill('');

    // Release the gate
    gateReleased = true;
    releaseGate();

    // Older rows load and older entries become visible
    await expect(logConsole.locator('[data-entry-id="1"]')).toBeVisible();

    // Request count remains bounded
    expect(olderCalls).toBeGreaterThanOrEqual(1);
    expect(olderCalls).toBeLessThanOrEqual(5);
  });
});
