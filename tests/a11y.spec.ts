import AxeBuilder from '@axe-core/playwright';
import { test, expect, type Frame, type Locator, type Page } from '@playwright/test';
import { setupVaultMockApi } from './mocks/vault-api';
import { seedTheme, settleMotion } from './helpers/contrast';
import { tabWalk } from './helpers/keyboard-walk';
import { BANNER_STATES } from './helpers/backup-status';
import { FRAME_ID, ROUTES, VIEWPORTS, openPresentation } from './helpers/presentation';

// Structural axe audit (#529). Colour contrast has its own suite across every
// theme in contrast.spec.ts; that is the only rule left out here.
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];

async function audit(page: Page, label: string, frame?: Frame) {
  await (frame ?? page).evaluate(() => Promise.all(document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations !== Infinity).map((a) => a.finished.catch(() => undefined))));
  let builder = new AxeBuilder({ page }).withTags(TAGS).disableRules(['color-contrast']);
  // Embedded: audit only Vault's document inside the host's frame.
  if (frame) builder = builder.include([`#${FRAME_ID}`, 'html'] as never);
  const { violations, passes } = await builder.analyze();
  // Guard against an empty audit: inside a frame, axe qualifies each target with the frame.
  if (frame) expect(passes.some((r) => r.nodes.some((n) => n.target.length > 1)), `${label}: audited the frame`).toBe(true);
  const summary = violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')} :: ${n.html.slice(0, 140)}`));
  expect(summary, `${label}: axe violations`).toEqual([]);
}

const PAGES = ROUTES;

test.describe('axe structural audit (#529)', () => {
  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
    await setupVaultMockApi(page);
  });

  for (const { name, hash, ready } of PAGES) {
    test(`${name} desktop`, async ({ page }) => {
      await page.goto(`/${hash}`);
      await expect(page.locator(ready).first()).toBeVisible();
      await audit(page, name);
    });

    test(`${name} at 375 px`, async ({ page }) => {
      await page.setViewportSize({ width: 375, height: 812 });
      await page.goto(`/${hash}`);
      await expect(page.locator(ready).first()).toBeVisible();
      await audit(page, `${name} mobile`);
    });
  }

  for (const tab of ['Notifications', 'Security', 'Reference']) {
    test(`Settings ${tab} tab`, async ({ page }) => {
      await page.goto('/#/settings');
      await page.getByRole('tab', { name: tab, exact: true }).click();
      await expect(page.getByRole('tabpanel', { name: tab })).toBeVisible();
      await audit(page, `Settings ${tab}`);
    });
  }

  test('Dashboard in Customize mode', async ({ page }) => {
    await page.goto('/#/');
    await page.getByRole('button', { name: 'Customize' }).click();
    await expect(page.getByRole('button', { name: 'Done' })).toBeVisible();
    await audit(page, 'Customize');
  });

  for (const { name, options, title } of BANNER_STATES) {
    test(`Dashboard status banner: ${name}`, async ({ page }) => {
      await setupVaultMockApi(page, options);
      await page.goto('/#/');
      await expect(page.getByRole('region', { name: 'Backup status' }).getByRole('heading', { name: title })).toBeVisible();
      await audit(page, `Banner ${name}`);
    });
  }

  test('Replication with sources', async ({ page }) => {
    await setupVaultMockApi(page, { replicationSources: [{ id: 1, name: 'Primary Tower', url: 'http://tower.example.test:24085', enabled: true, schedule: '0 3 * * *', last_sync_at: '2026-10-06T03:00:00Z', last_sync_status: 'success', last_sync_error: '', storage_dest_id: 1 }] });
    await page.goto('/#/replication');
    await expect(page.getByRole('heading', { name: 'Primary Tower' })).toBeVisible();
    await audit(page, 'Replication sources');
  });

  test('Job editor dialog', async ({ page }) => {
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await audit(page, 'Job editor');
  });

  test('Restore wizard plan step', async ({ page }) => {
    await page.goto('/#/restore');
    await page.getByRole('button', { name: /plex/i }).click();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('button', { name: /Docker & Appdata/i }).first().click();
    const summary = page.locator('summary', { hasText: 'plex' });
    await summary.click();
    await expect(page.getByRole('textbox', { name: 'Filter paths in plex' })).toBeVisible();
    await audit(page, 'Restore plan');
  });

  test('open More sheet', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: 'More', exact: true }).click();
    await expect(page.getByRole('dialog', { name: 'More' })).toBeVisible();
    await audit(page, 'More sheet');
  });
});

// The embedded Backups view (#526) renders its own link row instead of the
// sidebar and bottom bar, so each page gets the same audit inside the frame.
test.describe('axe structural audit, embedded (#529)', () => {
  for (const viewport of VIEWPORTS) {
    test.describe(`at ${viewport.name}`, () => {
      test.use({ viewport: { width: viewport.width, height: viewport.height } });
      test.beforeEach(async ({ page }) => {
        await settleMotion(page);
        await setupVaultMockApi(page);
      });

      for (const { name, hash, ready } of PAGES) {
        test(name, async ({ page }) => {
          const frame = await openPresentation(page, 'embedded', hash);
          await expect(frame.locator(ready).first()).toBeVisible();
          await audit(page, `embedded ${name} ${viewport.name}`, frame);
        });
      }
    });
  }
});

// Screen-reader polish found by the audit above (#608). Each test fails if its
// fix is reverted: tooltip text leaking into names, row actions that all read
// the same, checkboxes named by their whole description, the Anomalies list
// without row structure, and 1-Bit links spaced unlike the buttons they replaced.
test.describe('screen reader polish (#608)', () => {
  const ANOMALIES = [
    { id: 11, scope_kind: 'job', scope_id: 1, summary: 'Backup size spike above baseline', detector: 'size', severity: 'warning', state: 'open', metric: 'size_bytes', observed: 10737418240, expected: 5368709120, deviation: 3.2, details: '{"z":3.2}', first_seen_at: '2026-10-05T02:00:00Z' },
    { id: 12, scope_kind: 'destination', scope_id: 2, summary: 'Backup duration spike', detector: 'duration', severity: 'critical', state: 'open', metric: 'duration_seconds', observed: 900, expected: 120, deviation: 5.1, details: '{"z":5.1}', first_seen_at: '2026-10-04T02:00:00Z' },
  ];

  test.beforeEach(async ({ page }) => {
    await settleMotion(page);
    await setupVaultMockApi(page, { anomalies: ANOMALIES });
  });

  /** Every "More info" trigger is named after what it explains, and no other name contains it. */
  async function expectTooltipNamesClean(scope: Locator, label: string) {
    const lines = (await scope.ariaSnapshot()).split('\n').filter((l) => l.includes('More info'));
    expect(lines.length, `${label}: has tooltip triggers`).toBeGreaterThan(0);
    expect(lines.filter((l) => !/^\s*- '?button "More info: [^"]+"/.test(l)), `${label}: names that carry a tooltip trigger, or bare "More info"`).toEqual([]);
  }

  const openAllDetails = (scope: Locator) => scope.locator('details').evaluateAll((all) => all.forEach((d) => { (d as HTMLDetailsElement).open = true; }));

  test('tooltip triggers stay out of field, heading and disclosure names', async ({ page }) => {
    await page.goto('/#/settings');
    // Reference has no tooltips in this fixture.
    for (const tab of ['General', 'Notifications', 'Security']) {
      await page.getByRole('tab', { name: tab, exact: true }).click();
      await expectTooltipNamesClean(page.getByRole('tabpanel', { name: tab }), `Settings ${tab}`);
    }

    await page.getByRole('tab', { name: 'General', exact: true }).click();
    const panel = page.getByRole('tabpanel', { name: 'General' });
    await expect(panel.getByRole('heading', { name: 'Retry Policy', exact: true })).toBeVisible();
    await audit(page, 'Settings General');
    // Walk before anything has focus, so Tab starts at the top of the page.
    const walk = await tabWalk(page, page.mainFrame());
    const announced = walk.stops.map((s) => s.announced);
    expect(announced).toContain('spinbutton "Max retries"');
    expect(announced).toContain('button "More info: Max retries"');
    expect(announced.filter((a) => a.includes('More info') && !a.startsWith('button "More info: '))).toEqual([]);
    const maxRetries = panel.getByRole('spinbutton', { name: 'Max retries', exact: true });
    await maxRetries.focus();
    // The trigger sits just before its field, so Shift+Tab reaches it and opens the tip.
    await page.keyboard.press('Shift+Tab');
    const trigger = panel.getByRole('button', { name: 'More info: Max retries', exact: true });
    await expect(trigger).toBeFocused();
    await expect(page.getByRole('tooltip')).toContainText('Maximum number of retry attempts');
    await expect(trigger).toHaveAccessibleDescription(/Maximum number of retry attempts/);
    // The open tip lives inside <main>, so it belongs to a landmark (axe "region").
    await expect(page.getByRole('main').getByRole('tooltip')).toBeVisible();
    await audit(page, 'Settings with a tooltip open');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('tooltip')).toHaveCount(0);

    // The job editor: labels, and summaries that keep their trigger inside.
    await page.goto('/#/jobs');
    await page.getByRole('button', { name: /new job/i }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: /Containers/i }).first().click();
    await dialog.getByRole('button', { name: /plex/i }).first().click();
    const next = dialog.getByRole('button', { name: 'Next', exact: true });
    await next.click();
    await page.locator('#storage').selectOption({ label: 'Local Array Backup (local)' });
    await next.click();
    // The switches here are named by visible labels, not only aria-labelledby.
    await expect(dialog.getByLabel('Verify backups after writing', { exact: true })).toBeAttached();
    await audit(page, 'Job editor options');
    await openAllDetails(dialog);
    await expectTooltipNamesClean(dialog, 'Job editor options, all open');
    // Summary triggers sit beside the <summary>, not in it (axe "nested-interactive"),
    // on the same row, right of the title.
    const summary = dialog.locator('summary', { hasText: 'Retention Policy' });
    await expect(summary).toHaveAccessibleName('Retention Policy');
    await expect(summary.getByRole('button')).toHaveCount(0);
    const tip = dialog.getByRole('button', { name: 'More info: Retention Policy', exact: true });
    const [s, t] = [(await summary.boundingBox())!, (await tip.boundingBox())!];
    expect(Math.abs((t.y + t.height / 2) - (s.y + s.height / 2))).toBeLessThan(1);
    expect(t.x).toBeGreaterThan(s.x + 100);
    // Opening the tip leaves the disclosure as it was.
    const details = summary.locator('xpath=..');
    await tip.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('tooltip')).toContainText('Keep Last N');
    await expect(details).toHaveAttribute('open', '');
    await audit(page, 'Job editor options, all open');
    // Escape closes the tip and leaves the job editor open.
    await page.keyboard.press('Escape');
    await expect(page.getByRole('tooltip')).toHaveCount(0);
    await expect(dialog).toBeVisible();
    await next.click();
    await expect(dialog.getByRole('textbox', { name: 'Job Name', exact: true })).toBeVisible();
    await expectTooltipNamesClean(dialog, 'Job editor review');
    await audit(page, 'Job editor review');
  });

  test('an open tooltip stays visible and on top in the embedded view', async ({ page }) => {
    const frame = await openPresentation(page, 'embedded', '#/settings');
    const trigger = frame.getByRole('button', { name: 'More info: Max retries', exact: true });
    await trigger.focus();
    const tip = frame.getByRole('tooltip');
    await expect(tip).toBeVisible();
    await expect(frame.getByRole('main').getByRole('tooltip')).toHaveCount(1);
    const topmost = await tip.evaluate((el) => {
      // The popup ignores the pointer; let hit-testing see it for this check.
      (el as HTMLElement).style.pointerEvents = 'auto';
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      (el as HTMLElement).style.pointerEvents = '';
      return !!hit && el.contains(hit);
    });
    expect(topmost).toBe(true);
    await audit(page, 'embedded Settings with a tooltip open', frame);
  });

  test('row actions name their item', async ({ page }) => {
    await page.goto('/#/jobs');
    const main = page.getByRole('main');
    for (const job of ['Docker & Appdata', 'VM Backups']) {
      for (const action of ['Run backup now', 'Duplicate job', 'Edit job', 'Delete job']) {
        await expect(main.getByRole('button', { name: `${action}: ${job}`, exact: true })).toBeVisible();
      }
    }
    let walk = await tabWalk(page, page.mainFrame());
    expect(walk.stops.map((s) => s.announced)).toContain('button "Edit job: VM Backups"');
    await audit(page, 'Jobs row actions');

    await page.goto('/#/');
    for (const job of ['Docker & Appdata', 'VM Backups']) {
      await expect(main.getByRole('link', { name: `Restore: ${job}`, exact: true })).toHaveText('Restore');
      await expect(main.getByRole('button', { name: `Run Now: ${job}`, exact: true })).toHaveText('Run Now');
    }

    await page.goto('/#/storage');
    for (const dest of ['Local Array Backup', 'Offsite S3', 'Backup NAS']) {
      for (const action of ['Browse destination files', 'Import backups', 'Edit storage', 'Delete storage']) {
        await expect(main.getByRole('button', { name: `${action}: ${dest}`, exact: true })).toBeVisible();
      }
    }
    // Visible text is unchanged; only the name gains the item.
    await expect(main.getByRole('button', { name: 'Refresh: Local Array Backup', exact: true })).toHaveText('Refresh');
    walk = await tabWalk(page, page.mainFrame());
    expect(walk.stops.map((s) => s.announced)).toContain('button "Edit storage: Backup NAS"');
    const test = main.getByRole('button', { name: 'Test: Offsite S3', exact: true });
    await test.focus();
    await page.keyboard.press('Enter');
    // The name keeps the state text first and the item last.
    await expect(main.getByRole('button', { name: 'Connected: Offsite S3', exact: true })).toBeVisible();
    await audit(page, 'Storage row actions');

    await page.goto('/#/anomalies');
    for (const name of ['Backup size spike above baseline (Job #1)', 'Backup duration spike (Dest #2)']) {
      await expect(main.getByRole('button', { name: `Dismiss: ${name}`, exact: true })).toBeVisible();
      await expect(main.getByRole('button', { name: `Expected: ${name}`, exact: true })).toBeVisible();
    }
    walk = await tabWalk(page, page.mainFrame());
    const names = walk.stops.map((s) => s.announced).filter((a) => /^button "(Dismiss|Expected)/.test(a));
    expect(names).toHaveLength(4);
    expect(new Set(names).size).toBe(4);
  });

  async function checkStorageCheckboxes(page: Page, scope: Locator, label: string) {
    const stage = scope.getByRole('checkbox', { name: 'Stage temporary backups beside this destination', exact: true });
    await expect(stage).toHaveAccessibleDescription(/Prefers a hidden \.vault-stage folder/);
    const dedup = scope.getByRole('checkbox', { name: 'Enable deduplication', exact: true });
    await expect(dedup).toHaveAccessibleDescription(/Stores only changed data blocks/);
    for (const box of [stage, dedup]) {
      await box.focus();
      await page.keyboard.press('Space');
      await expect(box).toBeChecked();
    }
    await audit(page, `${label} local`);

    await scope.getByRole('button', { name: 'WebDAV' }).click();
    const tls = scope.getByRole('checkbox', { name: 'Allow self-signed TLS certificates', exact: true });
    await tls.focus();
    await page.keyboard.press('Space');
    await expect(tls).toBeChecked();
    await expect(scope.getByRole('button', { name: 'More info: Allow self-signed TLS certificates', exact: true })).toBeVisible();

    await scope.getByRole('button', { name: /^S3/ }).click();
    const pathStyle = scope.getByRole('checkbox', { name: 'Force path-style addressing', exact: true });
    await pathStyle.focus();
    await page.keyboard.press('Space');
    await expect(pathStyle).toBeChecked();
    await openAllDetails(scope);
    await expectTooltipNamesClean(scope, `${label} S3`);
    await audit(page, `${label} S3`);
  }

  test('storage form checkboxes have short names and separate descriptions', async ({ page }) => {
    await page.goto('/#/storage');
    await page.getByRole('button', { name: /add storage/i }).click();
    await checkStorageCheckboxes(page, page.getByRole('dialog'), 'Add Storage');
    await page.keyboard.press('Escape');

    // Editing: deduplication is locked, and the description says why.
    await page.getByRole('button', { name: 'Edit storage: Local Array Backup', exact: true }).click();
    const dedup = page.getByRole('dialog').getByRole('checkbox', { name: 'Enable deduplication', exact: true });
    await expect(dedup).toBeDisabled();
    await expect(dedup).toHaveAccessibleDescription(/Stores only changed data blocks.*Dedup mode is locked at creation time/);
    await audit(page, 'Edit Storage');

    // The Recover wizard reuses the same form.
    await page.goto('/#/recover');
    await checkStorageCheckboxes(page, page.getByRole('main'), 'Recover wizard');
  });

  for (const viewport of VIEWPORTS) {
    test(`Anomalies rows are headings with an expandable panel at ${viewport.name}`, async ({ page }) => {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await page.goto('/#/anomalies');
      const main = page.getByRole('main');
      const walk = await tabWalk(page, page.mainFrame());
      expect(walk.stops.map((s) => s.announced)).toContain('button "Backup size spike above baseline size"');
      expect(walk.stops.filter((s) => !s.named)).toEqual([]);

      const rows = main.getByRole('heading', { level: 2 });
      await expect(rows).toHaveCount(2);
      await expect(rows.nth(0)).toHaveAccessibleName('Backup size spike above baseline size');
      await expect(rows.nth(1)).toHaveAccessibleName('Backup duration spike duration');

      const toggle = rows.nth(0).getByRole('button');
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      await toggle.focus();
      await page.keyboard.press('Enter');
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
      const panelId = await toggle.getAttribute('aria-controls');
      const panel = page.locator(`[id="${panelId}"]`);
      await expect(panel).toBeVisible();
      await expect(panel).toContainText('Deviation');
      await panel.getByText('Raw details').click();
      await expect(panel.locator('pre')).toBeVisible();
      await audit(page, `Anomalies expanded ${viewport.name}`);
    });
  }

  test('1-Bit links keep the letter-spacing of the buttons they replaced, in every theme', async ({ browser }) => {
    for (const style of ['default', '1bit', 'retro', 'dracula', 'catppuccin'] as const) {
      const context = await browser.newContext();
      const page = await context.newPage();
      await settleMotion(page);
      await seedTheme(page, style, 'dark');
      await setupVaultMockApi(page);
      await page.goto('/#/recovery');
      for (const name of ['Recover Vault', 'Configure backup jobs →']) {
        const link = page.getByRole('link', { name, exact: true });
        await link.focus();
        await expect(link).toBeFocused();
        // A button with the link's classes, beside it, is what #607 replaced.
        const [linkSpacing, buttonSpacing] = await link.evaluate((a) => {
          const b = document.createElement('button');
          b.className = a.className;
          b.textContent = a.textContent;
          a.after(b);
          const spacing = [getComputedStyle(a).letterSpacing, getComputedStyle(b).letterSpacing];
          b.remove();
          return spacing;
        });
        expect(linkSpacing, `${style}: ${name}`).toBe(buttonSpacing);
      }
      if (style === '1bit') {
        await expect(page.locator('html')).toHaveClass(/theme-1bit/);
        await expect(page.getByRole('link', { name: 'Configure backup jobs →', exact: true })).toHaveCSS('letter-spacing', '-0.5px');
        await audit(page, 'Recovery in 1-Bit');
      }
      await context.close();
    }
  });
});
