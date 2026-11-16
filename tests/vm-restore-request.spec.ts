import { expect, test } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';

for (const scenario of ['ready', 'missing chain', 'changed disk'] as const) {
  test(`VM Restore Request with ${scenario}`, async ({ page }) => {
    await setupVaultMockApi(page, { restoreRequestFixtures: true });
    await page.addInitScript(() => {
      Object.assign(window, { __VAULT_RUNTIME_CONFIG__: {
        mode: 'unraid-proxy',
        csrfToken: 'vm-csrf-fixture',
        restoreRequestActionPath: '/plugins/vault/include/restore-request.php',
      } });
    });
    const eligible = scenario !== 'missing chain';
    const request = {
      id: 'vm-review',
      status: 'pending',
      requester_name: 'Recovery assistant',
      job_name: 'VM backups',
      item: { item_name: 'test-vm', item_type: 'vm' },
      restore_point: { id: 11, created_at: '2026-11-10T02:00:00Z' },
      target_choice: 'original',
      expires_at: '2026-11-14T02:00:00Z',
      membership: 'confirmed',
      verification: { status: 'none' },
      chain: {
        status: eligible ? 'confirmed' : 'missing',
        steps: [
          { restore_point_id: 11, backup_type: 'incremental', membership: 'confirmed', data_required: true, artifacts: 'confirmed', vm_targets: ['vda'] },
        ],
      },
      effective_write_mode: 'replace',
      warnings: ['unverified_backup'],
      eligible_for_approval: eligible,
      review_digest: 'fixture-vm-review',
      ineligibility_reasons: eligible ? [] : ['missing_parent'],
      effects: eligible ? ['vm_stop_domain', 'vm_replace_domain', 'vm_restore_disks', 'vm_define_saved_domain', 'vm_start_domain'] : [],
      vm: eligible ? {
        name: 'test-vm',
        uuid: '22222222-3333-4444-aaaa-555555555555',
        disks: [{ target: 'vda', format: 'qcow2', destination: '/mnt/user/domains/test-vm/disk.img', replace: true }],
        nvram: { destination: '/mnt/user/domains/test-vm/vars.fd', replace: true },
        domain: { state: 'running', replace: true, stop: true },
        start_after_restore: true,
        readiness_mode: 'guest_agent',
        readiness_timeout_seconds: 30,
      } : undefined,
    };
    const run = {
      status: 'completed',
      items_done: 1,
      items_total: 1,
      application_health: 'unchecked',
      vm_readiness: { startup: 'passed', running: 'passed', guest_check: 'guest_agent', guest_outcome: 'passed', cleanup_stop: 'not_attempted' },
    };
    let approved = false;
    let invalidated = false;
    await page.route('**/api/v1/restore-requests/vm-review', route => route.fulfill({ json: {
      ...request,
      ...(approved && { status: 'completed', run_id: 54, run }),
      ...(invalidated && { status: 'invalidated', terminal_reason: 'reviewed_operation_changed' }),
    } }));
    await page.route('**/plugins/vault/include/restore-request.php', route => {
      expect(Object.fromEntries(new URLSearchParams(route.request().postData() || ''))).toMatchObject({
        action: 'approve', id: 'vm-review', review_digest: 'fixture-vm-review', csrf_token: 'vm-csrf-fixture',
      });
      if (scenario === 'changed disk') {
        invalidated = true;
        return route.fulfill({ status: 410, json: { error: 'The reviewed operation changed' } });
      }
      approved = true;
      return route.fulfill({ status: 202, json: { ...request, status: 'completed', run_id: 54, run } });
    });

    await page.goto('/#/restore?request=vm-review');
    const review = page.getByRole('region', { name: 'Restore Request review' });
    if (!eligible) {
      await expect(review.getByText('A required parent Restore Point is missing.')).toBeVisible();
      await expect(review.getByRole('button', { name: 'Approve and run' })).toHaveCount(0);
      return;
    }

    const disks = review.getByRole('table', { name: 'VM disk and NVRAM targets' });
    await expect(disks.getByText('/mnt/user/domains/test-vm/disk.img')).toBeVisible();
    await expect(disks.getByText('/mnt/user/domains/test-vm/vars.fd')).toBeVisible();
    await expect(review.getByText(/may force-stop/)).toBeVisible();
    await review.getByRole('button', { name: 'Approve and run' }).click();
    const dialog = page.getByRole('dialog', { name: 'Approve Restore Request' });
    await dialog.getByLabel(/Type RESTORE to confirm/).fill('RESTORE');
    await dialog.getByRole('button', { name: 'Approve and run' }).click();

    if (scenario === 'changed disk') {
      await expect(review.getByText(/Ask the assistant to prepare a new request/)).toBeVisible();
      await expect(review.getByRole('button', { name: 'Approve and run' })).toHaveCount(0);
    } else {
      await expect(review.getByRole('heading', { name: 'Restore Run #54' })).toBeVisible();
      const readiness = review.getByRole('group', { name: 'VM readiness' });
      await expect(readiness.getByText('QEMU guest agent')).toBeVisible();
      await expect(readiness.getByText('Unchecked')).toBeVisible();
    }
  });
}
