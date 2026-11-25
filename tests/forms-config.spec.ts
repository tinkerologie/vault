import { test, expect, type Page } from '@playwright/test';
import { setupVaultMockApi, createInitialMockJobs, createInitialMockStorage } from './mocks/vault-api';

// Configuration and form behaviour from the UI/UX audit (#707, group G3):
// schedules, selection state, unsaved changes, field errors, typed deletes,
// secret fields, the full job form, Settings deep links and layout.

const REDACTED = '••••••••';

async function openNewJob(page: Page) {
  await page.goto('/#/jobs');
  await page.getByRole('button', { name: /new job/i }).click();
  return page.getByRole('dialog', { name: 'Create Backup Job' });
}

async function pickPlex(page: Page) {
  const dialog = page.getByRole('dialog', { name: 'Create Backup Job' });
  await dialog.getByRole('button', { name: /Containers/i }).first().click();
  await dialog.getByRole('button', { name: /plex/i }).first().click();
}

test.describe('job schedule (#682)', () => {
  test('a manual-only job stays manual when it is edited and saved', async ({ page }) => {
    const jobs = createInitialMockJobs();
    jobs[0].schedule = '';
    await setupVaultMockApi(page, { jobs });
    const put = page.waitForRequest((r) => r.method() === 'PUT' && /\/api\/v1\/jobs\/1$/.test(r.url()));
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'Edit job: Docker & Appdata' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit Job' });
    await expect(dialog.getByRole('button', { name: 'Manual only', pressed: true })).toBeVisible();
    await dialog.locator('#desc').fill('Changed only the description');
    await dialog.getByRole('button', { name: 'Save Changes' }).click();
    expect((await put).postDataJSON().schedule).toBe('');
  });

  test('an off-grid minute is shown and survives a weekday change', async ({ page }) => {
    const jobs = createInitialMockJobs();
    jobs[0].schedule = '15 2 * * 1,2,3,4,5';
    await setupVaultMockApi(page, { jobs });
    const put = page.waitForRequest((r) => r.method() === 'PUT' && /\/api\/v1\/jobs\/1$/.test(r.url()));
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'Edit job: Docker & Appdata' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit Job' });
    const where = dialog.locator('#storage').locator('xpath=ancestor::div[contains(@class,"space-y-5")][1]');
    await expect(where.getByLabel('Time', { exact: true })).toHaveValue('2:15');
    await where.getByRole('button', { name: 'Saturday' }).click();
    await expect(where.getByRole('button', { name: 'Saturday' })).toHaveAttribute('aria-pressed', 'true');
    await expect(where.getByTestId('schedule-next-runs')).toContainText('server time, CEST');
    await dialog.getByRole('button', { name: 'Save Changes' }).click();
    expect((await put).postDataJSON().schedule).toBe('15 2 * * 1,2,3,4,5,6');
  });
});

test.describe('selection state and the stepper (#683)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('type cards, items and days report their state after a click', async ({ page }) => {
    const dialog = await openNewJob(page);
    const containers = dialog.getByRole('button', { name: /Containers/i }).first();
    await expect(containers).toHaveAttribute('aria-pressed', 'false');
    await containers.click();
    await expect(containers).toHaveAttribute('aria-pressed', 'true');
    const plex = dialog.getByRole('button', { name: /plex/i }).first();
    await plex.click();
    await expect(plex).toHaveAttribute('aria-pressed', 'true');
    await dialog.getByRole('button', { name: 'Next', exact: true }).click();
    const sunday = dialog.getByRole('button', { name: 'Sunday' });
    await expect(sunday).toHaveAttribute('aria-pressed', 'true');
    await sunday.click();
    await expect(sunday).toHaveAttribute('aria-pressed', 'false');
  });

  test('backup modes are a named radio group', async ({ page }) => {
    const dialog = await openNewJob(page);
    await pickPlex(page);
    await dialog.getByRole('button', { name: 'Step 3: How' }).click();
    const group = dialog.getByRole('radiogroup', { name: 'Container Backup Mode' });
    await expect(group.getByRole('radio')).toHaveCount(2);
    const batch = group.getByRole('radio').nth(1);
    await batch.check();
    await expect(batch).toBeChecked();
  });

  test('at 375 px every step has a name with its state', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    const dialog = await openNewJob(page);
    await expect(dialog.getByRole('button', { name: 'Step 1: What' })).toHaveAttribute('aria-current', 'step');
    await pickPlex(page);
    await dialog.getByRole('button', { name: 'Next', exact: true }).click();
    await expect(dialog.getByRole('button', { name: 'Step 1: What, completed' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Step 2: Where & when' })).toHaveAttribute('aria-current', 'step');
  });

  test('a blocked step explains itself, and hints wait for an attempt (#688)', async ({ page }) => {
    const dialog = await openNewJob(page);
    const next = dialog.getByRole('button', { name: 'Next', exact: true });
    const step3 = dialog.getByRole('button', { name: 'Step 3: How' });
    await expect(next).not.toHaveAccessibleDescription(/Select at least one backup type/);
    await step3.click();
    await expect(step3).toHaveAccessibleDescription(/Step 3 isn.t available yet\. Select at least one backup type/);
    // aria-disabled, not disabled: the press still reaches the form, which
    // explains what is missing (Playwright won't click an aria-disabled button).
    await next.focus();
    await page.keyboard.press('Enter');
    await expect(next).toHaveAccessibleDescription(/Select at least one backup type/);
  });
});

test.describe('unsaved changes (#687)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('an untouched wizard closes; a changed one asks first', async ({ page }) => {
    let dialog = await openNewJob(page);
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);

    dialog = await openNewJob(page);
    await pickPlex(page);
    await dialog.getByRole('button', { name: 'Close' }).click();
    const discard = page.getByRole('dialog', { name: 'Discard changes?' });
    await discard.getByRole('button', { name: 'Keep editing' }).click();
    await expect(dialog.getByRole('button', { name: /plex/i }).first()).toHaveAttribute('aria-pressed', 'true');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await page.getByRole('dialog', { name: 'Discard changes?' }).getByRole('button', { name: 'Discard' }).click();
    await expect(dialog).toHaveCount(0);
  });

  test('Back closes the dialog instead of leaving the page, and asks when changed', async ({ page }) => {
    const dialog = await openNewJob(page);
    await page.goBack();
    await expect(dialog).toHaveCount(0);
    await expect(page).toHaveURL(/#\/jobs$/);

    await page.getByRole('button', { name: /new job/i }).click();
    await pickPlex(page);
    await page.goBack();
    await expect(page.getByRole('dialog', { name: 'Discard changes?' })).toBeVisible();
    await page.getByRole('button', { name: 'Keep editing' }).click();
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(/#\/jobs$/);
  });

  test('storage added from the wizard keeps the draft', async ({ page }) => {
    await setupVaultMockApi(page, { storage: [] });
    const dialog = await openNewJob(page);
    await pickPlex(page);
    await dialog.getByRole('button', { name: 'Next', exact: true }).click();
    await dialog.getByRole('button', { name: 'Add storage' }).click();
    const add = page.getByRole('dialog', { name: 'Add Storage' });
    await add.locator('#sname').fill('From the wizard');
    await add.getByRole('textbox', { name: 'Path' }).fill('/mnt/user/backups/vault');
    await add.getByRole('button', { name: 'Add Storage', exact: true }).click();
    await expect(add).toHaveCount(0);
    await expect(dialog.locator('#storage')).toHaveValue(/\d+/);
    await expect(dialog.locator('#storage option:checked')).toHaveText(/From the wizard/);
    await dialog.getByRole('button', { name: 'Step 1: What, completed' }).click();
    await expect(dialog.getByRole('button', { name: /plex/i }).first()).toHaveAttribute('aria-pressed', 'true');
  });

  test('a changed storage form asks before closing', async ({ page }) => {
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await page.keyboard.press('Escape');
    await expect(editor).toHaveCount(0);
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    await editor.locator('#sname').fill('Half typed');
    await page.goBack();
    await expect(page.getByRole('dialog', { name: 'Discard changes?' })).toBeVisible();
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(editor).toHaveCount(0);
    await expect(page).toHaveURL(/#\/storage$/);
  });

  test('a Settings section with a pending switch is marked and guards leaving', async ({ page }) => {
    await page.goto('/#/settings');
    const section = page.locator('#set-throttle');
    await expect(section.getByText('Unsaved changes')).toHaveCount(0);
    await section.getByRole('switch', { name: 'Toggle adaptive upload throttling' }).click();
    await expect(section.getByText('Unsaved changes')).toBeVisible();
    await page.getByRole('navigation').getByRole('link', { name: 'Jobs' }).first().click();
    const leave = page.getByRole('dialog', { name: 'Leave without saving?' });
    await expect(leave).toContainText('Upload Throttling');
    await leave.getByRole('button', { name: 'Stay' }).click();
    await expect(page).toHaveURL(/#\/settings/);
    await section.getByRole('switch', { name: 'Toggle adaptive upload throttling' }).click();
    await expect(section.getByText('Unsaved changes')).toHaveCount(0);
  });
});

test.describe('field errors (#688)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('a duplicate job name shows under Job Name and takes focus', async ({ page }) => {
    await page.route('**/api/v1/jobs', (route) => route.request().method() === 'POST'
      ? route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'a job with this name already exists' }) })
      : route.fallback());
    const dialog = await openNewJob(page);
    await pickPlex(page);
    const next = dialog.getByRole('button', { name: 'Next', exact: true });
    await next.click();
    await next.click();
    await next.click();
    await expect(dialog.locator('#name')).toHaveValue('Containers Backup');
    await dialog.getByRole('button', { name: 'Create Job', exact: true }).click();
    const name = dialog.locator('#name');
    await expect(name).toBeFocused();
    await expect(name).toHaveAttribute('aria-invalid', 'true');
    await expect(name).toHaveAccessibleDescription(/already exists/);
  });

  test('a failed connection test keeps its reason in the form', async ({ page }) => {
    await page.route('**/api/v1/storage/test', (route) => route.fulfill({
      status: 200, contentType: 'application/json', body: JSON.stringify({ success: false, error: 'connection refused' }),
    }));
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.getByRole('button', { name: 'Test connection' }).click();
    await expect(editor.getByTestId('storage-test-result')).toContainText('connection refused');
  });

  test('the item search and exclusion fields have labels', async ({ page }) => {
    const dialog = await openNewJob(page);
    await dialog.getByRole('button', { name: /Containers/i }).first().click();
    await expect(dialog.getByRole('searchbox', { name: 'Search containers' })).toBeVisible();
    await dialog.getByRole('button', { name: /plex/i }).first().click();
    await dialog.getByRole('button', { name: 'Switch to full form' }).click();
    await dialog.locator('summary', { hasText: 'Container Mounts' }).click();
    await expect(dialog.getByRole('textbox', { name: 'Additional path exclusions for plex' })).toBeVisible();
  });

  test('a job can be renamed from the keyboard', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'Rename Docker & Appdata' }).focus();
    await page.keyboard.press('Enter');
    const input = page.getByRole('textbox', { name: 'Rename Docker & Appdata' });
    await expect(input).toBeFocused();
  });
});

test.describe('typed deletes (#689)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('deleting a job with its files needs DELETE; without files stays one click', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'Delete job: Docker & Appdata' }).click();
    const dialog = page.getByRole('dialog', { name: 'Delete Job' });
    await expect(dialog.getByRole('group', { name: 'Backup files' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Delete Job', exact: true })).toBeEnabled();
    await dialog.getByRole('radio', { name: /Delete backup files/ }).check();
    const confirm = dialog.getByRole('button', { name: 'Delete Job & Files' });
    await expect(confirm).toBeDisabled();
    await dialog.getByRole('textbox', { name: /Type DELETE to confirm/ }).fill('delete');
    await expect(confirm).toBeDisabled();
    const del = page.waitForRequest((r) => r.method() === 'DELETE' && /\/jobs\/1/.test(r.url()));
    await dialog.getByRole('textbox', { name: /Type DELETE to confirm/ }).fill('DELETE');
    await confirm.click();
    expect((await del).url()).toMatch(/delete_files=true|deleteFiles=true|files=true/);
  });
});

test.describe('secret fields (#706)', () => {
  test('the toggle reports its state and the field is new-password', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.getByRole('button', { name: 'SFTP' }).click();
    const field = editor.locator('#cfg_pass');
    await expect(field).toHaveAttribute('autocomplete', 'new-password');
    await expect(field).toHaveAttribute('type', 'password');
    const toggle = editor.getByRole('button', { name: 'Show password' });
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'true');
    await expect(field).toHaveAttribute('type', 'text');
  });

  test('editing without touching a saved secret keeps it; typing replaces it', async ({ page }) => {
    const storage = createInitialMockStorage();
    const nas = storage.find((s) => s.type === 'sftp')!;
    nas.config = JSON.stringify({ host: 'nas.example.com', port: 22, user: 'vault', password: REDACTED, base_path: '/backups' });
    await setupVaultMockApi(page, { storage });
    await page.goto('/#/storage');
    await page.getByRole('button', { name: `Edit storage: ${nas.name}` }).click();
    const editor = page.getByRole('dialog', { name: 'Edit Storage' });
    const field = editor.locator('#cfg_pass');
    await expect(field).toHaveValue('');
    await expect(field).toHaveAccessibleDescription(/Leave blank to keep it/);

    let put = page.waitForRequest((r) => r.method() === 'PUT' && /\/storage\/\d+$/.test(r.url()));
    await editor.locator('#cfg_user').fill('vault2');
    await editor.getByRole('button', { name: 'Save Changes' }).click();
    expect(JSON.parse((await put).postDataJSON().config).password).toBe(REDACTED);

    await page.getByRole('button', { name: `Edit storage: ${nas.name}` }).click();
    put = page.waitForRequest((r) => r.method() === 'PUT' && /\/storage\/\d+$/.test(r.url()));
    await editor.locator('#cfg_pass').fill('new-secret');
    await editor.getByRole('button', { name: 'Save Changes' }).click();
    expect(JSON.parse((await put).postDataJSON().config).password).toBe('new-secret');
  });
});

test.describe('full job form (#712)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('every option is in the full form, and configured sections say so', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'Edit job: Docker & Appdata' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit Job' });
    await expect(dialog.getByText('Full form')).toBeVisible();
    for (const section of ['What', 'Where & when', 'How', 'Advanced', 'Name & review']) {
      await expect(dialog.getByRole('navigation', { name: 'Jump to section' }).getByRole('button', { name: section, exact: true })).toBeVisible();
    }
    for (const summary of ['Retention Policy', 'Long-Term Retention (LTR)', 'Scheduled verification', 'Scripts', 'Notifications', 'Backup Verification', 'Override retry policy', 'Upload concurrency', 'Adaptive Backup', 'Container Mounts']) {
      await expect(dialog.locator('summary', { hasText: summary })).toHaveCount(1);
    }
    await expect(dialog.locator('summary', { hasText: 'Long-Term Retention (LTR)' })).toContainText('Configured');
    await expect(dialog.locator('summary', { hasText: 'Scripts' })).not.toContainText('Configured');
    await expect(dialog.locator('#storage')).toBeVisible();
    await expect(dialog.locator('#name')).toHaveValue('Docker & Appdata');
  });

  test('the chosen layout is remembered', async ({ page }) => {
    let dialog = await openNewJob(page);
    await expect(dialog.getByText('Step 1 of 4')).toBeVisible();
    await dialog.getByRole('button', { name: 'Switch to full form' }).click();
    await page.keyboard.press('Escape');
    dialog = await openNewJob(page);
    await expect(dialog.getByText('Full form')).toBeVisible();
  });
});

test.describe('Settings deep links (#712)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('#/settings/security opens Security and Back/Forward move between tabs', async ({ page }) => {
    await page.goto('/#/settings/security');
    await expect(page.getByRole('tab', { name: 'Security' })).toHaveAttribute('aria-selected', 'true');
    await page.getByRole('tab', { name: 'Notifications' }).click();
    await expect(page).toHaveURL(/#\/settings\/notifications$/);
    await page.goBack();
    await expect(page.getByRole('tab', { name: 'Security' })).toHaveAttribute('aria-selected', 'true');
    await page.goForward();
    await expect(page.getByRole('tab', { name: 'Notifications' })).toHaveAttribute('aria-selected', 'true');
    await page.goto('/#/settings/nope');
    await expect(page.getByRole('tab', { name: 'General' })).toHaveAttribute('aria-selected', 'true');
  });
});

test.describe('dedup overhead (#713)', () => {
  test('a ratio below 1 is overhead, never savings', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.route(/\/api\/v1\/storage\/\d+\/dedup-stats$/, (route) => route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ logical_bytes: 473220000, physical_bytes: 4617000000, dedup_ratio: 0.1, wasted_bytes_estimate: 0, total_chunks: 10, total_packs: 2 }),
    }));
    await page.goto('/#/storage');
    const overhead = page.getByTestId('dedup-overhead').first();
    await expect(overhead).toContainText(/Using 4\.\d GB to store 451\.\d MB so far/);
    await expect(page.getByText(/0\.1×/)).toHaveCount(0);
    await expect(page.getByText(/Saved 0 B/)).toHaveCount(0);
  });
});

test.describe('layout (#697, #699, #715)', () => {
  test.beforeEach(async ({ page }) => setupVaultMockApi(page));

  test('credential fields stack and inputs are 44 px tall at 375 px', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const editor = page.getByRole('dialog', { name: 'Add Storage' });
    await editor.getByRole('button', { name: 'SFTP' }).click();
    const user = await editor.locator('#cfg_user').boundingBox();
    const pass = await editor.locator('#cfg_pass').boundingBox();
    expect(pass!.y).toBeGreaterThan(user!.y + user!.height - 1);
    // offsetHeight ignores the dialog's entrance scale.
    for (const id of ['#cfg_user', '#cfg_pass', '#sname', '#cfg_host']) {
      expect(await editor.locator(id).evaluate((el) => (el as HTMLElement).offsetHeight), id).toBeGreaterThanOrEqual(44);
    }
  });

  test('the S3 type button is not truncated', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto('/#/storage');
    await page.locator('main').getByRole('button', { name: /add storage/i }).first().click();
    const label = page.getByRole('dialog').getByRole('button', { name: 'S3 / S3-Compatible' }).locator('span.truncate');
    const clipped = await label.evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(clipped).toBe(false);
  });

  test('Jobs rows set Delete apart and use the Run Now treatment', async ({ page }) => {
    await page.goto('/#/jobs');
    await expect(page.getByRole('button', { name: 'Run Now: Docker & Appdata' })).toContainText('Run Now');
    const edit = await page.getByRole('button', { name: 'Edit job: Docker & Appdata' }).boundingBox();
    const del = await page.getByRole('button', { name: 'Delete job: Docker & Appdata' }).boundingBox();
    expect(del!.x - (edit!.x + edit!.width)).toBeGreaterThanOrEqual(8);
  });

  test('the Jobs search placeholder fits at 768 px', async ({ page }) => {
    await page.setViewportSize({ width: 768, height: 1024 });
    await page.goto('/#/jobs');
    const search = page.getByRole('searchbox', { name: /Search jobs/ });
    const fits = await search.evaluate((el: HTMLInputElement) => {
      const ctx = document.createElement('canvas').getContext('2d')!;
      ctx.font = getComputedStyle(el).font;
      const pad = parseFloat(getComputedStyle(el).paddingLeft) + parseFloat(getComputedStyle(el).paddingRight);
      return ctx.measureText(el.placeholder).width <= el.clientWidth - pad;
    });
    expect(fits).toBe(true);
  });

  test('Recover Vault heading lines up with other pages', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' }); // no route fade-up mid-measure
    await page.setViewportSize({ width: 1440, height: 900 });
    const settled = () => page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
    await page.goto('/#/jobs');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await settled();
    const jobs = await page.getByRole('heading', { level: 1 }).boundingBox();
    await page.goto('/#/recover');
    await expect(page.getByRole('heading', { level: 1, name: 'Recover Vault' })).toBeVisible();
    await settled();
    const recover = await page.getByRole('heading', { level: 1, name: 'Recover Vault' }).boundingBox();
    // Before #699 it sat at 48/488 against 24/304. Allow subpixel header alignment.
    expect(Math.abs(recover!.x - jobs!.x)).toBeLessThanOrEqual(2);
    expect(Math.abs(recover!.y - jobs!.y)).toBeLessThanOrEqual(2);
  });

  test('Settings prose is capped and focused fields clear the sticky bar', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto('/#/settings');
    const about = page.locator('#set-about p').first();
    const width = await about.evaluate((el) => el.getBoundingClientRect().width / parseFloat(getComputedStyle(el).fontSize));
    expect(width).toBeLessThanOrEqual(46); // 70ch of a 14px system font is well under 46em
    const field = page.locator('#throttle-link');
    await field.focus();
    const bar = await page.locator('.sticky').first().boundingBox();
    const box = await field.boundingBox();
    expect(box!.y).toBeGreaterThanOrEqual(bar!.y + bar!.height - 1);
  });
});

test.describe('edit link from a History error (#711)', () => {
  test('opens the job editor at the container exclusions', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/jobs?edit=1&item=plex');
    const field = page.getByRole('textbox', { name: /Additional path exclusions for plex/ });
    await expect(field).toBeVisible();
    await expect(field).toBeFocused();
    await expect(page).toHaveURL(/#\/jobs$/);
  });

  test('says so when the job no longer exists', async ({ page }) => {
    await setupVaultMockApi(page);
    await page.goto('/#/jobs?edit=999&item=plex');
    await expect(page.getByText('That job no longer exists')).toBeVisible();
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });
});
