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

  test('offers exactly Default, 1-Bit, Retro, Dracula and Catppuccin', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(styleButtons(page)).toHaveText(['Default', '1-Bit', 'Retro', 'Dracula', 'Catppuccin']);
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
    for (const name of ['1-Bit', 'Retro', 'Dracula', 'Catppuccin', 'Default']) {
      await styleButton(page, name).click();
      await expect(styleButtons(page)).toHaveCount(5);
      const widths = await styleButtons(page).evaluateAll((els) => els.map((el) => el.scrollWidth - el.clientWidth));
      expect(widths, `${name} labels overflow their buttons`).toEqual([0, 0, 0, 0, 0]);
      const overflow = await page.locator('main').evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(overflow, `${name} overflows the page`).toBeLessThanOrEqual(0);
    }
  });
});

test.describe('Plugin-served assets', () => {
  test('the app still loads from a deep URL', async ({ page }) => {
    await setupVaultMockApi(page);
    const failed: string[] = [];
    page.on('response', (response) => {
      const { pathname } = new URL(response.url());
      if (pathname.includes('/assets/') && (!pathname.startsWith('/assets/') || !response.ok())) failed.push(response.url());
    });
    await page.goto('/some/unknown/path');
    await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible();
    expect(failed).toEqual([]);
  });

  test('Retro loads its fonts when the app is served from the plugin path', async ({ page }) => {
    await setupVaultMockApi(page);
    const responses: { url: string; status: number }[] = [];
    page.on('response', (response) => {
      if (new URL(response.url()).pathname.endsWith('.woff2')) responses.push({ url: response.url(), status: response.status() });
    });
    // Mirrors app.php: index.html's asset paths point at the plugin's UI folder.
    await page.route('**/plugins/vault/ui/**', async (route) => {
      const { pathname } = new URL(route.request().url());
      const upstream = await route.fetch({ url: `http://127.0.0.1:4173/${pathname.replace('/plugins/vault/ui/', '')}` });
      if (pathname !== '/plugins/vault/ui/') return route.fulfill({ response: upstream });
      const html = (await upstream.text()).replaceAll(/(src|href)="(\.?\/)assets\//g, '$1="/plugins/vault/ui/assets/');
      return route.fulfill({ response: upstream, body: html });
    });
    await page.addInitScript(() => localStorage.setItem('vault-style', 'retro'));
    await page.goto('/plugins/vault/ui/');
    await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible();
    await expect.poll(() => responses.length).toBeGreaterThan(0);
    for (const { url, status } of responses) {
      expect(new URL(url).pathname).toMatch(/^\/plugins\/vault\/ui\/assets\//);
      expect(status).toBe(200);
    }
    // Requests still in flight must not outlive the test and fail the run.
    await page.unrouteAll({ behavior: 'ignoreErrors' });
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

  for (const style of ['default', '1bit', 'dracula', 'catppuccin']) {
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

// Colour-only styles (#590 Dracula, #591 Catppuccin): Default fonts and layout,
// their own palette, and the colour mode they allow.

const modeButton = (page: Page, name: string) => page.getByRole('group', { name: 'Color mode' }).getByRole('button', { name, exact: true });
const token = (page: Page, name: string) => page.evaluate((n) => {
  const probe = document.createElement('i');
  probe.style.color = `var(${n})`;
  document.body.append(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();
  return color;
}, name);
const rgbOf = (hex: string) => `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`;
const sidebarBrand = (page: Page) => page.getByRole('complementary', { name: 'Sidebar' });

test.describe('Dracula style', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' });
  });

  test('applies the palette without a reload and keeps it after one', async ({ page }) => {
    await seed(page, { 'vault-mode': 'light' });
    await page.goto('/#/settings');
    await page.evaluate(() => { (window as unknown as { marker: number }).marker = 1; });
    await styleButton(page, 'Dracula').click();
    const html = page.locator('html');
    await expect(html).toHaveClass(/\btheme-dracula\b/);
    await expect(html).toHaveClass(/\bdark\b/);
    expect(await token(page, '--color-surface')).toBe(rgbOf('#282a36'));
    expect(await token(page, '--color-vault')).toBe(rgbOf('#bd93f9'));
    expect(await page.evaluate(() => (window as unknown as { marker?: number }).marker)).toBe(1);
    expect(await stored(page, 'vault-style')).toBe('dracula');
    expect(await stored(page, 'vault-mode')).toBe('light');

    await page.reload();
    await expect(html).toHaveClass(/\btheme-dracula\b/);
    await expect(styleButton(page, 'Dracula')).toHaveClass(/bg-vault/);
  });

  test('disables the colour mode with a note and restores the saved mode on the way out', async ({ page }) => {
    await seed(page, { 'vault-style': 'dracula', 'vault-mode': 'light' });
    await page.goto('/#/settings');
    const group = page.getByRole('group', { name: 'Color mode' });
    for (const name of ['Light', 'System', 'Dark']) await expect(modeButton(page, name)).toBeDisabled();
    // The highlight shows the mode that comes back.
    await expect(modeButton(page, 'Light')).toHaveClass(/bg-vault/);
    await expect(group).toHaveAccessibleDescription(/Dracula is always dark/);

    await page.keyboard.press('Control+Shift+L');
    expect(await stored(page, 'vault-mode')).toBe('light');
    await expect(page.locator('html')).toHaveClass(/\bdark\b/);

    await styleButton(page, 'Default').click();
    await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);
    await expect(page.locator('html')).not.toHaveClass(/\btheme-/);
    for (const name of ['Light', 'System', 'Dark']) await expect(modeButton(page, name)).toBeEnabled();
    await expect(page.getByText('Dracula is always dark')).toHaveCount(0);
    expect(await token(page, '--color-vault')).toBe(rgbOf('#f59e0b'));

    await page.keyboard.press('Control+Shift+L');
    expect(await stored(page, 'vault-mode')).toBe('system');
  });

  test('keeps the Default brand, fonts and corners', async ({ page }) => {
    await seed(page, { 'vault-style': 'dracula', 'vault-mode': 'dark' });
    await page.goto('/#/jobs');
    await expect(sidebarBrand(page).getByText('Backup Manager')).toBeVisible();
    await expect(sidebarBrand(page).getByText('┌─ VAULT ─┐')).toHaveCount(0);
    await expect(page.locator('body')).toHaveCSS('font-family', /-apple-system/);
    await expect(page.getByRole('button', { name: /new job/i })).not.toHaveCSS('border-radius', '0px');
  });
});

test.describe('Catppuccin style', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
    await page.emulateMedia({ reducedMotion: 'reduce' });
  });

  for (const [mode, surface, vault] of [['light', '#eff1f5', '#8839ef'], ['dark', '#1e1e2e', '#cba6f7']]) {
    test(`uses ${mode === 'light' ? 'Latte' : 'Mocha'} in ${mode} mode`, async ({ page }) => {
      await seed(page, { 'vault-style': 'catppuccin', 'vault-mode': mode });
      await page.goto('/#/settings');
      expect(await token(page, '--color-surface')).toBe(rgbOf(surface));
      expect(await token(page, '--color-vault')).toBe(rgbOf(vault));
      for (const name of ['Light', 'System', 'Dark']) await expect(modeButton(page, name)).toBeEnabled();
    });
  }

  test('follows the device in System mode, live', async ({ page }) => {
    await seed(page, { 'vault-style': 'catppuccin', 'vault-mode': 'system' });
    await page.emulateMedia({ colorScheme: 'light' });
    await page.goto('/#/settings');
    expect(await token(page, '--color-surface')).toBe(rgbOf('#eff1f5'));
    await page.emulateMedia({ colorScheme: 'dark' });
    await expect.poll(() => token(page, '--color-surface')).toBe(rgbOf('#1e1e2e'));
    await page.emulateMedia({ colorScheme: 'light' });
    await expect.poll(() => token(page, '--color-surface')).toBe(rgbOf('#eff1f5'));
  });

  test('switches modes from Settings without a reload', async ({ page }) => {
    await seed(page, { 'vault-mode': 'dark' });
    await page.goto('/#/settings');
    await styleButton(page, 'Catppuccin').click();
    await expect(page.locator('html')).toHaveClass(/\btheme-catppuccin\b/);
    expect(await token(page, '--color-surface')).toBe(rgbOf('#1e1e2e'));
    await modeButton(page, 'Light').click();
    await expect.poll(() => token(page, '--color-surface')).toBe(rgbOf('#eff1f5'));
    await page.reload();
    await expect(styleButton(page, 'Catppuccin')).toHaveClass(/bg-vault/);
    expect(await token(page, '--color-surface')).toBe(rgbOf('#eff1f5'));
    await expect(sidebarBrand(page).getByText('Backup Manager')).toBeVisible();
  });
});

// Colours that only the Default style paints. Tailwind's own palette computes to
// oklch(), so any shade a style block misses shows up as oklch too.
const DEFAULT_ONLY = [
  '#f59e0b', '#d97706', '#92400e', '#f0b429', '#111827', '#1f2937', '#374151', '#4b5563', '#6b7280', '#aeb4be',
  '#ffffff', '#f9fafb', '#f3f4f6', '#e5e7eb', '#d1d5db', '#5f6673', '#166534', '#991b1b', '#1d4ed8',
  '#4ade80', '#fca5a5', '#fbbf24', '#93c5fd',
].map(rgbOf);

/** Every painted colour on the page, including dialogs, chart marks, pseudo-elements and backdrops. */
function paintedColours(page: Page) {
  return page.evaluate(() => {
    const found = new Map<string, string>();
    const props = ['color', 'background-color', 'border-top-color', 'border-bottom-color', 'border-left-color', 'border-right-color', 'outline-color', 'fill', 'stroke', 'caret-color', 'accent-color'];
    const note = (el: Element, style: CSSStyleDeclaration, pseudo = '') => {
      for (const prop of props) {
        const value = style.getPropertyValue(prop);
        if (!value || value === 'none' || value === 'auto' || value.endsWith(' 0)') || value === 'transparent') continue;
        if (!found.has(value)) found.set(value, `${el.tagName.toLowerCase()}.${String(el.getAttribute('class') ?? '').slice(0, 80)}${pseudo} ${prop}`);
      }
    };
    for (const el of document.querySelectorAll('*')) {
      note(el, getComputedStyle(el));
      for (const pseudo of ['::before', '::after', '::placeholder']) {
        const style = getComputedStyle(el, pseudo);
        if (pseudo === '::placeholder' || style.content !== 'none') note(el, style, pseudo);
      }
      if (el.tagName === 'DIALOG') note(el, getComputedStyle(el, '::backdrop'), '::backdrop');
    }
    return [...found].map(([value, where]) => ({ value, where }));
  });
}

test.describe('No leftover Default colours', () => {
  const STYLES = [
    { style: 'dracula', mode: 'dark' },
    { style: 'catppuccin', mode: 'light' },
    { style: 'catppuccin', mode: 'dark' },
  ];
  const VISITS: { name: string; hash: string; then?: (page: Page) => Promise<void> }[] = [
    { name: 'Dashboard', hash: '#/' },
    { name: 'Jobs', hash: '#/jobs' },
    { name: 'New job wizard', hash: '#/jobs', then: async (p) => {
      await p.getByRole('button', { name: /new job/i }).click();
      await p.getByRole('button', { name: /Containers/i }).first().click();
    } },
    { name: 'Storage', hash: '#/storage' },
    { name: 'Add storage dialog', hash: '#/storage', then: async (p) => {
      await p.locator('main').getByRole('button', { name: /add storage/i }).first().click();
      await expect(p.getByRole('dialog', { name: 'Add Storage' })).toBeVisible();
    } },
    { name: 'Restore', hash: '#/restore' },
    { name: 'History with chart', hash: '#/history', then: async (p) => {
      await expect(p.locator('main svg').first()).toBeVisible();
    } },
    { name: 'Logs', hash: '#/logs', then: async (p) => {
      await expect(p.getByText('Failed to reach remote endpoint').first()).toBeVisible();
    } },
    { name: 'Anomalies', hash: '#/anomalies' },
    { name: 'Replication', hash: '#/replication' },
    { name: 'Settings', hash: '#/settings' },
    { name: 'Delete job dialog', hash: '#/jobs', then: async (p) => {
      await p.getByRole('button', { name: /delete job/i }).first().click();
      await expect(p.getByRole('dialog').first()).toBeVisible();
    } },
  ];

  for (const { style, mode } of STYLES) {
    for (const { name, hash, then } of VISITS) {
      test(`${style} ${mode}: ${name}`, async ({ page }) => {
        await setupVaultMockApi(page);
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await seed(page, { 'vault-style': style, 'vault-mode': mode });
        await page.goto(`/${hash}`);
        await expect(page.locator('main')).toBeVisible();
        await page.waitForLoadState('networkidle');
        if (then) await then(page);
        const leftovers = (await paintedColours(page)).filter(({ value }) =>
          DEFAULT_ONLY.includes(value.replace(/rgba\((\d+), (\d+), (\d+), 1\)/, 'rgb($1, $2, $3)')) || value.startsWith('oklch('));
        expect(leftovers).toEqual([]);
      });
    }
  }
});

test.describe('Appearance pickers announce the selection', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('style and mode buttons report pressed state as it changes', async ({ page }) => {
    await seed(page, { 'vault-style': 'default', 'vault-mode': 'light' });
    await page.goto('/#/settings');
    const styles = page.getByRole('group', { name: 'Theme style' });
    await expect(styles.getByRole('button', { name: 'Default', pressed: true })).toBeVisible();
    await expect(styles.getByRole('button', { pressed: true })).toHaveCount(1);
    await expect(modeButton(page, 'Light')).toHaveAttribute('aria-pressed', 'true');
    for (const name of ['System', 'Dark']) await expect(modeButton(page, name)).toHaveAttribute('aria-pressed', 'false');

    await styles.getByRole('button', { name: 'Catppuccin' }).click();
    await expect(styles.getByRole('button', { name: 'Catppuccin' })).toHaveAttribute('aria-pressed', 'true');
    await expect(styles.getByRole('button', { name: 'Default' })).toHaveAttribute('aria-pressed', 'false');
    await modeButton(page, 'Dark').click();
    await expect(modeButton(page, 'Dark')).toHaveAttribute('aria-pressed', 'true');
    await expect(modeButton(page, 'Light')).toHaveAttribute('aria-pressed', 'false');
  });

  test('under Dracula the mode buttons read as disabled, keep the saved mode pressed, and carry the note', async ({ page }) => {
    await seed(page, { 'vault-style': 'dracula', 'vault-mode': 'system' });
    await page.goto('/#/settings');
    const group = page.getByRole('group', { name: 'Color mode' });
    await expect(group).toHaveAccessibleDescription('Dracula is always dark. Your saved mode comes back when you choose another style.');
    for (const name of ['Light', 'System', 'Dark']) await expect(group.getByRole('button', { name, disabled: true })).toBeVisible();
    await expect(group.getByRole('button', { name: 'System', pressed: true })).toBeDisabled();
    const snapshot = await group.ariaSnapshot();
    expect(snapshot).toContain('button "System" [disabled] [pressed]');
  });
});
