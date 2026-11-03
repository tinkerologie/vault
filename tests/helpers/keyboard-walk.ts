import type { Frame, Page } from '@playwright/test';

/** Elements a screen reader announces as a control. A widget inside another is a nesting defect. */
export const WIDGETS = [
  'a[href]', 'button', 'input:not([type="hidden"])', 'select', 'textarea', 'summary',
  '[role="button"]', '[role="link"]', '[role="tab"]', '[role="switch"]', '[role="checkbox"]',
  '[role="radio"]', '[role="menuitem"]', '[role="option"]', '[role="slider"]',
].join(', ');

export interface TabStop {
  /** First line of the element's ARIA snapshot, e.g. `button "Edit job"`. */
  announced: string;
  /** The element has a role and a non-empty accessible name. */
  named: boolean;
  /** Some focus indicator (outline, shadow, border, background) differs from the unfocused state. */
  visibleFocus: boolean;
  /** The element sits inside another widget. */
  nestedIn: string | null;
}

export interface Walk {
  stops: TabStop[];
  /** Visible, enabled widgets in the page that Tab never reached and that aren't part of a roving group. */
  unreached: string[];
}

type WalkWindow = Window & { __walk?: Element[] };

/**
 * Presses Tab from the start of the document until focus wraps, leaves the frame
 * or `limit` stops pass, and describes each stop as a screen reader would meet it.
 */
export async function tabWalk(page: Page, frame: Frame, limit = 300): Promise<Walk> {
  await frame.evaluate(() => {
    (document.activeElement as HTMLElement | null)?.blur?.();
    (window as WalkWindow).__walk = [];
  });
  const stops: TabStop[] = [];
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press('Tab');
    const state = await frame.evaluate((widgets) => {
      const el = document.activeElement;
      if (!el || el === document.body || !document.hasFocus()) return 'left';
      const seen = (window as WalkWindow).__walk!;
      if (seen.includes(el)) return 'wrapped';
      seen.push(el);
      const outer = el.parentElement?.closest(widgets);
      return outer ? outer.outerHTML.slice(0, 120) : null;
    }, WIDGETS);
    if (state === 'left' || state === 'wrapped') break;
    const snapshot = await frame.locator(':focus').ariaSnapshot().catch(() => '');
    const announced = (snapshot.split('\n')[0] ?? '').replace(/^- /, '').replace(/:$/, '').replace(/^'(.*)'$/, (_, inner: string) => inner.replace(/''/g, "'"));
    stops.push({ announced, named: /^[\w-]+ "[^"]*\S[^"]*"/.test(announced), visibleFocus: true, nestedIn: state });
  }

  // Compare each stop's focused look with its resting look.
  const looks = await frame.evaluate(async () => {
    const props = ['outline-style', 'outline-width', 'outline-color', 'box-shadow', 'border-color', 'background-color', 'text-decoration-line'];
    const look = (el: Element) => { const cs = getComputedStyle(el); return props.map((p) => cs.getPropertyValue(p)).join('|'); };
    const seen = (window as WalkWindow).__walk!;
    const focused: string[] = [];
    for (const el of seen) {
      (el as HTMLElement).focus({ preventScroll: true });
      focused.push(el.isConnected ? look(el) : '');
    }
    (document.activeElement as HTMLElement | null)?.blur?.();
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    return seen.map((el, i) => !el.isConnected || focused[i] !== look(el));
  });
  // Programmatic focus doesn't always match :focus-visible, so a stop counts as
  // visible if either the keyboard pass or the replay shows a difference.
  looks.forEach((ok, i) => { if (stops[i]) stops[i].visibleFocus = ok; });

  const unreached = await frame.evaluate((widgets) => {
    const seen = new Set((window as WalkWindow).__walk!);
    return [...document.querySelectorAll<HTMLElement>(widgets)]
      .filter((el) => !seen.has(el))
      .filter((el) => el.checkVisibility() && el.getBoundingClientRect().width > 0)
      .filter((el) => !el.closest('[inert]') && !(el as HTMLButtonElement).disabled && el.getAttribute('aria-disabled') !== 'true')
      // Roving groups (tabs, radios) keep one member in the Tab order on purpose.
      .filter((el) => el.tabIndex >= 0 && !(el instanceof HTMLInputElement && el.type === 'radio' && !el.checked))
      // Content inside a closed <details> is skipped by Tab; its summary is reached instead.
      .filter((el) => !el.closest('details:not([open]) > :not(summary)'))
      .map((el) => el.outerHTML.slice(0, 120));
  }, WIDGETS);

  return { stops, unreached };
}
