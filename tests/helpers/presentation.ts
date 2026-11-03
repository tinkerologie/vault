import path from 'node:path';
import { expect, type Frame, type Page } from '@playwright/test';

/**
 * Opens Vault in either presentation and returns the frame that holds the app.
 *
 * Standalone is the top-level page. Embedded is the real embed-host.js driving a
 * same-origin iframe, the way Unraid's Backups page loads it (#526); only the
 * runtime config app.php would inject is faked. embedded.spec.ts covers the host
 * page itself; this harness stays minimal so audits see only Vault's markup.
 */

export type Presentation = 'standalone' | 'embedded';

export const PRESENTATIONS: Presentation[] = ['standalone', 'embedded'];

export const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 800 },
  { name: '375 px', width: 375, height: 812 },
] as const;

/** Every dispatched route, with its h1 (web/src/lib/routes.js) and a sign its data has loaded. */
export const ROUTES = [
  { name: 'Dashboard', hash: '#/', heading: 'Dashboard', ready: '.dash-tile' },
  { name: 'Jobs', hash: '#/jobs', heading: 'Backup Jobs', ready: 'text=Docker & Appdata' },
  { name: 'Storage', hash: '#/storage', heading: 'Storage Destinations', ready: 'text=Local Array Backup' },
  { name: 'History', hash: '#/history', heading: 'Backup & Restore History', ready: 'main span:has-text("Docker & Appdata")' },
  { name: 'Restore', hash: '#/restore', heading: 'Restore', ready: 'text=plex' },
  { name: 'Logs', hash: '#/logs', heading: 'Logs', ready: 'text=Storage capacity above 75%' },
  { name: 'Replication', hash: '#/replication', heading: 'Replication', ready: 'h1' },
  { name: 'Recovery', hash: '#/recovery', heading: 'Recovery Guide', ready: 'h1' },
  { name: 'Recover wizard', hash: '#/recover', heading: 'Recover Vault', ready: 'h1' },
  { name: 'Anomalies', hash: '#/anomalies', heading: 'Anomalies', ready: 'text=/Backup size spike/i' },
  { name: 'Settings', hash: '#/settings', heading: 'Settings', ready: '[role="tabpanel"]' },
] as const;

export const FRAME_ID = 'vault-backups-frame';
const EMBED_HOST = path.join(process.cwd(), 'plugin/pages/include/embed-host.js');
const HARNESS = '/__presentation_host__.html';

const harness = (hash: string) => `<!doctype html><html lang="en"><head><title>Unraid</title></head><body>
<iframe id="${FRAME_ID}" src="/?embed=1${hash}" style="width:100%;border:0;display:block" title="Vault Backups"></iframe>
<script src="/plugins/vault/include/embed-host.js"></script>
<script>VaultEmbedHost.attach(document.getElementById('${FRAME_ID}'));</script>
</body></html>`;

/** Navigates to `hash` and resolves once the page's h1 is visible. Mock the API first. */
export async function openPresentation(page: Page, presentation: Presentation, hash: string): Promise<Frame> {
  if (presentation === 'standalone') {
    await page.goto(`/${hash}`);
    await expect(page.locator('h1').first()).toBeVisible();
    return page.mainFrame();
  }
  await page.addInitScript(() => {
    if (window.self !== window.top) {
      (window as unknown as { __VAULT_RUNTIME_CONFIG__: unknown }).__VAULT_RUNTIME_CONFIG__ = { presentation: 'embedded', hostTheme: null };
    }
  });
  await page.route(`**${HARNESS}`, (route) => route.fulfill({ contentType: 'text/html', body: harness(hash) }));
  await page.route('**/plugins/vault/include/embed-host.js', (route) => route.fulfill({ contentType: 'text/javascript', path: EMBED_HOST }));
  await page.goto(HARNESS);
  await expect(page.frameLocator(`#${FRAME_ID}`).locator('h1').first()).toBeVisible();
  const frame = page.frames().find((f) => f !== page.mainFrame())!;
  await expect.poll(() => frame.evaluate(() => document.documentElement.classList.contains('vault-embedded'))).toBe(true);
  return frame;
}
