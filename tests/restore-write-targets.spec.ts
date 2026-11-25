import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

// Exact host paths for container and VM restores, from the write-targets
// request sent with the pre-flight, in step 3 and in the typed RESTORE
// confirmation (#716).

async function reachStep3(page: Page) {
  await page.goto('/#/restore');
  await page.getByRole('button', { name: /plex/i }).click();
  await page.getByRole('button', { name: /home-assistant/i }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
  await page.getByRole('button', { name: /VM Backups/i }).first().click();
  await page.getByRole('button', { name: /Continue/ }).click();
}

test.describe('Exact container and VM restore paths (#716)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('step 3 and the RESTORE confirmation list the server-resolved paths', async ({ page }) => {
    await reachStep3(page);
    const targets = page.getByTestId('restore-targets');
    await expect(targets.first()).toContainText('Run the pre-flight checks to see the exact host paths.');

    await page.getByRole('button', { name: 'Run all pre-flight checks to enable Start Restore' }).click();
    await expect(page.getByRole('button', { name: /^Start Restore/ })).toBeEnabled();

    const plex = targets.first().getByTestId('restore-write-targets');
    await expect(plex).toContainText('Volume /config');
    await expect(plex).toContainText('/mnt/user/appdata/plex');
    await expect(plex).toContainText('Files here that are not in the backup will be deleted');
    await expect(plex).toContainText('/boot/config/plugins/dockerMan/templates-user/my-plex.xml');
    const vm = targets.nth(1).getByTestId('restore-write-targets');
    await expect(vm).toContainText('Disk vda');
    await expect(vm).toContainText('/mnt/user/domains/home-assistant/vdisk1.img');
    await expect(vm).toContainText('/etc/libvirt/qemu/nvram/home-assistant_VARS-pure-efi.fd');
    await expect(vm).toContainText('Domain definition');

    await page.getByRole('button', { name: /^Start Restore/ }).click();
    const dialog = page.getByRole('dialog', { name: 'Restore and Delete Extra Files' });
    await expect(dialog).toContainText('/mnt/user/appdata/plex');
    await expect(dialog).toContainText('/boot/config/plugins/dockerMan/templates-user/my-plex.xml');
    await expect(dialog).toContainText('/mnt/user/domains/home-assistant/vdisk1.img');
    await expect(dialog).toContainText('/etc/libvirt/qemu/nvram/home-assistant_VARS-pure-efi.fd');
    await expect(dialog).toContainText('home-assistant (VM), VM Backups');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
  });

  test('a custom destination shows the remapped paths once checked again', async ({ page }) => {
    const requests: Record<string, unknown>[] = [];
    page.on('request', req => {
      if (req.method() === 'POST' && req.url().endsWith('/write-targets')) requests.push(req.postDataJSON());
    });
    await reachStep3(page);
    await page.getByRole('radio', { name: 'Custom destination' }).first().check();
    await page.getByLabel('Custom restore destination').first().fill('/mnt/user/restore-copy');
    await page.getByLabel(/I understand the live container/).check();
    await page.getByRole('button', { name: 'Run all pre-flight checks to enable Start Restore' }).click();
    const plex = page.getByTestId('restore-targets').first().getByTestId('restore-write-targets');
    await expect(plex).toContainText('/mnt/user/restore-copy/plex');
    expect(requests).toContainEqual(expect.objectContaining({ items: ['plex'], destination: '/mnt/user/restore-copy' }));
  });
});

test.describe('Write targets never hold up the pre-flight (#716)', () => {
  test.beforeEach(async ({ page }) => {
    await setupVaultMockApi(page);
  });

  test('a failing write-targets request still lets Start Restore proceed', async ({ page }) => {
    await page.route('**/restore-points/*/write-targets', route =>
      route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'boom' }) }));
    await reachStep3(page);
    const writeTargets = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/write-targets'));
    const preflight = page.waitForResponse(res => res.url().endsWith('/preflight'));
    await page.getByRole('button', { name: 'Run all pre-flight checks to enable Start Restore' }).click();
    expect((await writeTargets).postDataJSON()).toEqual(expect.objectContaining({ items: ['plex'] }));
    expect((await preflight).ok()).toBe(true);
    await expect(page.getByRole('button', { name: /^Start Restore/ })).toBeEnabled();
    const targets = page.getByTestId('restore-targets');
    await expect(targets.first()).toContainText('Exact paths could not be checked before the restore.');
    await expect(targets.nth(1)).toContainText('Exact paths could not be checked before the restore.');

    await page.getByRole('button', { name: /^Start Restore/ }).click();
    const dialog = page.getByRole('dialog', { name: 'Restore and Delete Extra Files' });
    await expect(dialog).toContainText('plex (Container)');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
  });

  test('a slow write-targets request does not delay the pre-flight', async ({ page }) => {
    let release: () => void = () => {};
    const held = new Promise<void>(resolve => { release = resolve; });
    await page.route('**/restore-points/*/write-targets', async route => {
      await held;
      await route.fallback();
    });
    await reachStep3(page);
    await page.getByRole('button', { name: 'Run all pre-flight checks to enable Start Restore' }).click();
    await expect(page.getByRole('button', { name: /^Start Restore/ })).toBeEnabled();
    const plex = page.getByTestId('restore-targets').first();
    await expect(plex).toContainText('Checking the exact host paths');
    await expect(plex.getByTestId('restore-write-targets')).toHaveCount(0);

    release();
    await expect(plex.getByTestId('restore-write-targets')).toContainText('/mnt/user/appdata/plex');
  });
});
