import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Design-system consistency (#697, #698, #710): touch-sized fields on phones,
// one switch, one selected-card style, distinct storage icons and the 12px floor.

/** Visible text fields, selects and textareas under `scope` shorter than 44 px. */
async function shortFields(page: Page, scope = 'main') {
  return page.evaluate((sel) => {
    const out: string[] = [];
    for (const el of document.querySelectorAll(`${sel} input, ${sel} select, ${sel} textarea`)) {
      const input = el as HTMLInputElement;
      if (['checkbox', 'radio', 'hidden', 'range', 'file'].includes(input.type)) continue;
      const box = input.getBoundingClientRect();
      if (box.width < 2 || box.height < 2) continue;
      if (input.offsetHeight < 44) out.push(`${input.id || input.getAttribute('aria-label') || input.placeholder || input.tagName}: ${input.offsetHeight}px`);
    }
    return out;
  }, scope);
}

test.describe('phone layout (#697)', () => {
  test.use({ viewport: { width: 375, height: 812 } });

  for (const hash of ['#/jobs', '#/restore', '#/history', '#/logs', '#/anomalies', '#/settings/general', '#/settings/security']) {
    test(`${hash} fields are 44 px tall and the page does not scroll sideways`, async ({ page }) => {
      await setupVaultMockApi(page);
      await page.goto(`/${hash}`);
      await page.waitForLoadState('networkidle');
      expect(await shortFields(page)).toEqual([]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    });
  }

  test('Jobs row checkbox and enable switch have 44 px targets', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/jobs');
    const sw = page.getByRole('switch', { name: 'Enable Docker & Appdata' });
    const box = (await sw.boundingBox())!;
    expect(Math.round(box.width)).toBeGreaterThanOrEqual(44);
    expect(Math.round(box.height)).toBeGreaterThanOrEqual(44);
    const label = page.locator('label', { has: page.getByRole('checkbox', { name: 'Select Docker & Appdata' }) });
    const lbox = (await label.boundingBox())!;
    expect(Math.round(Math.min(lbox.width, lbox.height))).toBeGreaterThanOrEqual(44);
  });
});

test.describe('shared components (#698, #710)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('no readable text renders below 12 px on the Dashboard and Logs', async ({ page }) => {
    for (const hash of ['#/', '#/logs', '#/history']) {
      await page.goto(`/${hash}`);
      await page.waitForLoadState('networkidle');
      const tiny = await page.evaluate(() => {
        const out: string[] = [];
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = walker.nextNode(); n; n = walker.nextNode()) {
          const el = n.parentElement;
          if (!el || !n.textContent?.trim() || el.closest('[aria-hidden="true"], .sr-only')) continue;
          const size = parseFloat(getComputedStyle(el).fontSize);
          if (size < 12 && el.getClientRects().length) out.push(`${size}px "${n.textContent.trim().slice(0, 30)}"`);
        }
        return out;
      });
      expect(tiny, hash).toEqual([]);
    }
  });

  test('a Settings switch toggles and keeps its name and state', async ({ page }) => {
    await page.goto('/#/settings/general');
    const sw = page.getByRole('switch', { name: 'Toggle adaptive upload throttling' });
    const before = await sw.getAttribute('aria-checked');
    await sw.click();
    await expect(sw).toHaveAttribute('aria-checked', before === 'true' ? 'false' : 'true');
    await sw.focus();
    await page.keyboard.press('Space');
    await expect(sw).toHaveAttribute('aria-checked', before!);
  });

  test('storage types have distinct icons and select with the shared card style', async ({ page }) => {
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const group = page.getByRole('dialog').getByRole('group', { name: 'Storage type' });
    const paths = await group.locator('button svg path').evaluateAll((els) => els.map((e) => e.getAttribute('d')));
    expect(paths).toHaveLength(6);
    expect(new Set(paths).size).toBe(6);
    const s3 = group.getByRole('button', { name: /^S3/ });
    await s3.click();
    await expect(s3).toHaveAttribute('aria-pressed', 'true');
    await expect(s3).toHaveClass(/border-vault/);
    await expect(group.getByRole('button', { name: 'Local Path' })).not.toHaveClass(/border-vault/);
  });

  test('each page has the same icon in the sidebar and the command palette', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto('/#/');
    const sidebar = await page.locator('aside nav a').evaluateAll((links) =>
      Object.fromEntries(links.map((a) => [a.textContent!.trim(), a.querySelector('path')?.getAttribute('d')])));
    await page.keyboard.press('Control+k');
    const options = page.getByRole('option', { name: /^Go to / });
    await expect(options.first()).toBeVisible();
    const palette = await options.evaluateAll((opts) =>
      Object.fromEntries(opts.map((o) => [o.textContent!.replace(/↵/g, '').trim().replace(/^Go to /, ''), o.querySelector('path')?.getAttribute('d')])));
    expect(Object.keys(palette).length).toBeGreaterThan(5);
    for (const [label, d] of Object.entries(palette)) expect(d, label).toBe(sidebar[label]);
  });
});
