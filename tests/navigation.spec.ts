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

    await expect(asideNav.getByRole('link', { name: /Dashboard/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Jobs/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Storage/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Restore/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Logs/i })).toBeVisible();
    await expect(asideNav.getByRole('link', { name: /Settings/i })).toBeVisible();
  });

  test('navigates across main views seamlessly', async ({ page }) => {
    await page.goto('/');

    const asideNav = page.locator('aside nav');
    await expect(asideNav).toBeVisible();

    // Navigate to Jobs
    await asideNav.getByRole('link', { name: /Jobs/i }).click();
    await expect(page).toHaveURL(/.*#\/jobs/);
    await expect(page.getByText('Docker & Appdata').first()).toBeVisible();

    // Navigate to Storage
    await asideNav.getByRole('link', { name: /Storage/i }).click();
    await expect(page).toHaveURL(/.*#\/storage/);
    await expect(page.getByText('Local Array Backup').first()).toBeVisible();

    // Navigate to Settings
    await asideNav.getByRole('link', { name: /Settings/i }).click();
    await expect(page).toHaveURL(/.*#\/settings/);

    // Navigate to Logs
    await asideNav.getByRole('link', { name: /Logs/i }).click();
    await expect(page).toHaveURL(/.*#\/logs/);

    // Navigate to Restore
    await asideNav.getByRole('link', { name: /Restore/i }).click();
    await expect(page).toHaveURL(/.*#\/restore/);
  });
});

// Navigation names and groups (#531): Replication sits under Protect with its
// own icon, and the planning page reads "Disaster Recovery".
test.describe('navigation groups and names (#531)', () => {
  const sidebar = (page: import('@playwright/test').Page) => page.getByRole('navigation', { name: 'Main navigation' });

  /** Section headers and link labels in sidebar order. */
  const sidebarOutline = (page: import('@playwright/test').Page) =>
    sidebar(page).evaluate((nav) => Array.from(nav.children, (el) => (el.tagName === 'A' ? '' : '# ') + (el.textContent ?? '').trim()));

  test('daemon sidebar groups Replication under Protect and Disaster Recovery under Recover', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/');
    await expect(sidebar(page).getByRole('link', { name: 'Disaster Recovery' })).toBeVisible();
    expect(await sidebarOutline(page)).toEqual([
      'Dashboard',
      '# Protect', 'Jobs', 'Storage', 'Replication',
      '# Recover', 'Restore', 'History', 'Disaster Recovery',
      '# System', 'Logs', 'Anomalies', 'Settings', 'Documentation',
    ]);
  });

  test('Replication has its own icon', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/');
    const iconOf = (name: string) => sidebar(page).getByRole('link', { name, exact: true }).locator('svg path').getAttribute('d');
    const replication = await iconOf('Replication');
    expect(replication).toBeTruthy();
    for (const other of ['Dashboard', 'Jobs', 'Storage', 'Restore', 'History', 'Disaster Recovery', 'Logs', 'Anomalies', 'Settings']) {
      expect(await iconOf(other), `${other} icon`).not.toBe(replication);
    }
  });

  test('Disaster Recovery opens the page with the matching heading and title', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/');
    await sidebar(page).getByRole('link', { name: 'Disaster Recovery' }).click();
    await expect(page).toHaveURL(/#\/recovery$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Disaster Recovery', exact: true })).toBeVisible();
    await expect(page).toHaveTitle('Disaster Recovery – Vault');
  });

  test('replica sidebar keeps Replication under Protect without empty groups', async ({ page }) => {
    await setupVaultMockApi(page, { replica: true });
    await page.route('**/api/v1/settings', (route) => (route.request().method() === 'GET'
      ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ replication_enabled: 'false' }) })
      : route.fallback()));
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/');
    await expect(sidebar(page).getByRole('link', { name: 'Replication' })).toBeVisible();
    expect(await sidebarOutline(page)).toEqual([
      'Dashboard',
      '# Protect', 'Storage', 'Replication',
      '# Recover', 'History',
      '# System', 'Logs', 'Anomalies', 'Settings', 'Documentation',
    ]);
    await sidebar(page).getByRole('link', { name: 'Replication' }).click();
    await expect(page).toHaveURL(/#\/replication$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Replication', exact: true })).toBeVisible();
  });

  test('replica redirects a deep link to Disaster Recovery to the Dashboard', async ({ page }) => {
    await setupVaultMockApi(page, { replica: true });
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/#/recovery');
    await expect(page).toHaveURL(/#\/$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Dashboard', exact: true })).toBeVisible();
  });

  test('replica bottom bar shows Replication with its own icon', async ({ page }) => {
    await setupVaultMockApi(page, { replica: true });
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/#/');
    const bar = page.getByRole('navigation', { name: 'Mobile navigation' });
    const replication = bar.getByRole('link', { name: 'Replication' });
    await expect(replication).toBeVisible();
    const replicationIcon = await replication.locator('svg path').getAttribute('d');
    expect(replicationIcon).not.toBe(await bar.getByRole('link', { name: 'History' }).locator('svg path').getAttribute('d'));
    await replication.click();
    await expect(page).toHaveURL(/#\/replication$/);
  });
});
