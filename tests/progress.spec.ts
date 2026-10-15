import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Real-time progress (#456): the dashboard shows the phase and the work
// actually done instead of a percentage of fixed workflow stages.
const GB = 1024 ** 3;
const base = {
  active: true,
  job_id: 1,
  run_id: 77,
  job_name: 'Docker & Appdata',
  run_type: 'backup',
  items_total: 1,
  items_done: 0,
  started_at: new Date(Date.now() - 3600_000).toISOString(),
  current_item: 'appdata',
  current_item_type: 'folder',
  current_item_message: 'Uploading',
  queue: [],
};

test.describe('Structured backup progress on the dashboard (#456)', () => {
  test('shows bytes, percent, rate and ETA for a determinate upload', async ({ page }) => {
    await setupVaultMockApi(page, {
      runnerStatus: {
        ...base,
        current_item_percent: 37,
        phase: 'transferring',
        bytes_done: Math.round(84.2 * GB),
        bytes_total: 225 * GB,
        progress_determinate: true,
        rate_bytes_per_sec: 5 * 1024 ** 2,
        eta_seconds: 7200,
        last_progress_at: new Date().toISOString(),
      },
    });
    await page.goto('/#/');

    await expect(page.getByText('84.2 GB / 225 GB — 37%')).toBeVisible();
    // The phase label and counters read as one phrase.
    await expect(page.getByText('Uploading 84.2 GB / 225 GB — 37%', { exact: true })).toBeVisible();
    await expect(page.getByText('5 MB/s · 2h 0m left')).toBeVisible();
    const bar = page.getByRole('progressbar', { name: 'Uploading appdata' });
    await expect(bar).toHaveAttribute('aria-valuenow', '37');
  });

  test('shows an indeterminate scan without a made-up percentage, and a stall hint', async ({ page }) => {
    await setupVaultMockApi(page, {
      runnerStatus: {
        ...base,
        phase: 'scanning',
        files_done: 12340,
        last_progress_at: new Date(Date.now() - 10 * 60_000).toISOString(),
      },
    });
    await page.goto('/#/');

    await expect(page.getByText('Scanning…')).toBeVisible();
    await expect(page.getByText('12,340 files')).toBeVisible();
    const bar = page.getByRole('progressbar', { name: 'Scanning… appdata' });
    await expect(bar).toBeVisible();
    await expect(bar).not.toHaveAttribute('aria-valuenow');
    await expect(page.getByText(/No progress for 10m/)).toBeVisible();
  });
});
