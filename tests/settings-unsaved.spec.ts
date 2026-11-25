import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Every Settings section with its own Save guards leaving the page by any
// route: Back, Forward, the command palette and in-app links (#719).

const discard = (page: Page) => page.getByRole('dialog', { name: 'Discard changes?' });

// Arrive on Settings from Jobs, so Back has somewhere to go.
async function openSettings(page: Page) {
  await page.goto('/#/jobs');
  await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible();
  await page.goto('/#/settings');
  await expect(page.getByRole('spinbutton', { name: 'Max retries' })).toBeVisible();
}

// An unsaved Retry Policy edit on General and a Discord edit on Notifications.
async function editRetryAndDiscord(page: Page) {
  const retry = page.locator('#set-retry');
  await expect(page.getByText('Unsaved changes')).toHaveCount(0);
  await retry.getByRole('spinbutton', { name: 'Max retries' }).fill('7');
  await expect(retry.getByText('Unsaved changes')).toBeVisible();
  // Moving between Settings tabs keeps the edit, so it doesn't ask.
  await page.getByRole('tab', { name: 'Notifications', exact: true }).click();
  await expect(discard(page)).toHaveCount(0);
  await page.getByLabel('Bot Username').fill('Vault bot');
  await expect(page.getByText('Unsaved changes')).toBeVisible();
  await expect(page).toHaveURL(/#\/settings\/notifications$/);
}

// Answer Keep editing and check the page stayed mounted with both edits. Tabs
// are switched with the arrow keys, which replace the history entry, so the
// history around us is left as it was.
async function keepEditing(page: Page, url = /#\/settings\/notifications$/) {
  await expect(discard(page)).toContainText('Retry Policy, Discord');
  await discard(page).getByRole('button', { name: 'Keep editing' }).click();
  await expect(discard(page)).toHaveCount(0);
  await expect(page).toHaveURL(url);
  await page.getByRole('tab', { name: 'Notifications', exact: true }).focus();
  await page.keyboard.press('Home');
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(page.getByLabel('Bot Username')).toHaveValue('Vault bot');
  await page.keyboard.press('Home');
  await expect(page.getByRole('spinbutton', { name: 'Max retries' })).toHaveValue('7');
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(page).toHaveURL(/#\/settings\/notifications$/);
}

test.describe('Settings unsaved changes (#719)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('Back asks first, Keep editing stays without a new history entry, Discard leaves', async ({ page }) => {
    await openSettings(page);
    await editRetryAndDiscord(page);
    const length = await page.evaluate(() => history.length);

    // Back to the previous Settings tab stays on the page: no question.
    await page.goBack();
    await expect(page).toHaveURL(/#\/settings$/);
    await expect(discard(page)).toHaveCount(0);
    await page.goBack();
    await expect(discard(page)).toBeVisible();
    await keepEditing(page, /#\/settings$/);
    expect(await page.evaluate(() => history.length)).toBe(length);

    // keepEditing's arrow keys left this entry on Notifications; Jobs is next.
    await page.goBack();
    await discard(page).getByRole('button', { name: 'Discard' }).click();
    await expect(page).toHaveURL(/#\/jobs$/);
    await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible();
  });

  test('Forward asks first', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(page.getByRole('spinbutton', { name: 'Max retries' })).toBeVisible();
    await page.getByRole('navigation').getByRole('link', { name: 'Jobs' }).first().click();
    await expect(page).toHaveURL(/#\/jobs$/);
    await page.goBack();
    await expect(page.getByRole('spinbutton', { name: 'Max retries' })).toBeVisible();

    // Edit without adding history: arrow keys on the tab list replace the
    // entry, so Jobs stays ahead of us.
    await page.getByRole('spinbutton', { name: 'Max retries' }).fill('7');
    await page.getByRole('tab', { name: 'General', exact: true }).focus();
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await expect(page).toHaveURL(/#\/settings\/notifications$/);
    await page.getByLabel('Bot Username').fill('Vault bot');

    await page.goForward();
    await expect(discard(page)).toBeVisible();
    await keepEditing(page);
    await page.goForward();
    await discard(page).getByRole('button', { name: 'Discard' }).click();
    await expect(page).toHaveURL(/#\/jobs$/);
  });

  test('command palette navigation asks first', async ({ page }) => {
    await openSettings(page);
    await editRetryAndDiscord(page);
    await page.keyboard.press('Control+k');
    await page.getByRole('combobox', { name: 'Search commands' }).fill('logs');
    await page.getByRole('group', { name: 'Pages' }).getByRole('option', { name: /^Go to Logs/ }).click();
    await expect(discard(page)).toBeVisible();
    await keepEditing(page);

    await page.keyboard.press('Control+k');
    await page.getByRole('combobox', { name: 'Search commands' }).fill('logs');
    await page.getByRole('group', { name: 'Pages' }).getByRole('option', { name: /^Go to Logs/ }).click();
    await discard(page).getByRole('button', { name: 'Discard' }).click();
    await expect(page).toHaveURL(/#\/logs$/);
  });

  test('a sidebar link asks first', async ({ page }) => {
    await openSettings(page);
    await editRetryAndDiscord(page);
    await page.getByRole('navigation').getByRole('link', { name: 'History' }).first().click();
    await expect(discard(page)).toBeVisible();
    await keepEditing(page);
    await page.getByRole('navigation').getByRole('link', { name: 'History' }).first().click();
    await discard(page).getByRole('button', { name: 'Discard' }).click();
    await expect(page).toHaveURL(/#\/history$/);
  });

  test('no prompt after saving both sections', async ({ page }) => {
    await openSettings(page);
    await editRetryAndDiscord(page);
    await page.getByRole('button', { name: 'Save Discord Settings' }).click();
    await page.getByRole('tab', { name: 'General', exact: true }).click();
    await page.getByRole('button', { name: 'Save Retry Policy' }).click();
    await expect(page.getByText('Unsaved changes')).toHaveCount(0);

    // Back through General, Notifications and the tab-less entry to Jobs.
    for (const url of [/#\/settings\/notifications$/, /#\/settings$/, /#\/jobs$/]) {
      await page.goBack();
      await expect(page).toHaveURL(url);
    }
    await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible();
    await expect(discard(page)).toHaveCount(0);
  });
});
