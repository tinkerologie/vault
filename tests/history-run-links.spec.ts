// History run links (#720): #/history?run=<id> shows the run wherever it is.
import { test, expect } from '@playwright/test';
import { createMockHistoryRuns, setupVaultMockApi } from './mocks/vault-api';

// 35 runs, newest first: the History page renders 20 at a time, so the 25th
// newest (run 11) sits on the second page.
const runs = createMockHistoryRuns(35);
const runRow = (id: number) => `#run-${id}`;

test('a link to the 25th run shows it expanded and in view', async ({ page }) => {
  await setupVaultMockApi(page, { historyRuns: runs });
  await page.goto('/#/history?run=11');
  const row = page.locator(runRow(11));
  await expect(row.locator('button[aria-expanded="true"]')).toBeVisible();
  await expect(page.locator('#run-details-11')).toBeVisible();
  await expect(row).toBeInViewport();
  // Handled, so the link leaves the URL; filters stay at their defaults.
  await expect(page).toHaveURL(/#\/history$/);
});

test('a link to a run older than the loaded history fetches it on its own', async ({ page }) => {
  const old = { ...createMockHistoryRuns(1, Date.UTC(2025, 0, 1, 12))[0], id: 900 };
  await setupVaultMockApi(page, { historyRuns: runs, runResult: (id) => (id === 900 ? old : 404) });
  await page.goto('/#/history?run=900');
  await expect(page.locator(runRow(900)).locator('button[aria-expanded="true"]')).toBeVisible();
  await expect(page.locator(runRow(900))).toBeInViewport();
});

test('a link to a run that no longer exists says so', async ({ page }) => {
  await setupVaultMockApi(page, { historyRuns: runs });
  await page.goto('/#/history?run=9999');
  const notice = page.getByRole('status').filter({ hasText: "Run 9999 wasn't found" });
  await expect(notice).toBeVisible();
  await expect(page.locator('button[aria-expanded="true"]')).toHaveCount(0);
  await notice.getByRole('button', { name: 'Dismiss' }).click();
  await expect(notice).toHaveCount(0);
});

test('filters in the link that would hide the run are cleared', async ({ page }) => {
  await setupVaultMockApi(page, { historyRuns: runs });
  await page.goto('/#/history?status=failed&run=11');
  await expect(page.locator(runRow(11)).locator('button[aria-expanded="true"]')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Failed', exact: true })).toHaveAttribute('aria-pressed', 'false');
});
