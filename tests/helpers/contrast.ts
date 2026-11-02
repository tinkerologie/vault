import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';

/**
 * Shared colour-contrast tooling for Playwright specs.
 *
 * axe's `color-contrast` rule does the measuring, so alpha compositing, ancestor
 * backgrounds, large-text thresholds and disabled-control exemptions are handled in
 * one place. Nothing here excludes elements or lowers a threshold: to audit another
 * palette, seed its theme and call the same functions.
 */

export type ThemeStyle = 'default' | '1bit' | '8bit' | '16bit';
export type ThemeMode = 'light' | 'dark';

export interface ContrastOptions {
  /** CSS selector limiting the audit to one subtree, e.g. a hovered button. */
  include?: string;
}

export interface ContrastFinding {
  selector: string;
  text: string;
  foreground?: string;
  background?: string;
  ratio?: number;
  required?: string;
  /** axe's reason for an incomplete result, such as `bgGradient` or `bgOverlap`. */
  reason?: string;
}

export interface ContrastResult {
  violations: ContrastFinding[];
  /** Elements axe could not measure, such as text over gradients or overlapping layers. */
  incomplete: ContrastFinding[];
}

/** Seeds the persisted theme. Call before the first navigation. */
export async function seedTheme(page: Page, style: ThemeStyle, mode: ThemeMode): Promise<void> {
  await page.addInitScript(
    ([s, m]) => {
      localStorage.setItem('vault-style', s);
      localStorage.setItem('vault-mode', m);
    },
    [style, mode],
  );
}

/** Finishes colour transitions and entrance animations so measurements see final colours. */
export async function settleMotion(page: Page): Promise<void> {
  await page.emulateMedia({ reducedMotion: 'reduce' });
}

async function finishAnimations(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const finite = document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations !== Infinity);
    await Promise.all(finite.map((a) => a.finished.catch(() => undefined)));
  });
}

type AxeNode = Awaited<ReturnType<AxeBuilder['analyze']>>['violations'][number]['nodes'][number];

function toFinding(node: AxeNode): ContrastFinding {
  const data = node.any[0]?.data as
    | { fgColor?: string; bgColor?: string; contrastRatio?: number; expectedContrastRatio?: string; messageKey?: string }
    | undefined;
  return {
    selector: node.target.join(' '),
    text: node.html.slice(0, 160),
    foreground: data?.fgColor,
    background: data?.bgColor,
    ratio: data?.contrastRatio,
    required: data?.expectedContrastRatio,
    reason: data?.messageKey,
  };
}

/** Runs only axe's color-contrast rule against the current page state. */
export async function auditContrast(page: Page, options: ContrastOptions = {}): Promise<ContrastResult> {
  await finishAnimations(page);
  let builder = new AxeBuilder({ page }).withRules(['color-contrast']);
  if (options.include) builder = builder.include(options.include);
  const results = await builder.analyze();
  return {
    violations: results.violations.flatMap((rule) => rule.nodes.map(toFinding)),
    incomplete: results.incomplete.flatMap((rule) => rule.nodes.map(toFinding)),
  };
}

/** Fails on any violation, and logs what axe could not measure so it stays reviewable. */
export async function expectNoContrastViolations(
  page: Page,
  label: string,
  options: ContrastOptions = {},
): Promise<ContrastResult> {
  const result = await auditContrast(page, options);
  if (result.incomplete.length > 0) {
    console.log(`[contrast:incomplete] ${label}: ${JSON.stringify(result.incomplete)}`);
  }
  expect(result.violations, `${label}: color-contrast violations`).toEqual([]);
  return result;
}
