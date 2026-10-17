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

test.describe('Logs render window (#507)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('renders only the newest rows and grows the window on scroll', async ({ page }) => {
    // 1,500 distinct lines, so nothing collapses into one row.
    const all = Array.from({ length: 1500 }, (_, i) => {
      const id = 1500 - i;
      return {
        id,
        level: 'info',
        category: 'backup',
        message: `Window entry ${id}`,
        details: null,
        created_at: new Date(Date.UTC(2026, 7, 21, 15, 0, 0) + id * 1000).toISOString(),
      };
    });
    await page.route('**/api/v1/activity*', async (route) => {
      if (route.request().method() !== 'GET') return route.fallback();
      const url = new URL(route.request().url());
      const beforeId = parseInt(url.searchParams.get('before_id') || '0', 10);
      const limit = parseInt(url.searchParams.get('limit') || '30', 10);
      const rows = all.filter((r) => !beforeId || r.id < beforeId).slice(0, limit);
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(rows) });
    });

    await page.goto('/#/logs');
    const consoleBox = page.getByRole('log');
    await expect(consoleBox.locator('[data-entry-id="1500"]')).toBeVisible();
    const rendered = () => consoleBox.locator('[data-entry-id]').count();
    // The background full-history load fills the store; the window stays at
    // the newest 200 rows however much is loaded. All lines fall on one day,
    // so that is one date separator plus 199 log lines.
    await expect.poll(rendered, { timeout: 15000 }).toBe(199);
    await expect(page.getByText('— End of logs —')).toHaveCount(0);

    // Scrolling to the bottom of the rendered rows renders the next slice.
    await consoleBox.evaluate((el) => { el.scrollTop = el.scrollHeight; el.dispatchEvent(new Event('scroll')); });
    await expect.poll(rendered).toBe(399);

    // Further down, the window slides instead of growing: it stays capped at
    // 600 rows, drops the newest rows above the viewport, and keeps the line
    // being read in place while it does.
    for (let i = 0; i < 3; i++) {
      const before = await consoleBox.evaluate((el) => {
        el.scrollTop = el.scrollHeight;
        const box = el.getBoundingClientRect();
        const row = [...el.querySelectorAll('[data-entry-id]')].find((r) => r.getBoundingClientRect().top >= box.top) as HTMLElement;
        const top = row.getBoundingClientRect().top - box.top;
        el.dispatchEvent(new Event('scroll'));
        return { id: row.dataset.entryId, top };
      });
      await page.waitForTimeout(150);
      const after = await consoleBox.evaluate((el, id) => {
        const row = el.querySelector(`[data-entry-id="${id}"]`) as HTMLElement;
        return row ? row.getBoundingClientRect().top - el.getBoundingClientRect().top : null;
      }, before.id);
      expect(after).not.toBeNull();
      expect(Math.abs((after as number) - before.top)).toBeLessThan(2);
      expect(await rendered()).toBeLessThanOrEqual(600);
    }
    await expect(consoleBox.locator('[data-entry-id="1500"]')).toHaveCount(0);

    // "Jump to oldest" renders only the oldest rows, ending at the marker.
    await page.getByRole('button', { name: 'Jump to oldest log' }).click();
    await expect(consoleBox.locator('[data-entry-id="1"]')).toBeAttached();
    await expect(page.getByText('— End of logs —')).toBeVisible();
    expect(await rendered()).toBe(200);
    await expect(consoleBox.locator('[data-entry-id="1500"]')).toHaveCount(0);

    // Scrolling up from there renders newer rows above, keeping the reader's
    // line in place, and does not count as reaching the live edge.
    // Note the topmost rendered row and its position at the top of the
    // console, then trigger the upward growth.
    const before = await consoleBox.evaluate((el) => {
      el.scrollTop = 0;
      const row = el.querySelector('[data-entry-id]') as HTMLElement;
      const top = row.getBoundingClientRect().top - el.getBoundingClientRect().top;
      el.dispatchEvent(new Event('scroll'));
      return { id: row.dataset.entryId, top };
    });
    await expect.poll(rendered).toBe(400);
    const after = await consoleBox.evaluate((el, id) => {
      const row = el.querySelector(`[data-entry-id="${id}"]`) as HTMLElement;
      return row.getBoundingClientRect().top - el.getBoundingClientRect().top;
    }, before.id);
    expect(Math.abs(after - before.top)).toBeLessThan(2);
    await expect(page.getByRole('button', { name: 'Jump to present' })).toBeVisible();

    // "Jump to present" returns to the newest 200 rows.
    await page.getByRole('button', { name: 'Jump to present' }).click();
    await expect(consoleBox.locator('[data-entry-id="1500"]')).toBeVisible();
    await expect.poll(rendered).toBe(199);
  });
});
