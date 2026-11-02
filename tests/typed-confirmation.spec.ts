import { test as base, expect, type Page, type Request } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { DB_PATH, mockFailedPreflight, reachRestoreReview, RISKY_FLOWS, type RiskyFlow } from './helpers/risky-flows';

// Every test here fails if a browser confirm(), alert() or prompt() appears.
const test = base.extend<{ nativeDialogs: string[] }>({
  nativeDialogs: [
    async ({ page }, use) => {
      const seen: string[] = [];
      page.on('dialog', (d) => {
        seen.push(`${d.type()}: ${d.message()}`);
        void d.dismiss();
      });
      await use(seen);
      expect(seen).toEqual([]);
    },
    { auto: true },
  ],
});

function recordActions(page: Page, flow: RiskyFlow) {
  const calls: Request[] = [];
  page.on('request', (r) => {
    if (flow.isAction(r)) calls.push(r);
  });
  return calls;
}

async function openDialog(page: Page, flow: RiskyFlow) {
  await (await flow.opener(page, 'mouse')).click();
  const dialog = page.getByRole('dialog', { name: flow.title });
  await expect(dialog).toBeVisible();
  return dialog;
}

for (const flow of RISKY_FLOWS) {
  const field = (dialog: ReturnType<Page['getByRole']>) => dialog.getByLabel(`Type ${flow.word} to confirm`);
  const confirm = (dialog: ReturnType<Page['getByRole']>) => dialog.getByRole('button', { name: flow.confirmLabel, exact: true });
  const cancel = (dialog: ReturnType<Page['getByRole']>) => dialog.getByRole('button', { name: 'Cancel', exact: true });

  test.describe(`Typed confirmation: ${flow.name} (#525)`, () => {
    test.beforeEach(async ({ page }) => {
      await setupVaultMockApi(page);
      await flow.arrive(page);
    });

    test('states the loss, names the source and starts on Cancel with confirm disabled', async ({ page }) => {
      const calls = recordActions(page, flow);
      const dialog = await openDialog(page, flow);
      for (const text of [...flow.source, ...flow.loss]) {
        await expect(dialog).toContainText(text);
      }
      await expect(cancel(dialog)).toBeFocused();
      await expect(confirm(dialog)).toBeDisabled();
      await expect(field(dialog)).toHaveValue('');
      expect(calls).toHaveLength(0);
    });

    test('the description is exposed to assistive technology', async ({ page }) => {
      const dialog = await openDialog(page, flow);
      const described = await dialog.getAttribute('aria-describedby');
      await expect(page.locator(`[id="${described}"]`)).toContainText(flow.loss[0]);
    });

    test('confirm stays disabled for wrong case, partial and trailing text, and Enter sends nothing', async ({ page }) => {
      const calls = recordActions(page, flow);
      const dialog = await openDialog(page, flow);
      const lower = flow.word.toLowerCase();
      const wrong = [lower, lower[0].toUpperCase() + lower.slice(1), flow.word.slice(0, -1), flow.word[0], `${flow.word} `, ` ${flow.word}`, `${flow.word}X`, `${flow.word}${flow.word}`];
      for (const text of wrong) {
        await field(dialog).fill(text);
        await expect(confirm(dialog)).toBeDisabled();
        await field(dialog).press('Enter');
        await expect(dialog).toBeVisible();
        await expect(field(dialog)).toBeFocused();
      }
      await field(dialog).fill(flow.word);
      await expect(confirm(dialog)).toBeEnabled();
      await field(dialog).fill(`${flow.word}X`);
      await expect(confirm(dialog)).toBeDisabled();
      expect(calls).toHaveLength(0);
    });

    test('Cancel and Escape send nothing, return focus, and the word resets on reopen', async ({ page }) => {
      const calls = recordActions(page, flow);
      const opener = await flow.opener(page, 'mouse');
      for (const dismiss of ['cancel', 'escape'] as const) {
        await opener.click();
        const dialog = page.getByRole('dialog', { name: flow.title });
        await expect(field(dialog)).toHaveValue('');
        await expect(confirm(dialog)).toBeDisabled();
        await field(dialog).fill(flow.word);
        await expect(confirm(dialog)).toBeEnabled();
        if (dismiss === 'cancel') await cancel(dialog).click();
        else await page.keyboard.press('Escape');
        await expect(dialog).toHaveCount(0);
        await expect(opener).toBeFocused();
      }
      await opener.click();
      const dialog = page.getByRole('dialog', { name: flow.title });
      await expect(field(dialog)).toHaveValue('');
      await expect(confirm(dialog)).toBeDisabled();
      expect(calls).toHaveLength(0);
    });

    test('the exact word and a click send exactly one confirmation', async ({ page }) => {
      const calls = recordActions(page, flow);
      const dialog = await openDialog(page, flow);
      await field(dialog).fill(flow.word);
      expect(calls).toHaveLength(0);
      await confirm(dialog).click();
      await flow.afterConfirm(page);
      await expect.poll(() => calls.length).toBe(flow.calls);
    });

    test('a keyboard-only run works: Tab to the field, type, Enter', async ({ page }) => {
      const calls = recordActions(page, flow);
      const opener = await flow.opener(page, 'keyboard');
      await opener.focus();
      await page.keyboard.press('Enter');
      const dialog = page.getByRole('dialog', { name: flow.title });
      await expect(cancel(dialog)).toBeFocused();

      // Enter on the initial focus cancels: Cancel is the safe default.
      await page.keyboard.press('Enter');
      await expect(dialog).toHaveCount(0);
      expect(calls).toHaveLength(0);
      await expect(opener).toBeFocused();
      await page.keyboard.press('Enter');
      await expect(cancel(dialog)).toBeFocused();

      // Confirm is disabled, so Tab skips it and wraps to the field.
      await page.keyboard.press('Tab');
      await expect(field(dialog)).toBeFocused();
      await page.keyboard.type(flow.word.slice(0, -1));
      await page.keyboard.press('Enter');
      await expect(dialog).toBeVisible();
      expect(calls).toHaveLength(0);
      await page.keyboard.type(flow.word.slice(-1));
      await page.keyboard.press('Enter');
      await flow.afterConfirm(page);
      await expect.poll(() => calls.length).toBe(flow.calls);
    });

    test('the keyboard can also Tab to confirm and activate it with Space', async ({ page }) => {
      const calls = recordActions(page, flow);
      const opener = await flow.opener(page, 'keyboard');
      await opener.focus();
      await page.keyboard.press('Enter');
      const dialog = page.getByRole('dialog', { name: flow.title });
      await page.keyboard.press('Shift+Tab');
      await expect(field(dialog)).toBeFocused();
      await page.keyboard.type(flow.word);
      await page.keyboard.press('Tab');
      await expect(cancel(dialog)).toBeFocused();
      await page.keyboard.press('Tab');
      await expect(confirm(dialog)).toBeFocused();
      await page.keyboard.press('Space');
      await flow.afterConfirm(page);
      await expect.poll(() => calls.length).toBe(flow.calls);
    });

    test('fits a 375 px viewport with every control reachable', async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 667 });
      const dialog = await openDialog(page, flow);
      // Starting on Cancel must not scroll the title away, and Cancel must be visible.
      await expect(dialog.getByRole('heading', { name: flow.title })).toBeInViewport({ ratio: 1 });
      await expect(cancel(dialog)).toBeInViewport({ ratio: 1 });
      for (const control of [field(dialog), cancel(dialog), confirm(dialog)]) {
        await control.scrollIntoViewIfNeeded();
        await expect(control).toBeInViewport();
      }
      const panel = dialog.locator('div.overflow-y-auto').first();
      expect(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    });
  });
}

test.describe('Typed confirmation: request in flight (#525)', () => {
  for (const flow of RISKY_FLOWS.filter((f) => f.word !== 'RESTORE')) {
    test(`${flow.name} locks while pending and can't be sent twice`, async ({ page }) => {
      await setupVaultMockApi(page);
      await flow.arrive(page);
      const calls: Request[] = [];
      page.on('request', (r) => { if (flow.isAction(r)) calls.push(r); });
      let release: () => void = () => {};
      const held = new Promise<void>((resolve) => { release = resolve; });
      await page.route(flow.actionUrl, async (route) => {
        if (!flow.isAction(route.request())) return route.fallback();
        await held;
        return route.fallback();
      });

      await (await flow.opener(page, 'mouse')).click();
      const dialog = page.getByRole('dialog', { name: flow.title });
      const input = dialog.getByLabel(`Type ${flow.word} to confirm`);
      await input.fill(flow.word);
      await input.press('Enter');
      await expect.poll(() => calls.length).toBe(1);
      await expect(input).toBeDisabled();
      await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
      await expect(dialog.getByRole('button', { name: /…$/ })).toBeDisabled();

      // Chromium closes directly on a repeated Escape, so press it more than once.
      await page.keyboard.press('Escape');
      await page.keyboard.press('Escape');
      await page.keyboard.press('Escape');
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole('button', { name: /…$/ })).toBeVisible();

      release();
      await flow.afterConfirm(page);
      expect(calls).toHaveLength(1);
    });
  }
});

test.describe('Typed confirmation: Storage database replacement details (#525)', () => {
  const flow = RISKY_FLOWS[0];

  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    await flow.arrive(page);
  });

  test('sends the chosen file for the chosen storage, then returns focus to the Import button', async ({ page }) => {
    const calls: Request[] = [];
    page.on('request', (r) => { if (flow.isAction(r)) calls.push(r); });
    const importButton = page.getByRole('button', { name: 'Import backups' }).first();
    await (await flow.opener(page, 'mouse')).click();
    const dialog = page.getByRole('dialog', { name: flow.title });
    await dialog.getByLabel('Type REPLACE to confirm').fill('REPLACE');
    await dialog.getByRole('button', { name: flow.confirmLabel }).click();
    await flow.afterConfirm(page);
    expect(calls[0].postDataJSON()).toMatchObject({ storage_path: DB_PATH, verify_only: false });
    await expect(importButton).toBeFocused();
  });

  test('Escape and Cancel close only the confirmation, leaving the import modal open', async ({ page }) => {
    await (await flow.opener(page, 'mouse')).click();
    const importDialog = page.getByRole('dialog', { name: /Import Backups/ });
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog', { name: flow.title })).toHaveCount(0);
    await expect(importDialog).toBeVisible();

    await importDialog.getByRole('button', { name: /Vault Database/ }).click();
    await page.getByRole('dialog', { name: flow.title }).getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('dialog', { name: flow.title })).toHaveCount(0);
    await expect(importDialog).toBeVisible();
  });

  test('a failed replacement reports it, keeps the import modal, and demands the word again', async ({ page }) => {
    await page.route(flow.actionUrl, (route) =>
      route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'failed the integrity check' }) }),
    );
    const opener = await flow.opener(page, 'mouse');
    await opener.click();
    const dialog = page.getByRole('dialog', { name: flow.title });
    await dialog.getByLabel('Type REPLACE to confirm').fill('REPLACE');
    await dialog.getByRole('button', { name: flow.confirmLabel }).click();
    await expect(page.getByRole('alert').filter({ hasText: /failed the integrity check/ })).toBeVisible();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole('dialog', { name: /Import Backups/ })).toBeVisible();
    await opener.click();
    await expect(dialog.getByLabel('Type REPLACE to confirm')).toHaveValue('');
    await expect(dialog.getByRole('button', { name: flow.confirmLabel })).toBeDisabled();
  });
});

test.describe('Typed confirmation: History purge details (#525)', () => {
  test('after purging, focus lands on the page rather than the now-disabled button', async ({ page }) => {
    const flow = RISKY_FLOWS[2];
    await setupVaultMockApi(page);
    await flow.arrive(page);
    await (await flow.opener(page, 'mouse')).click();
    const input = page.getByRole('dialog').getByLabel('Type PURGE to confirm');
    await input.fill('PURGE');
    await input.press('Enter');
    await flow.afterConfirm(page);
    await expect(page.locator('#main-content')).toBeFocused();
  });

  test('a failed purge closes the dialog and reports the error', async ({ page }) => {
    const flow = RISKY_FLOWS[2];
    await setupVaultMockApi(page);
    await flow.arrive(page);
    await page.route(flow.actionUrl, (route) =>
      route.request().method() === 'DELETE'
        ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'database is locked' }) })
        : route.fallback(),
    );
    await (await flow.opener(page, 'mouse')).click();
    const input = page.getByRole('dialog').getByLabel('Type PURGE to confirm');
    await input.fill('PURGE');
    await input.press('Enter');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByText(/database is locked/)).toBeVisible();
  });
});

test.describe('Typed confirmation: RestoreWizard details (#525)', () => {
  const flow = RISKY_FLOWS[1];

  test('cancelling keeps the review and its pre-flight results', async ({ page }) => {
    await setupVaultMockApi(page);
    await flow.arrive(page);
    await (await flow.opener(page, 'mouse')).click();
    await page.keyboard.press('Escape');
    await expect(page.getByText('Free space').first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Restore anyway' })).toBeFocused();
    await expect(page.getByRole('button', { name: /^Start Restore/ })).toBeDisabled();
  });

  test('submits every selected job, passing and failing', async ({ page }) => {
    await setupVaultMockApi(page);
    await flow.arrive(page);
    const urls: string[] = [];
    page.on('request', (r) => { if (flow.isAction(r)) urls.push(r.url()); });
    await (await flow.opener(page, 'mouse')).click();
    const dialog = page.getByRole('dialog', { name: flow.title });
    await dialog.getByLabel('Type RESTORE to confirm').fill('RESTORE');
    await dialog.getByRole('button', { name: flow.confirmLabel }).click();
    await expect.poll(() => urls.length).toBe(2);
    expect(urls.some((u) => u.endsWith('/jobs/1/restore'))).toBe(true);
    expect(urls.some((u) => u.endsWith('/jobs/2/restore'))).toBe(true);
  });

  test('warns about destination clearing only when it is on', async ({ page }) => {
    await setupVaultMockApi(page);
    await flow.arrive(page);
    const opener = await flow.opener(page, 'mouse');
    await opener.click();
    const dialog = page.getByRole('dialog', { name: flow.title });
    await expect(dialog).toContainText('are removed for: Docker & Appdata, VM Backups');
    await page.keyboard.press('Escape');

    const clearing = page.getByRole('checkbox', { name: /clear/i });
    await clearing.first().uncheck();
    await clearing.nth(1).uncheck();
    await opener.click();
    await expect(dialog).toBeVisible();
    await expect(dialog).not.toContainText('are removed for');
  });

  test('normal restores keep their review step, with no typed dialog', async ({ page }) => {
    await setupVaultMockApi(page);
    const calls: Request[] = [];
    page.on('request', (r) => { if (flow.isAction(r)) calls.push(r); });
    await reachRestoreReview(page);
    await expect(page.getByLabel('passed').first()).toBeVisible();
    await expect(page.getByText(/This will overwrite existing data/)).toBeVisible();
    expect(calls).toHaveLength(0);
    await expect(page.getByRole('button', { name: 'Restore anyway' })).toHaveCount(0);
    await page.getByRole('button', { name: /^Start Restore/ }).click();
    await expect.poll(() => calls.length).toBe(2);
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  test('only the job whose pre-flight failed is listed as failed', async ({ page }) => {
    await setupVaultMockApi(page);
    await mockFailedPreflight(page);
    await reachRestoreReview(page);
    await (await flow.opener(page, 'mouse')).click();
    const dialog = page.getByRole('dialog', { name: flow.title });
    await expect(dialog).toContainText('Failed checks: VM Backups');
    await expect(dialog).not.toContainText('Failed checks: Docker & Appdata');
  });
});
