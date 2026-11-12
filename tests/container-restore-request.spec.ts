import { test, expect } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

test('unlocks saved Docker effects in Unraid before approval', async ({ page }) => {
  await setupVaultMockApi(page, { restoreRequestFixtures: true });
  await page.addInitScript(() => {
    Object.assign(window, { __VAULT_RUNTIME_CONFIG__: {
      mode: 'unraid-proxy',
      csrfToken: 'native-csrf-fixture',
      restoreRequestActionPath: '/plugins/vault/include/restore-request.php',
    } });
  });
  const request = {
    id: 'docker-review', status: 'pending', requester_name: 'Recovery assistant',
    job_name: 'Docker backups', item: { item_name: 'web', item_type: 'container' },
    restore_point: { id: 11, created_at: '2026-11-10T02:00:00Z' },
    target_choice: 'alternate', expires_at: '2026-11-14T02:00:00Z', membership: 'confirmed',
    verification: { status: 'none' }, chain: { status: 'confirmed', steps: [] },
    effective_write_mode: 'replace', warnings: [], requires_passphrase: true,
    eligible_for_approval: false, requires_metadata_review: true, review_digest: 'locked',
    ineligibility_reasons: ['backup_metadata_locked'], effects: [],
  };
  await page.route('**/api/v1/restore-requests/docker-review', route => route.fulfill({ json: request }));
  await page.route('**/plugins/vault/include/restore-request.php', route => {
    expect(Object.fromEntries(new URLSearchParams(route.request().postData() || ''))).toEqual({
      action: 'review', id: 'docker-review', review_digest: 'locked',
      passphrase: 'ephemeral-review-fixture', csrf_token: 'native-csrf-fixture',
    });
    return route.fulfill({ json: {
      ...request, eligible_for_approval: true, requires_metadata_review: false, review_digest: 'unlocked',
      ineligibility_reasons: [], effects: ['stop_existing_container', 'remove_existing_container', 'recreate_container', 'start_container'],
      resolved_path: '/mnt/user/restore/web', container: {
        name: 'web', image: 'fixture/web:backup', start: true,
        volumes: [{ mount: '/config', target: '/mnt/user/restore/web/config', write_mode: 'replace' }],
      },
    } });
  });

  await page.goto('/#/restore?request=docker-review');
  const review = page.getByRole('region', { name: 'Restore Request review' });
  await expect(review.getByRole('button', { name: 'Approve and run' })).toHaveCount(0);
  await review.getByLabel('Backup Passphrase').fill('ephemeral-review-fixture');
  await review.getByRole('button', { name: 'Review saved configuration' }).click();
  await expect(review.getByText('fixture/web:backup')).toBeVisible();
  await expect(review.getByText('/mnt/user/restore/web/config')).toBeVisible();
  await expect(review.getByText('Stop the existing container if it is running.')).toBeVisible();
  await review.getByRole('button', { name: 'Approve and run' }).click();
  await expect(page.getByRole('dialog', { name: 'Approve Restore Request' }).getByLabel('Backup Passphrase')).toHaveValue('');
});
