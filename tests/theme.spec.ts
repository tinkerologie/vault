import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Theme styles (#532): the picker, migration of saved 8-Bit and 16-Bit
// preferences, and where the pixel fonts are fetched from.

const ROUTES = ['#/', '#/jobs', '#/storage', '#/restore', '#/history', '#/recovery', '#/replication', '#/logs', '#/anomalies', '#/settings'];

function seed(page: Page, values: Record<string, string>) {
  // Only the first load: a later reload must see what the app itself persisted.
  return page.addInitScript((v) => {
    if (sessionStorage.getItem('seeded')) return;
    sessionStorage.setItem('seeded', '1');
    for (const [key, value] of Object.entries(v)) localStorage.setItem(key, value);
  }, values);
}

/** Records every font and off-origin request, and anything the page logs as an error. */
function watch(page: Page) {
  const fonts: string[] = [];
  const foreign: string[] = [];
  const errors: string[] = [];
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.protocol === 'data:') return;
    if (request.resourceType() === 'font' || url.pathname.endsWith('.woff2')) fonts.push(request.url());
    if (url.origin !== 'http://127.0.0.1:4173') foreign.push(request.url());
  });
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text());
  });
  return { fonts, foreign, errors };
}

async function visitAllRoutes(page: Page) {
  for (const hash of ROUTES) {
    await page.goto(`/${hash}`);
    await expect(page.locator('main')).toBeVisible();
    await page.waitForLoadState('networkidle');
  }
}

const stored = (page: Page, key: string) => page.evaluate((k) => localStorage.getItem(k), key);
const styleButton = (page: Page, name: string) => page.locator('#set-appearance').getByRole('button', { name, exact: true });
const styleButtons = (page: Page) => page.locator('#set-appearance div:has(> p:text-is("Theme style")) button');

async function expectRetroSelected(page: Page) {
  await expect(page.locator('html')).toHaveClass(/\btheme-retro\b/);
  await expect(styleButton(page, 'Retro')).toHaveClass(/bg-vault/);
  await expect(styleButton(page, 'Default')).not.toHaveClass(/bg-vault/);
}

test.describe('Theme style picker', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('offers exactly Default, 1-Bit and Retro', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(styleButtons(page)).toHaveText(['Default', '1-Bit', 'Retro']);
  });

  test('switches style with the mouse, keeping one style class and persisting it', async ({ page }) => {
    await page.goto('/#/settings');
    const html = page.locator('html');

    await styleButton(page, 'Retro').click();
    await expectRetroSelected(page);
    expect(await stored(page, 'vault-style')).toBe('retro');

    await styleButton(page, '1-Bit').click();
    await expect(html).toHaveClass(/\btheme-1bit\b/);
    await expect(html).not.toHaveClass(/\btheme-retro\b/);
    expect(await stored(page, 'vault-style')).toBe('1bit');

    await styleButton(page, 'Default').click();
    await expect(html).not.toHaveClass(/\btheme-/);
    expect(await stored(page, 'vault-style')).toBe('default');
  });

  test('switches style from the keyboard', async ({ page }) => {
    await page.goto('/#/settings');
    await styleButton(page, 'Retro').focus();
    await page.keyboard.press('Enter');
    await expectRetroSelected(page);
    await page.keyboard.press('Shift+Tab');
    await expect(styleButton(page, '1-Bit')).toBeFocused();
    await page.keyboard.press('Space');
    await expect(page.locator('html')).toHaveClass(/\btheme-1bit\b/);
  });

  test('keeps a chosen style after a reload', async ({ page }) => {
    await page.goto('/#/settings');
    await styleButton(page, 'Retro').click();
    await page.reload();
    await expectRetroSelected(page);
  });

  test('fits a 375 px viewport in every style', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/#/settings');
    for (const name of ['1-Bit', 'Retro', 'Default']) {
      await styleButton(page, name).click();
      await expect(styleButtons(page)).toHaveCount(3);
      const widths = await styleButtons(page).evaluateAll((els) => els.map((el) => el.scrollWidth - el.clientWidth));
      expect(widths, `${name} labels overflow their buttons`).toEqual([0, 0, 0]);
      const overflow = await page.locator('main').evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(overflow, `${name} overflows the page`).toBeLessThanOrEqual(0);
    }
  });
});

test.describe('Themed navigation links', () => {
  const seed = (page: Page, style: string) => page.addInitScript((s) => {
    localStorage.setItem('vault-style', s);
    localStorage.setItem('vault-mode', 'dark');
  }, style);
  const nav = (page: Page) => page.getByRole('navigation', { name: 'Main navigation' });

  for (const style of ['1bit', 'retro']) {
    test(`${style}: the active sidebar link has the solid accent fill`, async ({ page }) => {
      await setupVaultMockApi(page);
      await seed(page, style);
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await page.goto('/#/jobs');
      const active = nav(page).getByRole('link', { name: 'Jobs', exact: true });
      await expect(active).toBeVisible();
      const paint = await active.evaluate((el) => {
        const probe = document.createElement('i');
        probe.style.cssText = 'background-color:var(--color-vault);color:var(--color-on-vault)';
        document.body.append(probe);
        const want = getComputedStyle(probe);
        const got = getComputedStyle(el);
        const result = { bg: got.backgroundColor, fg: got.color, wantBg: want.backgroundColor, wantFg: want.color };
        probe.remove();
        return result;
      });
      expect(paint.bg).toBe(paint.wantBg);
      expect(paint.fg).toBe(paint.wantFg);
    });
  }

  test('1bit: sidebar links are square and monospace', async ({ page }) => {
    await setupVaultMockApi(page);
    await seed(page, '1bit');
    await page.goto('/#/jobs');
    const link = nav(page).getByRole('link', { name: 'Storage', exact: true });
    await expect(link).toHaveCSS('border-radius', '0px');
    await expect(link).toHaveCSS('font-family', /monospace/);
  });

  test('retro: sidebar and mobile links use the pixel font', async ({ page }) => {
    await setupVaultMockApi(page);
    await seed(page, 'retro');
    await page.goto('/#/jobs');
    await expect(nav(page).getByRole('link', { name: 'Storage', exact: true })).toHaveCSS('font-family', /VT323/);
    await page.setViewportSize({ width: 375, height: 700 });
    await expect(page.getByRole('navigation', { name: 'Mobile navigation' }).getByRole('link').first()).toHaveCSS('font-family', /VT323/);
  });
});

test.describe('Saved style migration', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  for (const saved of ['8bit', '16bit']) {
    for (const mode of ['light', 'dark']) {
      test(`moves ${saved} ${mode} to Retro without errors`, async ({ page }) => {
        const { errors } = watch(page);
        await seed(page, { 'vault-style': saved, 'vault-mode': mode });
        await page.goto('/#/settings');

        await expectRetroSelected(page);
        await expect(page.locator('html')).toHaveClass(mode === 'dark' ? /\bdark\b/ : /^(?!.*\bdark\b)/);
        await expect(page.locator(`html[class*="${saved}"]`)).toHaveCount(0);
        expect(await stored(page, 'vault-style')).toBe('retro');
        expect(await stored(page, 'vault-mode')).toBe(mode);

        await page.reload();
        await expectRetroSelected(page);
        expect(errors).toEqual([]);
      });
    }
  }

  for (const [old, mode] of [['retro', 'light'], ['retro-dark', 'dark']]) {
    test(`moves the older vault-theme value ${old} to Retro`, async ({ page }) => {
      const { errors } = watch(page);
      await seed(page, { 'vault-theme': old });
      await page.goto('/#/settings');

      await expectRetroSelected(page);
      expect(await stored(page, 'vault-style')).toBe('retro');
      expect(await stored(page, 'vault-mode')).toBe(mode);
      expect(await stored(page, 'vault-theme')).toBeNull();
      expect(errors).toEqual([]);
    });
  }

  test('still shows Retro when the migrated value cannot be saved', async ({ page }) => {
    const { errors } = watch(page);
    await seed(page, { 'vault-style': '16bit' });
    await page.addInitScript(() => {
      const setItem = Storage.prototype.setItem;
      Storage.prototype.setItem = function (key: string, value: string) {
        if (key === 'vault-style' && value === 'retro') throw new DOMException('full', 'QuotaExceededError');
        setItem.call(this, key, value);
      };
    });
    await page.goto('/#/settings');

    await expectRetroSelected(page);
    expect(await stored(page, 'vault-style')).toBe('16bit');
    expect(errors).toEqual([]);
  });

  for (const invalid of ['neon', '8BIT', '32bit', '', 'constructor', '__proto__']) {
    test(`falls back to Default for the invalid value ${JSON.stringify(invalid)}`, async ({ page }) => {
      const { errors, fonts } = watch(page);
      await seed(page, { 'vault-style': invalid });
      await page.goto('/#/settings');

      await expect(styleButton(page, 'Default')).toHaveClass(/bg-vault/);
      await expect(page.locator('html')).not.toHaveClass(/\btheme-/);
      await page.waitForLoadState('networkidle');
      expect(fonts).toEqual([]);
      expect(errors).toEqual([]);
    });
  }
});

test.describe('Pixel fonts', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  for (const style of ['default', '1bit']) {
    for (const mode of ['light', 'dark']) {
      test(`${style} ${mode} requests no fonts on any page`, async ({ page }) => {
        const { fonts, foreign } = watch(page);
        await seed(page, { 'vault-style': style, 'vault-mode': mode });
        await visitAllRoutes(page);
        expect(fonts).toEqual([]);
        expect(foreign).toEqual([]);
      });
    }
  }

  for (const mode of ['light', 'dark']) {
    test(`Retro ${mode} loads both faces from the app itself`, async ({ page }) => {
      const { fonts, foreign } = watch(page);
      const statuses: number[] = [];
      page.on('response', (response) => {
        if (new URL(response.url()).pathname.endsWith('.woff2')) statuses.push(response.status());
      });
      await seed(page, { 'vault-style': 'retro', 'vault-mode': mode });
      await visitAllRoutes(page);

      const files = fonts.map((url) => new URL(url).pathname);
      expect(files.some((file) => /\/vt323-latin[^/]*\.woff2$/.test(file))).toBe(true);
      expect(files.some((file) => /\/press-start-2p-latin[^/]*\.woff2$/.test(file))).toBe(true);
      expect(fonts.every((url) => new URL(url).origin === 'http://127.0.0.1:4173')).toBe(true);
      expect(statuses.length).toBeGreaterThan(0);
      expect(statuses.every((status) => status === 200 || status === 304)).toBe(true);
      expect(foreign).toEqual([]);

      await expect(page.locator('body')).toHaveCSS('font-family', /VT323/);
      await expect(page.locator('main h1, main h2').first()).toHaveCSS('font-family', /Press Start 2P/);
    });
  }

  test('fonts are fetched only once Retro is chosen in Settings', async ({ page }) => {
    const { fonts, foreign } = watch(page);
    await page.goto('/#/settings');
    await page.waitForLoadState('networkidle');
    await styleButton(page, '1-Bit').click();
    await page.waitForLoadState('networkidle');
    expect(fonts).toEqual([]);

    await styleButton(page, 'Retro').click();
    await expect.poll(() => fonts.length).toBeGreaterThan(0);
    expect(foreign).toEqual([]);
  });

  test('a migrated preference loads the same self-hosted fonts', async ({ page }) => {
    const { fonts, foreign } = watch(page);
    await seed(page, { 'vault-style': '8bit' });
    await page.goto('/#/settings');
    await expect.poll(() => fonts.length).toBeGreaterThan(0);
    expect(foreign).toEqual([]);
  });

  test('the document preloads no fonts and links no font service', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(page.locator('link[rel="preload"][as="font"]')).toHaveCount(0);
    await expect(page.locator('link[href*="fonts.g"]')).toHaveCount(0);
  });
});
