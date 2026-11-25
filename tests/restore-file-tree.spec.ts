import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// The partial-restore file tree (#703): the WAI-ARIA tree pattern, bounded
// rendering, and responsiveness on a 100k-file listing.

const contentsGlob = '**/jobs/1/restore-points/1001/contents*';

async function openTree(page: Page) {
  await page.goto('/#/restore');
  await page.getByRole('button', { name: /plex/i }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
  await page.locator('summary', { hasText: 'plex' }).click();
  const tree = page.getByRole('tree', { name: 'Files in plex' });
  await expect(tree).toBeVisible();
  return tree;
}

const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => setTimeout(resolve, 0)));

test.describe('File tree keyboard and semantics', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    await page.route(contentsGlob, route => route.fulfill({ json: { version: 1, archive: 'plex', files: [
      { path: '/config/settings.xml', size: 20 },
      { path: '/config/db/main.db', size: 200 },
      { path: '/config/db/wal.db', size: 10 },
      { path: '/logs/plex.log', size: 5 },
    ] } }));
  });

  test('is one Tab stop and follows the ARIA tree keys', async ({ page }) => {
    const tree = await openTree(page);
    const items = tree.getByRole('treeitem');
    await expect(items).toHaveCount(2);
    await expect(tree.locator('[role="treeitem"][tabindex="0"]')).toHaveCount(1);

    await page.getByRole('button', { name: 'Collapse all' }).focus();
    await page.keyboard.press('Tab');
    const config = tree.getByRole('treeitem', { name: /^config / });
    await expect(config).toBeFocused();
    await expect(config).toHaveAttribute('aria-level', '1');
    await expect(config).toHaveAttribute('aria-expanded', 'false');
    await expect(config).toHaveAttribute('aria-checked', 'true');

    await page.keyboard.press('ArrowRight');
    await expect(config).toHaveAttribute('aria-expanded', 'true');
    await page.keyboard.press('ArrowRight');
    const db = tree.getByRole('treeitem', { name: /^db / });
    await expect(db).toBeFocused();
    await expect(db).toHaveAttribute('aria-level', '2');
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowDown');
    const main = tree.getByRole('treeitem', { name: /^main\.db / });
    await expect(main).toBeFocused();
    await page.keyboard.press('Space');
    await expect(main).toHaveAttribute('aria-checked', 'false');
    await expect(db).toHaveAttribute('aria-checked', 'mixed');
    await expect(config).toHaveAttribute('aria-checked', 'mixed');
    await expect(page.getByText('3 of 4 files selected')).toBeVisible();

    await page.keyboard.press('ArrowLeft');
    await expect(db).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(db).toHaveAttribute('aria-expanded', 'false');
    await page.keyboard.press('End');
    await expect(tree.getByRole('treeitem', { name: /^logs / })).toBeFocused();
    await page.keyboard.press('Home');
    await expect(config).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(tree.getByRole('treeitem').filter({ has: page.locator(':focus') })).toHaveCount(0);

    const { violations } = await new AxeBuilder({ page }).include('[data-testid="file-tree"]')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).disableRules(['color-contrast']).analyze();
    expect(violations.map(v => v.id)).toEqual([]);
  });
});

// Performance on a synthetic 100k-file listing. Thresholds are deliberately
// tolerant (150 ms) so shared CI runners pass; locally each step is a few ms
// to a few tens of ms. Before #703 the same steps took 1.2 s (toggle) and
// 1.0 s (filter keystroke).
test.describe('File tree with 100k files', () => {
  const FILES = 100_000;
  const BUDGET_MS = 150;
  const FILTER_DEBOUNCE_MS = 150;

  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    const files = [];
    for (let i = 0; i < FILES; i++) files.push({ path: `/appdata/d${i % 50}/s${i % 7}/file-${i}.dat`, size: 1024 });
    const body = JSON.stringify({ version: 1, archive: 'plex', files });
    await page.route(contentsGlob, route => route.fulfill({ contentType: 'application/json', body }));
  });

  test('keeps toggling and filtering under budget with bounded rows', async ({ page }) => {
    test.setTimeout(90_000);
    const tree = await openTree(page);
    await expect(page.getByText(/All 100,?000 files selected/).first()).toBeVisible({ timeout: 30_000 });

    await page.getByRole('button', { name: 'Expand all' }).click();
    const rendered = await tree.getByRole('treeitem').count();
    expect(rendered, 'rendered rows stay bounded').toBeLessThan(60);

    await page.evaluate(() => {
      const w = window as unknown as { __longTasks: number[] };
      w.__longTasks = [];
      new PerformanceObserver(list => { for (const e of list.getEntries()) w.__longTasks.push(e.duration); })
        .observe({ type: 'longtask', buffered: false });
    });

    // Toggle the root folder (all 100k leaves) and one file.
    const toggleMs = await page.evaluate(async (frame) => {
      const next = new Function(`return (${frame})`)() as () => Promise<void>;
      const times: number[] = [];
      for (const selector of ['[role="treeitem"][aria-level="1"]', '[role="treeitem"][aria-level="4"]']) {
        const row = document.querySelector(selector)!;
        const t0 = performance.now();
        row.querySelector('[data-part="check"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await next();
        times.push(performance.now() - t0);
      }
      return Math.max(...times);
    }, nextFrame.toString());
    expect(toggleMs, 'toggle latency (ms)').toBeLessThan(BUDGET_MS);

    // One filter keystroke: the input handler stays cheap, and the debounced
    // filter applies within budget once typing pauses.
    const filter = page.getByRole('textbox', { name: 'Filter paths in plex' });
    const { keystrokeMs, applyMs } = await page.evaluate(async ({ frame, debounce }) => {
      const next = new Function(`return (${frame})`)() as () => Promise<void>;
      const input = document.querySelector('input[aria-label="Filter paths in plex"]') as HTMLInputElement;
      const t0 = performance.now();
      input.value = 'file-99999';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await next();
      const keystroke = performance.now() - t0;
      await new Promise<void>(resolve => {
        const check = () => (document.querySelectorAll('[role="treeitem"]').length === 4 ? resolve() : requestAnimationFrame(check));
        check();
      });
      return { keystrokeMs: keystroke, applyMs: performance.now() - t0 - debounce };
    }, { frame: nextFrame.toString(), debounce: FILTER_DEBOUNCE_MS });
    await expect(filter).toHaveValue('file-99999');
    expect(keystrokeMs, 'filter keystroke latency (ms)').toBeLessThan(BUDGET_MS);
    expect(applyMs, 'debounced filter apply (ms)').toBeLessThan(BUDGET_MS);

    const longest = await page.evaluate(() => Math.max(0, ...(window as unknown as { __longTasks: number[] }).__longTasks));
    console.log(`file tree 100k: toggle ${toggleMs.toFixed(1)} ms, keystroke ${keystrokeMs.toFixed(1)} ms, filter ${applyMs.toFixed(1)} ms, longest task ${longest.toFixed(1)} ms, rows ${rendered}`);
    expect(longest, 'longest main-thread task (ms)').toBeLessThan(BUDGET_MS);
  });
});
